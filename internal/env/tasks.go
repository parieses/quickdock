package env

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

// 运行时的「特色操作」。
//
// 通用模型（安装 / 切版本 / 进 PATH / 编辑配置 / 看日志）能覆盖所有运行时，
// 但每个语言生态里都有一批只有它自己才有的高频动作：
// nvm 之外的 npm 全局包与 corepack、pyenv 之外的 pip 引导与缓存、
// go 的 modcache、composer 的全局包与缓存、JDK 的 toolchain 信息……
// 这些动作放进通用模型里会变成一堆特例弹窗，故收敛成一张「任务表」：
//
//   - 后端只声明「有哪些任务 + 危险级别」，命令实现就近写在各自的 task* 函数里；
//   - 执行结果统一是纯文本（键值清单 / 命令原始输出），前端用一个通用输出弹窗渲染；
//   - 文案交给前端 i18n（键名 = task_ + id 把点换成下划线），后端不出现用户可见中文长句。
//
// 危险动作（清缓存、删模块）一律 confirm=true，前端必须先确认再调用。

// TaskKind 任务类别，决定前端按钮语气与是否需要确认。
type TaskKind string

const (
	TaskInfo   TaskKind = "info"   // 只读：查状态、列清单
	TaskAction TaskKind = "action" // 写操作，可逆：启用 corepack、引导 pip
	TaskDanger TaskKind = "danger" // 清理/删除，不可逆
)

// RuntimeTask 供前端渲染菜单的任务声明。
type RuntimeTask struct {
	ID      string   `json:"id"`
	Kind    TaskKind `json:"kind"`
	Confirm bool     `json:"confirm"`
}

// TaskResult 任务执行结果。失败也返回结果（Error 非空），便于前端既看到
// 出错前的输出、又看到错误原因——很多命令失败时输出的正是最有用的信息。
type TaskResult struct {
	ID         string `json:"id"`
	Output     string `json:"output"`
	Error      string `json:"error"`
	Truncated  bool   `json:"truncated"`
	DurationMs int64  `json:"durationMs"`
}

const (
	// taskTimeout 单次任务上限。`go clean -modcache` 在机械盘上删几十万个小文件
	// 可能几十秒，故给到 3 分钟；真正的长任务（装包）不属于这里。
	taskTimeout = 180 * time.Second
	// taskMaxOutput 输出上限，防止 `npm ls -g` 之类在巨型环境下撑爆前端。
	taskMaxOutput = 256 << 10
)

type taskDef struct {
	id      string
	kind    TaskKind
	confirm bool
	run     func(ctx context.Context, e *taskExec) (string, error)
}

// taskDefsFor 返回某运行时支持的任务，顺序即菜单顺序：只读 → 可逆 → 清理。
func taskDefsFor(rt Runtime) []taskDef {
	switch rt {
	case RuntimeNode:
		return []taskDef{
			{id: "node.info", kind: TaskInfo, run: taskNodeInfo},
			{id: "node.globalList", kind: TaskInfo, run: taskNodeGlobalList},
			{id: "node.corepackEnable", kind: TaskAction, run: taskNodeCorepackEnable},
			{id: "node.cacheClean", kind: TaskDanger, confirm: true, run: taskNodeCacheClean},
		}
	case RuntimeBun:
		return []taskDef{
			{id: "bun.info", kind: TaskInfo, run: taskBunInfo},
			{id: "bun.globalList", kind: TaskInfo, run: taskBunGlobalList},
			{id: "bun.cacheClean", kind: TaskDanger, confirm: true, run: taskBunCacheClean},
		}
	case RuntimePython:
		return []taskDef{
			{id: "python.info", kind: TaskInfo, run: taskPythonInfo},
			{id: "python.pipList", kind: TaskInfo, run: taskPythonPipList},
			{id: "python.ensurepip", kind: TaskAction, run: taskPythonEnsurepip},
			{id: "python.pipUpgrade", kind: TaskAction, run: taskPythonPipUpgrade},
			{id: "python.cachePurge", kind: TaskDanger, confirm: true, run: taskPythonCachePurge},
		}
	case RuntimeGo:
		return []taskDef{
			{id: "go.info", kind: TaskInfo, run: taskGoInfo},
			{id: "go.binList", kind: TaskInfo, run: taskGoBinList},
			{id: "go.cleanBuildCache", kind: TaskDanger, confirm: true, run: taskGoCleanBuildCache},
			{id: "go.cleanModCache", kind: TaskDanger, confirm: true, run: taskGoCleanModCache},
		}
	case RuntimeComposer:
		return []taskDef{
			{id: "composer.info", kind: TaskInfo, run: taskComposerInfo},
			{id: "composer.globalList", kind: TaskInfo, run: taskComposerGlobalList},
			{id: "composer.clearCache", kind: TaskDanger, confirm: true, run: taskComposerClearCache},
		}
	case RuntimePHP:
		return []taskDef{
			{id: "php.info", kind: TaskInfo, run: taskPHPInfo},
			{id: "php.modules", kind: TaskInfo, run: taskPHPModules},
		}
	case RuntimeJDK:
		return []taskDef{
			{id: "jdk.info", kind: TaskInfo, run: taskJDKInfo},
		}
	case RuntimeNginx:
		return []taskDef{
			{id: "nginx.configTest", kind: TaskInfo, run: taskNginxConfigTest},
			{id: "nginx.versionDetail", kind: TaskInfo, run: taskNginxVersionDetail},
		}
	}
	return nil
}

// TasksFor 返回某运行时的任务声明；未支持时返回空切片（前端不渲染该分组）。
func TasksFor(rt Runtime) []RuntimeTask {
	defs := taskDefsFor(rt)
	out := make([]RuntimeTask, 0, len(defs))
	for _, d := range defs {
		out = append(out, RuntimeTask{ID: d.id, Kind: d.kind, Confirm: d.confirm})
	}
	return out
}

// RunTask 执行一个特色操作。version 为空时按该运行时当前激活/首个已装版本解析，
// 便于「系统 PATH 上的版本」也能用（bin 目录取 exe 所在目录）。
func (m *Manager) RunTask(rt Runtime, version, taskID string) *TaskResult {
	res := &TaskResult{ID: taskID}
	defs := taskDefsFor(rt)
	var def *taskDef
	for i := range defs {
		if defs[i].id == taskID {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		res.Error = fmt.Sprintf("未知操作: %s", taskID)
		return res
	}
	ver, err := m.ResolveVersion(rt, version)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	binDir := m.exeDirFor(rt, ver)
	if binDir == "" || !dirExists(binDir) {
		res.Error = fmt.Sprintf("未找到 %s %s 的可执行目录", DisplayName(rt), ver)
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)
	defer cancel()
	start := time.Now()
	out, runErr := def.run(ctx, &taskExec{rt: rt, version: ver, binDir: binDir, mgr: m})
	res.DurationMs = time.Since(start).Milliseconds()
	if runErr != nil {
		if ctx.Err() != nil {
			res.Error = fmt.Sprintf("执行超时（%s），请稍后重试或手动执行", taskTimeout)
		} else {
			res.Error = runErr.Error()
		}
	}
	if len(out) > taskMaxOutput {
		out = out[:taskMaxOutput] + "\n\n…（输出过长，已截断）"
		res.Truncated = true
	}
	res.Output = strings.TrimRight(out, "\r\n")
	return res
}

// taskExec 一次任务执行的上下文：定位到某个具体版本的 bin 目录，
// 所有命令都在该目录下执行、并把该目录前置进 PATH，
// 保证 `npm` / `pip` / `php` 用的是这个版本自带的，而不是系统 PATH 上碰巧存在的那份。
type taskExec struct {
	rt      Runtime
	version string
	binDir  string
	mgr     *Manager
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// tool 在版本 bin 目录内定位可执行文件，找不到再回退 PATH。
func (e *taskExec) tool(name string) (string, error) {
	if p := firstExe(e.binDir, name); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("未找到 %s 可执行文件（该版本目录与系统 PATH 中都没有）", name)
}

// firstExe 在目录内按平台补全后缀查找可执行文件，返回绝对路径（找不到返回 ""）。
func firstExe(dir string, names ...string) string {
	if dir == "" {
		return ""
	}
	for _, n := range names {
		cands := []string{n}
		if runtime.GOOS == "windows" {
			base := strings.TrimSuffix(strings.TrimSuffix(n, ".exe"), ".cmd")
			if !strings.Contains(base, ".") {
				cands = []string{base + ".exe", base + ".cmd", base + ".bat"}
			}
		}
		for _, c := range cands {
			p := filepath.Join(dir, c)
			if fileExists(p) && !dirExists(p) {
				return p
			}
		}
	}
	return ""
}

// env 构造子进程环境：PATH 前置版本 bin 目录。
func (e *taskExec) env() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			if e.binDir != "" {
				kv = "PATH=" + e.binDir + string(os.PathListSeparator) + kv[len("PATH="):]
			}
			out = append(out, kv)
		}
	}
	return out
}

func isBatchFile(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".cmd" || ext == ".bat"
}

// runBin 执行并合并 stdout/stderr。
//
// Windows 上 npm / corepack 都是 .cmd：CreateProcess 不能直接拉起批处理，
// 必须经 cmd /c call 转一手（与 rabbitmq.go 里 plugins.bat 的处理一致）。
func (e *taskExec) runBin(ctx context.Context, bin string, args ...string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" && isBatchFile(bin) {
		sh := os.Getenv("ComSpec")
		if sh == "" {
			sh = "cmd.exe"
		}
		cmd = sysutil.CommandContext(ctx, sh, append([]string{"/c", "call", bin}, args...)...)
	} else {
		cmd = sysutil.CommandContext(ctx, bin, args...)
	}
	cmd.Dir = e.binDir
	cmd.Env = e.env()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimRight(buf.String(), "\r\n")
	if err != nil {
		return out, fmt.Errorf("执行 %s 失败: %v", filepath.Base(bin), err)
	}
	return out, nil
}

// run 按名字定位可执行文件并执行。
func (e *taskExec) run(ctx context.Context, name string, args ...string) (string, error) {
	bin, err := e.tool(name)
	if err != nil {
		return "", err
	}
	return e.runBin(ctx, bin, args...)
}

// try 执行并把「失败」降级成一行说明：信息类任务由多条子命令拼成，
// 其中某条不可用（例如 embeddable python 没装 pip）不该让整块信息失败。
func (e *taskExec) try(ctx context.Context, name string, args ...string) string {
	out, err := e.run(ctx, name, args...)
	if err != nil {
		if strings.TrimSpace(out) != "" {
			return firstLine(out) + "（返回非零）"
		}
		return "—（" + err.Error() + "）"
	}
	if strings.TrimSpace(out) == "" {
		return "—"
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// kv 输出一行「标签  值」。按显示宽度对齐（中文算 2 列），否则中英混排的键名会参差。
func kv(b *strings.Builder, label, value string) {
	const width = 16
	pad := label
	for dispWidth(pad) < width {
		pad += " "
	}
	b.WriteString(pad + value + "\n")
}

func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x2E80 { // 中日韩全角区，按两列计
			w += 2
		} else {
			w++
		}
	}
	return w
}

// ---- Node / npm ----

// taskNodeInfo 汇总 Node 环境。
// 只用一次 `npm config list` 取版本/全局目录/源，而不是逐个 `npm config get`——
// 每次 npm 调用在 Windows 上都要 1~2 秒（Node 启动 + 读配置），拼 5 条就是 9 秒的等待。
func taskNodeInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	cfg := e.try(ctx, "npm", "config", "list")
	kv(&b, "Node", e.try(ctx, "node", "-v"))
	kv(&b, "npm", npmField(cfg, "npm version"))
	kv(&b, "全局目录", npmGlobalDir(npmField(cfg, "node bin location")))
	kv(&b, "npm 源", npmField(cfg, "registry"))
	kv(&b, "缓存目录", e.try(ctx, "npm", "config", "get", "cache"))
	kv(&b, "corepack", e.try(ctx, "corepack", "--version"))
	kv(&b, "版本目录", e.binDir)
	return b.String(), nil
}

// npmGlobalDir 由 `node bin location` 推出 npm 全局包目录。
// Windows 便携版 node.exe 与 node_modules 同级；类 Unix 下 node 在 <prefix>/bin 里，
// 所以要多上一层才是 prefix。
func npmGlobalDir(nodeBin string) string {
	if nodeBin == "" || nodeBin == "—" {
		return "—"
	}
	prefix := filepath.Dir(nodeBin)
	if runtime.GOOS != "windows" {
		prefix = filepath.Dir(prefix)
	}
	return filepath.Join(prefix, "node_modules")
}

// npmField 从 `npm config list` 输出里取一个字段。
// 用户配置是 `registry = "https://…"`，运行时信息则是注释形式 `; npm version = 11.19.1`，
// 两种都去掉开头分号与引号后按 `key = value` 匹配。
func npmField(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		l = strings.TrimSpace(strings.TrimPrefix(l, ";"))
		if !strings.HasPrefix(l, key+" =") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(l, key+" ="))
		return strings.Trim(v, "\"")
	}
	return "—"
}

func taskNodeGlobalList(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "npm", "ls", "-g", "--depth=0")
	return out, err
}

func taskNodeCorepackEnable(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "corepack", "enable")
	if err != nil {
		if strings.Contains(out, "EPERM") || strings.Contains(out, "EACCES") {
			return out, fmt.Errorf("启用失败：没有写权限（便携 Node 目录可能被占用或只读）")
		}
		return out, err
	}
	ver := e.try(ctx, "corepack", "--version")
	return strings.TrimSpace(out + "\n\nCorepack 已启用，版本 " + ver +
		"\n现在项目里 package.json 的 packageManager 字段即可自动切换 pnpm / yarn 版本。"), nil
}

func taskNodeCacheClean(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "npm", "cache", "clean", "--force")
	if err != nil {
		return out, err
	}
	verify := e.try(ctx, "npm", "cache", "verify")
	return strings.TrimSpace(out + "\n" + verify), nil
}

// ---- Bun ----

func taskBunInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	kv(&b, "Bun", e.try(ctx, "bun", "--version"))
	kv(&b, "全局 bin", e.try(ctx, "bun", "pm", "bin", "-g"))
	kv(&b, "缓存目录", e.try(ctx, "bun", "pm", "cache"))
	kv(&b, "版本目录", e.binDir)
	return b.String(), nil
}

func taskBunGlobalList(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "bun", "pm", "ls", "-g")
	return out, err
}

func taskBunCacheClean(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "bun", "pm", "cache", "rm")
	if err != nil {
		return out, err
	}
	return strings.TrimSpace(out + "\nbun 包缓存已清空。"), nil
}

// ---- Python / pip ----

func taskPythonInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	kv(&b, "Python", e.try(ctx, "python", "-V"))
	kv(&b, "解释器", e.try(ctx, "python", "-c", "import sys;print(sys.executable)"))
	kv(&b, "site-packages", e.try(ctx, "python", "-c", "import sysconfig;print(sysconfig.get_path('purelib'))"))
	kv(&b, "pip", e.try(ctx, "python", "-m", "pip", "--version"))
	kv(&b, "venv", pythonVenvState(e.binDir))
	kv(&b, "版本目录", e.binDir)
	return b.String(), nil
}

// pythonVenvState 判断该解释器是否带 venv / ensurepip 模块——embeddable 包通常两者皆无，
// 这是「pip 用不了」的最常见原因，直接显示出来比让用户猜要好。
//
// 对 embeddable（无内置 venv）额外显示 virtualenv 状态：
// 引导 pip 时会顺带装上 virtualenv，它是该场景下唯一能建虚拟环境的途径，
// 只报「venv 不可用」会让用户以为完全没法建虚拟环境。
func pythonVenvState(binDir string) string {
	py := firstExe(binDir, "python", "python3")
	if py == "" {
		return "—"
	}
	out := RunVersion(py, "-c", "import importlib.util as u;print('venv' if u.find_spec('venv') else '-', 'ensurepip' if u.find_spec('ensurepip') else '-')")
	if strings.TrimSpace(out) == "" {
		return "—"
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return out
	}
	desc := []string{}
	if parts[0] == "venv" {
		desc = append(desc, "可用")
	} else {
		desc = append(desc, "不可用")
		if pythonHasVirtualenv(py) {
			desc = append(desc, "可用 virtualenv 代替")
		} else {
			desc = append(desc, "执行「引导 pip」后可用 virtualenv 代替")
		}
	}
	if parts[1] == "ensurepip" {
		desc = append(desc, "ensurepip 可用")
	} else {
		desc = append(desc, "ensurepip 不可用")
	}
	return strings.Join(desc, "，")
}

func taskPythonPipList(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "python", "-m", "pip", "list")
	if err != nil && strings.Contains(out, "No module named pip") {
		return out, fmt.Errorf("该 Python 未安装 pip，可先执行「引导 pip」")
	}
	return out, err
}

// taskPythonEnsurepip 为该解释器引导 pip。
//
// 完整版 Python 自带 pip（或可 ensurepip），一步即成；
// 而 QuickDock 装的是 python.org embeddable 便携包——它既不含 pip 也不含 ensurepip 模块
// （`python -m ensurepip` 必然报 "No module named ensurepip"），
// 且 python3XX._pth 默认把 `import site` 注释掉，导致 site-packages 根本不进 sys.path。
// 故对 embeddable 走专用流程：先修 _pth 启用 site，再下载 get-pip.py 装 pip。
// 详见 python_embed.go。
func taskPythonEnsurepip(ctx context.Context, e *taskExec) (string, error) {
	lay := detectPythonEmbedLayout(e.binDir)
	if lay == nil {
		// 非 embeddable：走标准 ensurepip
		out, err := e.run(ctx, "python", "-m", "ensurepip", "--upgrade")
		if err != nil {
			if strings.Contains(out, "No module named ensurepip") {
				return out, fmt.Errorf("该发行版不含 ensurepip，且不是 embeddable 便携包，请手动安装 pip")
			}
			return out, err
		}
		return strings.TrimSpace(out + "\n\n" + e.try(ctx, "python", "-m", "pip", "--version")), nil
	}
	// embeddable：修 _pth + get-pip.py（多源回退，见 python_embed.go）
	return bootstrapPythonPip(ctx, lay, nil)
}

func taskPythonPipUpgrade(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "python", "-m", "pip", "install", "--upgrade", "pip")
	return out, err
}

func taskPythonCachePurge(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "python", "-m", "pip", "cache", "purge")
	return out, err
}

// ---- Go ----

// goRuntime 取出 Go adapter；tasks.go 里需要它的 envBin/goVersionEnv 等版本隔离细节。
func (e *taskExec) goRuntime() (*GoRuntime, error) {
	a, err := e.mgr.adapter(RuntimeGo)
	if err != nil {
		return nil, err
	}
	gr, ok := a.(*GoRuntime)
	if !ok {
		return nil, fmt.Errorf("Go 运行时类型不匹配")
	}
	return gr, nil
}

// goRun 以该版本的 GOENV 执行 go 子命令（与 go.go 的 runGo 等价，但带 ctx 超时）。
func (e *taskExec) goRun(ctx context.Context, g *GoRuntime, args ...string) (string, error) {
	bin, err := g.envBin(e.version)
	if err != nil {
		return "", err
	}
	cmd := sysutil.CommandContext(ctx, bin, args...)
	cmd.Env = g.goVersionEnv(e.version)
	cmd.Dir = e.binDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	out := strings.TrimRight(buf.String(), "\r\n")
	if err != nil {
		return out, fmt.Errorf("执行 go %s 失败: %v", strings.Join(args, " "), err)
	}
	return out, nil
}

func taskGoInfo(ctx context.Context, e *taskExec) (string, error) {
	g, err := e.goRuntime()
	if err != nil {
		return "", err
	}
	keys := []string{"GOVERSION", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOBIN", "GOENV", "GOPROXY", "GOSUMDB", "GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED"}
	var b strings.Builder
	for _, k := range keys {
		v, e2 := e.goRun(ctx, g, "env", k)
		if e2 != nil {
			v = "—（" + e2.Error() + "）"
		}
		if strings.TrimSpace(v) == "" {
			v = "（默认）"
		}
		kv(&b, k, v)
	}
	return b.String(), nil
}

func taskGoBinList(ctx context.Context, e *taskExec) (string, error) {
	g, err := e.goRuntime()
	if err != nil {
		return "", err
	}
	dir, _ := e.goRun(ctx, g, "env", "GOBIN")
	if strings.TrimSpace(dir) == "" {
		gopath, e2 := e.goRun(ctx, g, "env", "GOPATH")
		if e2 != nil {
			return "", e2
		}
		dir = filepath.Join(strings.TrimSpace(gopath), "bin")
	}
	dir = strings.TrimSpace(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败（该目录可能尚未创建，先 go install 一个工具即可）: %v", dir, err)
	}
	var rows []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(name), ".exe") {
			continue
		}
		info, _ := ent.Info()
		size := int64(0)
		mod := ""
		if info != nil {
			size = info.Size()
			mod = info.ModTime().Format("2006-01-02 15:04")
		}
		rows = append(rows, fmt.Sprintf("%-28s %8.1f MB  %s", name, float64(size)/(1<<20), mod))
	}
	sort.Strings(rows)
	if len(rows) == 0 {
		return fmt.Sprintf("%s\n\n该目录下还没有 go install 安装的工具。", dir), nil
	}
	return dir + "\n\n" + strings.Join(rows, "\n"), nil
}

func taskGoCleanBuildCache(ctx context.Context, e *taskExec) (string, error) {
	g, err := e.goRuntime()
	if err != nil {
		return "", err
	}
	out, err := e.goRun(ctx, g, "clean", "-cache")
	if err != nil {
		return out, err
	}
	return strings.TrimSpace(out + "\n构建缓存（GOCACHE）已清空。"), nil
}

func taskGoCleanModCache(ctx context.Context, e *taskExec) (string, error) {
	g, err := e.goRuntime()
	if err != nil {
		return "", err
	}
	before, _ := e.goRun(ctx, g, "env", "GOMODCACHE")
	out, err := e.goRun(ctx, g, "clean", "-modcache")
	if err != nil {
		return out, err
	}
	return strings.TrimSpace(out + "\n模块缓存已清空：" + before + "\n下次 go build 会重新下载依赖。"), nil
}

// ---- PHP ----

func taskPHPInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	kv(&b, "PHP", firstLine(e.try(ctx, "php", "-v")))
	kv(&b, "ini 文件", phpIniPath(ctx, e))
	kv(&b, "扩展目录", e.try(ctx, "php", "-r", "echo ini_get('extension_dir');"))
	kv(&b, "扩展数", phpModuleCount(ctx, e))
	kv(&b, "版本目录", e.binDir)
	return b.String(), nil
}

func phpIniPath(ctx context.Context, e *taskExec) string {
	out, err := e.run(ctx, "php", "--ini")
	if err != nil {
		return "—（" + err.Error() + "）"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Loaded Configuration File") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return "—"
}

func phpModuleCount(ctx context.Context, e *taskExec) string {
	out, err := e.run(ctx, "php", "-m")
	if err != nil {
		return "—"
	}
	n := 0
	// php -m 前后各有 [PHP Modules] / [Zend Modules] 标题与空行，只数真正的模块行。
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "[") {
			continue
		}
		n++
	}
	return fmt.Sprintf("%d 个（详见「查看已加载扩展」）", n)
}

func taskPHPModules(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "php", "-m")
	return out, err
}

// ---- Composer ----

// phpExe 定位可用的 php：当前运行时自己的（composer 不会）→ QuickDock 托管的最高版本 → PATH。
func (e *taskExec) phpExe() (string, error) {
	if e.rt == RuntimePHP {
		if p := firstExe(e.binDir, "php"); p != "" {
			return p, nil
		}
	}
	base := filepath.Join(platform.DefaultDataDir(), "runtime/php")
	if entries, err := os.ReadDir(base); err == nil {
		var vers []string
		for _, ent := range entries {
			if ent.IsDir() && fileExists(firstExe(filepath.Join(base, ent.Name()), "php")) {
				vers = append(vers, ent.Name())
			}
		}
		sort.Slice(vers, func(i, j int) bool { return semverLess(vers[j], vers[i]) })
		if len(vers) > 0 {
			return firstExe(filepath.Join(base, vers[0]), "php"), nil
		}
	}
	if p, err := exec.LookPath("php"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("未找到 php。Composer 需要 PHP 运行：请先在「环境管理 → PHP」安装一个版本")
}

// composerRun 执行 composer.phar 子命令。
func (e *taskExec) composerRun(ctx context.Context, args ...string) (string, error) {
	php, err := e.phpExe()
	if err != nil {
		return "", err
	}
	phar := filepath.Join(e.binDir, "composer.phar")
	if !fileExists(phar) {
		return "", fmt.Errorf("未找到 %s", phar)
	}
	return e.runBin(ctx, php, append([]string{phar}, args...)...)
}

func taskComposerInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	kv(&b, "Composer", e.composerTry(ctx, "--version"))
	kv(&b, "PHP", e.phpVersionLine(ctx))
	kv(&b, "COMPOSER_HOME", e.composerTry(ctx, "config", "--global", "home"))
	kv(&b, "缓存目录", e.composerTry(ctx, "config", "--global", "cache-dir"))
	kv(&b, "phar", filepath.Join(e.binDir, "composer.phar"))
	return b.String(), nil
}

// phpVersionLine 显示 composer 实际会用到的那份 PHP 的版本（而非 PATH 上碰巧有的）。
func (e *taskExec) phpVersionLine(ctx context.Context) string {
	p, err := e.phpExe()
	if err != nil {
		return "—（" + err.Error() + "）"
	}
	out, _ := e.runBin(ctx, p, "-v")
	if strings.TrimSpace(out) == "" {
		return "—"
	}
	return firstLine(out)
}

// composerTry 信息类子命令的容错版。
func (e *taskExec) composerTry(ctx context.Context, args ...string) string {
	out, err := e.composerRun(ctx, args...)
	if err != nil {
		if strings.TrimSpace(out) != "" {
			return firstLine(out)
		}
		return "—（" + err.Error() + "）"
	}
	if strings.TrimSpace(out) == "" {
		return "—"
	}
	return strings.TrimSpace(out)
}

func taskComposerGlobalList(ctx context.Context, e *taskExec) (string, error) {
	return e.composerRun(ctx, "global", "show")
}

func taskComposerClearCache(ctx context.Context, e *taskExec) (string, error) {
	return e.composerRun(ctx, "clear-cache")
}

// ---- JDK ----

func taskJDKInfo(ctx context.Context, e *taskExec) (string, error) {
	var b strings.Builder
	kv(&b, "java", firstLine(e.try(ctx, "java", "-version")))
	kv(&b, "javac", firstLine(e.try(ctx, "javac", "-version")))
	kv(&b, "JAVA_HOME", nonEmpty(os.Getenv("JAVA_HOME"), "—（未设置，Maven / Gradle 依赖它定位 JDK）"))
	kv(&b, "JDK 目录", filepath.Dir(e.binDir))
	kv(&b, "模块数", jdkModuleCount(ctx, e))
	return b.String(), nil
}

func jdkModuleCount(ctx context.Context, e *taskExec) string {
	out, err := e.run(ctx, "java", "--list-modules")
	if err != nil {
		return "—"
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return fmt.Sprintf("%d 个内置模块（java --list-modules 查看）", n)
}

// ---- Nginx ----

// taskNginxConfigTest 校验配置语法（nginx -t）。
// 在版本目录里执行，nginx 才会以它为 prefix 找到 conf/nginx.conf——
// 这也是命令行里最容易踩的坑：在别的目录执行 nginx -t 会报找不到配置文件。
func taskNginxConfigTest(ctx context.Context, e *taskExec) (string, error) {
	out, err := e.run(ctx, "nginx", "-t")
	if err != nil {
		return out, fmt.Errorf("配置校验未通过（详见上方输出）")
	}
	return strings.TrimSpace(out + "\n\n配置语法正常。改动配置后记得重启或 reload 使其生效。"), nil
}

// taskNginxVersionDetail 输出 nginx -V 的编译参数，排查模块是否编译进去时用。
func taskNginxVersionDetail(ctx context.Context, e *taskExec) (string, error) {
	return e.run(ctx, "nginx", "-V")
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
