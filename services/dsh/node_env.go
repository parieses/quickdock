package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
	"quickdock/services/env"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	nodeVersion    = "v22.22.2" // LTS，DSH 要求 ^22.19.0 || >=24.0.0
	dshHomeRel     = "dsh"
	dshPkg         = "@deepseek-ai/dsh"
	// npm registry 元数据（与安装镜像一致，用于最新版本检测）
	dshRegistryJSON = "https://registry.npmmirror.com/@deepseek-ai/dsh"
)

// NodeEnvStatus 运行环境检测结果（返回前端）
type NodeEnvStatus struct {
	NodeFound          bool   `json:"nodeFound"`
	NpxFound           bool   `json:"npxFound"`
	NodeVersion        string `json:"nodeVersion"`
	NpxVersion         string `json:"npxVersion"`
	NodePath           string `json:"nodePath"`
	NodeSupport        bool   `json:"nodeSupport"` // node 版本是否满足 dsh 要求（^22.19.0 || >=24.0.0）
	DshInstalled       bool   `json:"dshInstalled"`
	DshPath            string `json:"dshPath"` // 检测到的 dsh 可执行入口（lib/bin.js）
	DshHome            string `json:"dshHome"` // DSH 数据目录
	DshVersion         string `json:"dshVersion"`
	LatestDshVersion   string `json:"latestDshVersion"`
	DshUpdateAvailable bool   `json:"dshUpdateAvailable"`
	Installing         bool   `json:"installing"`
	Message            string `json:"message"` // 未就绪时的可读原因
}

// SetupProgress 一键安装进度（经 quickdock:dsh:progress 事件推前端）
type SetupProgress struct {
	Stage   string `json:"stage"` // download-node | extract-node | install-dsh | update-dsh | done | error
	Written int64  `json:"written"`
	Total   int64  `json:"total"`
	Message string `json:"message"`
}

// setupLog 一键安装的实时日志行（经 quickdock:dsh:log 事件推前端，前端滚动面板展示）
type setupLog struct {
	Level   string `json:"level"` // info | error
	Message string `json:"message"`
}

// NodeEnvManager 检测并提供 Node 运行时（委托 services/env）；缺失时下载便携版并在其下安装 dsh。
// 不依赖系统 PATH、不写注册表、不请求管理员权限——纯用户态、随 QuickDock 清理。
type NodeEnvManager struct {
	app        *application.App
	dataDir    string
	node       *env.NodeRuntime
	installing atomic.Bool
}

func NewNodeEnvManager() *NodeEnvManager {
	dataDir := platform.DefaultDataDir()
	return &NodeEnvManager{
		dataDir: dataDir,
		node:    env.NewNodeRuntime(),
	}
}

// runtimeDir 当前实际使用的 node 所在目录（npm 全局 prefix 与 PATH 拼装基准）。
// Node 已改为多版本（runtime/node/<version>），必须每次动态解析，
// 不能在构造时缓存——否则用户切换/新增版本后 dsh 仍指向旧目录。
func (m *NodeEnvManager) runtimeDir() string { return m.node.RuntimeDir() }

func (m *NodeEnvManager) SetApp(app *application.App) { m.app = app }

// EmitLog 向前端 DSH 日志面板推送一行日志
func (m *NodeEnvManager) EmitLog(level, msg string) {
	if m.app != nil {
		m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
	}
}

func (m *NodeEnvManager) nodeExe() string { return m.node.Exe() }
func (m *NodeEnvManager) npxExe() string  { return m.node.NpxExe() }

// npmCli 返回与当前 node 同目录下的 npm-cli.js（node 自带 npm）。
// 关键：必须基于「实际检测到的 node 目录」推导，而非写死的便携目录——
// 当 node 来自 PATH（系统安装）时便携目录为空，写死路径会导致 MODULE_NOT_FOUND。
func (m *NodeEnvManager) npmCli() string {
	nodeExe := m.NodePath()
	if nodeExe == "" {
		nodeExe = m.nodeExe()
	}
	nodeDir := filepath.Dir(nodeExe)
	if runtime.GOOS == "windows" {
		return filepath.Join(nodeDir, "node_modules", "npm", "bin", "npm-cli.js")
	}
	return filepath.Join(nodeDir, "..", "lib", "node_modules", "npm", "bin", "npm-cli.js")
}

// installDir 旧版 dsh 自定义安装目录（npm -g --prefix 的位置）。
// 已改为默认安装（npm -g 落到 node 全局 prefix），此目录仅用于兼容历史残留与兜底报错。
func (m *NodeEnvManager) installDir() string {
	return filepath.Join(m.dataDir, dshHomeRel)
}

// DshHome DSH 数据目录（profile/会话/配置，决定 dsh 读写哪份数据）。
// 优先取用户环境变量 DSH_HOME，其次 DSH 官方默认 ~/.dsh——
// 这样 QuickDock 入口与用户手动 `npx @deepseek-ai/dsh web` 共用同一份
// profile（皮肤/插件/会话互通），避免程序入口因独立数据目录缺 bundle 而无法启动。
func (m *NodeEnvManager) DshHome() string {
	if env := strings.TrimSpace(os.Getenv("DSH_HOME")); env != "" {
		return env
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".dsh")
	}
	return m.installDir()
}

// DshBin 返回 dsh 可执行文件（用于安装检测/报错提示）。
// 按优先级探测：PATH 官方全局 → 便携 node 的 npm 默认全局（runtimeDir）→ 旧版自定义 installDir。
func (m *NodeEnvManager) DshBin() string {
	var dirs []string
	if p, err := exec.LookPath("dsh"); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	if m.runtimeDir() != "" {
		dirs = append(dirs, m.runtimeDir())
	}
	dirs = append(dirs, m.installDir())
	candidates := []string{}
	for _, dir := range dirs {
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				filepath.Join(dir, "dsh.cmd"),
				filepath.Join(dir, "node_modules", ".bin", "dsh.cmd"),
			)
		} else {
			candidates = append(candidates,
				filepath.Join(dir, "node_modules", ".bin", "dsh"),
			)
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// npmGlobalRoot 返回 npm 全局模块根目录（npm root -g），用于定位官方全局安装的 dsh。
// 只缓存成功结果：首次调用失败（npm 暂不可用）不缓存，下次 Detect 会重试，
// 避免 sync.Once 把空值永久缓存导致整个进程生命周期内漏掉 npm 全局安装的 dsh。
var npmGlobalRootMu sync.Mutex
var npmGlobalRootVal string
var npmGlobalRootCached bool

// dshMainJSCandidates 收集所有可能的 dsh JS 入口，按优先级排列：
// 1 官方全局（PATH dsh 命令所在目录）→ 2 npm 默认全局 bin（Roaming/npm）→ 3 npm root -g → 4 npx 临时缓存 → 5 QuickDock 自定义 installDir（兜底）
func (m *NodeEnvManager) dshMainJSCandidates() []string {
	var cands []string
	if p, err := exec.LookPath("dsh"); err == nil {
		cands = append(cands,
			filepath.Join(filepath.Dir(p), "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
	}
	if bd := npmDefaultBinDir(); bd != "" {
		cands = append(cands,
			filepath.Join(bd, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
	}
	if root := npmGlobalRoot(); root != "" {
		cands = append(cands, filepath.Join(root, "@deepseek-ai", "dsh", "lib", "bin.js"))
	}
	cands = append(cands, npxCacheDshBins()...)
	// 便携 node 的 npm 默认全局位置（npm -g 默认 prefix = node 所在目录 runtimeDir）
	if m.runtimeDir() != "" {
		cands = append(cands,
			filepath.Join(m.runtimeDir(), "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
	}
	// 旧版 QuickDock 自定义 --prefix 安装作为最低兜底（兼容历史残留）
	cands = append(cands,
		filepath.Join(m.installDir(), "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
	return cands
}

// DshMainJS 定位可用的 dsh JS 入口（lib/bin.js），供 node 直接拉起。
// 探测顺序见 dshMainJSCandidates：官方全局/默认 bin/npm 全局/npx 缓存优先，自定义 installDir 兜底。
func (m *NodeEnvManager) DshMainJS() string {
	for _, c := range m.dshMainJSCandidates() {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return filepath.Join(m.installDir(), "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
}

// dshInstalledAnywhere 判断 dsh 是否已安装（程序能找到可执行入口）。
// 官方安装（PATH/默认 bin/npm 全局/npx 缓存）或 QuickDock 自定义安装任一命中即已装。
// 注意：数据目录 ~/.dsh/profiles 存在不算已安装——它只说明数据/配置在，可执行二进制
// 可能随 npx 临时缓存清理或用户清理而消失；此时应由安装流程补装（复用原 DSH_HOME 数据）。
// 判定必须与 Start() 完全一致（都认 DshMainJS 的 bin.js 文件存在）：
// 不能再用 exec.LookPath("dsh") 兜底——PATH 上残留 dsh.cmd 但 node_modules 被删时，
// 探测会误判「已安装」，而 Start() 里 os.Stat(mainJS) 失败报「未安装」，前后矛盾。
// DshMainJS 的候选①已经覆盖了 PATH 上 dsh 命令所在目录，LookPath 兜底本身冗余。
func (m *NodeEnvManager) dshInstalledAnywhere() bool {
	mainJS := m.DshMainJS()
	if mainJS == "" {
		return false
	}
	_, err := os.Stat(mainJS)
	return err == nil
}

// NodePath 返回实际可用的 node：优先系统 PATH（版本满足要求），其次便携目录。
// 系统 node 版本过低时降级到便携版，避免把 dsh 装进一个跑不起来的旧环境。
func (m *NodeEnvManager) NodePath() string {
	p, _, _ := m.node.Detect()
	return p
}

// DshVersion 读取已安装 dsh 的版本号（定位到 bin.js 所在包的 package.json）
func (m *NodeEnvManager) DshVersion() string {
	for _, c := range m.dshMainJSCandidates() {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		pkg := filepath.Join(filepath.Dir(filepath.Dir(c)), "package.json")
		b, err := os.ReadFile(pkg)
		if err != nil {
			continue
		}
		var p struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &p) == nil && p.Version != "" {
			return p.Version
		}
	}
	return ""
}

// Detect 检测 node/npx/dsh 状态（不发网络请求，快速返回）
func (m *NodeEnvManager) Detect() NodeEnvStatus {
	st := NodeEnvStatus{Installing: m.installing.Load()}
	if p, v, ok := m.node.Detect(); ok {
		st.NodeFound = true
		st.NodePath = p
		st.NodeVersion = v
		st.NodeSupport = true
	} else if p, err := exec.LookPath("node"); err == nil {
		// 系统里有 node 但版本不满足——说明原因，安装会走内置 Node
		if v := env.RunVersion(p, "--version"); v != "" {
			st.NodeFound = true
			st.NodePath = p
			st.NodeVersion = v
			st.NodeSupport = false
			st.Message = "系统 Node 版本不满足要求（需 ^22.19.0 或 >=24.0.0），将使用内置 Node"
		}
	}
	if p, err := exec.LookPath("npx"); err == nil {
		st.NpxFound = true
		st.NpxVersion = env.RunVersion(p, "--version")
	} else if _, err := os.Stat(m.npxExe()); err == nil {
		st.NpxFound = true
	}
	if m.dshInstalledAnywhere() {
		st.DshInstalled = true
		st.DshPath = m.DshMainJS()
		st.DshVersion = m.DshVersion()
	}
	st.DshHome = m.DshHome()
	if !st.NodeFound {
		st.Message = "未检测到可用的 Node 运行时，将自动下载内置 Node"
	}
	if st.NodeFound && st.NodeSupport && !st.DshInstalled {
		st.Message = "Node 已就绪，DeepSeek Harness 未安装——点「一键安装」仅补装 dsh"
	}
	return st
}

// DetectWithUpdate 快速检测 + 联网查最新 dsh 版本（查失败静默，不阻塞界面）
func (m *NodeEnvManager) DetectWithUpdate() NodeEnvStatus {
	st := m.Detect()
	if st.DshInstalled {
		st.LatestDshVersion = m.LatestDshVersion()
		st.DshUpdateAvailable = st.LatestDshVersion != "" && st.DshVersion != "" && st.LatestDshVersion != st.DshVersion
	}
	return st
}

// LatestDshVersion 查询 npm registry 的 latest 版本；失败返回空串
func (m *NodeEnvManager) LatestDshVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dshRegistryJSON, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := (&http.Client{Transport: env.ProxyTransport()}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		DistTags map[string]string `json:"dist-tags"`
		Version  string            `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return ""
	}
	// 取 dist-tags 中各标签里语义化版本最大者：dsh 的预发布版本发在 next/rc 等非
	// latest 标签上（当前 latest=0.1.0-rc.7，next=0.1.0-rc.8），只读 latest 会漏掉最新 rc。
	// 排除 alpha/beta 标签：alpha 通道（0.1.2-alpha.x）改了 @deepseek-ai/dsh-settings
	// 的导出与 ctx.subagents 等内部 API，社区插件（dshmarket/@linxin666 全家桶等）未跟进，
	// 装 alpha 会导致 dsh web profile 插件加载全崩、启动失败（2026-09-03 实测）。
	best := ""
	for tag, v := range info.DistTags {
		switch tag {
		case "alpha", "beta":
			continue
		}
		if v != "" && compareSemver(best, v) < 0 {
			best = v
		}
	}
	if best == "" {
		best = info.Version
	}
	return best
}

// compareSemver 比较两个 npm 版本号（major.minor.patch[-pre]），返回 -1/0/1。
// UpdateDSH 将 dsh 更新到最新版（stage: update-dsh）。与安装共用 installing 锁防并发。
func (m *NodeEnvManager) UpdateDSH(ctx context.Context) error {
	emit := func(sp SetupProgress) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:progress", sp)
		}
	}
	logf := func(level, msg string) { m.EmitLog(level, msg) }

	if m.installing.Swap(true) {
		// 锁被占用也补发 error 事件，前端 installingPlugin/settingUp/updating 才能复位
		emit(SetupProgress{Stage: "error", Message: "安装/更新正在进行中，请稍候"})
		return fmt.Errorf("安装/更新正在进行中")
	}
	defer m.installing.Store(false)

	if m.NodePath() == "" {
		emit(SetupProgress{Stage: "error", Message: "运行环境未就绪，无法更新"})
		return fmt.Errorf("运行环境未就绪，无法更新")
	}
	emit(SetupProgress{Stage: "update-dsh", Message: "正在更新 DeepSeek Harness…"})
	target := m.LatestDshVersion()
	if target == "" {
		target = "latest"
	}
	logf("info", "更新 "+dshPkg+"@"+target+"（registry: npmmirror）")
	if err := m.runNpmInstall(ctx, target, func(msg string) {
		emit(SetupProgress{Stage: "update-dsh", Message: msg})
		logf("info", msg)
	}); err != nil {
		emit(SetupProgress{Stage: "error", Message: "更新 dsh 失败: " + err.Error()})
		logf("error", "更新 dsh 失败: "+err.Error())
		return err
	}
	logf("info", "DeepSeek Harness 更新完成（版本 "+m.DshVersion()+"）")
	emit(SetupProgress{Stage: "done", Message: "更新完成"})
	return nil
}

// SetupDSH 一键安装：node 缺失则下载便携版（经 env 可切换源），dsh 缺失则 npm 安装；分阶段回调进度。
// 进度同时经 a.app.Event.Emit("quickdock:dsh:progress", ...) 推前端，故 onProgress 可为 nil。
func (m *NodeEnvManager) SetupDSH(ctx context.Context, onProgress func(SetupProgress)) error {
	emit := func(sp SetupProgress) {
		if onProgress != nil {
			onProgress(sp)
		}
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:progress", sp)
		}
	}
	logf := func(level, msg string) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
		}
	}

	if m.installing.Swap(true) {
		// 锁被占用（并发安装/更新）也补发 error 事件，前端 settingUp/updating 才能复位，
		// 否则按钮会永久禁用（前端只能靠 done/error 事件复位）
		emit(SetupProgress{Stage: "error", Message: "安装正在进行中，请稍候"})
		return fmt.Errorf("安装正在进行中")
	}
	defer m.installing.Store(false)

	logf("info", "开始准备运行环境（缺失 Node 将下载，缺失 dsh 将安装）")

	// 1. 确保 node（委托 env 包，使用可切换下载源）
	if m.NodePath() == "" {
		emit(SetupProgress{Stage: "download-node", Message: "正在下载 Node 运行时…"})
		if err := m.node.Install(ctx, nodeVersion, env.InstallCallback{
			OnProgress: func(w, t int64) {
				emit(SetupProgress{Stage: "download-node", Written: w, Total: t, Message: "正在下载 Node 运行时…"})
			},
			OnLog: func(msg string) { logf("info", msg) },
			OnStage: func(stage, msg string) {
				if stage == "download" {
					emit(SetupProgress{Stage: "download-node", Message: msg})
				} else if stage == "extract" {
					emit(SetupProgress{Stage: "extract-node", Message: msg})
				}
			},
		}); err != nil {
			emit(SetupProgress{Stage: "error", Message: err.Error()})
			logf("error", err.Error())
			return err
		}
	} else {
		logf("info", "Node 已就绪: "+m.NodePath())
	}

	// 2. 确保 dsh（任意来源已安装即跳过：便携安装 / 全局 npm / ~/.dsh 手动初始化过）
	if !m.dshInstalledAnywhere() {
		emit(SetupProgress{Stage: "install-dsh", Message: "正在安装 DeepSeek Harness…"})
		logf("info", "安装 "+dshPkg+"（registry: npmmirror）")
		if err := m.installDSH(ctx, func(msg string) {
			emit(SetupProgress{Stage: "install-dsh", Message: msg})
			logf("info", msg)
		}); err != nil {
			emit(SetupProgress{Stage: "error", Message: "安装 dsh 失败: " + err.Error()})
			logf("error", "安装 dsh 失败: "+err.Error())
			return err
		}
		logf("info", "DeepSeek Harness 安装完成")
	} else {
		logf("info", "DeepSeek Harness 已安装（跳过安装）")
	}

	emit(SetupProgress{Stage: "done", Message: "运行环境已就绪"})
	logf("info", "运行环境已就绪，可打开 DeepSeek Harness")
	return nil
}

// installDSH 安装最新发布的 dsh（等价 npm install -g @deepseek-ai/dsh --prefix <installDir>）。
// 目标版本取 LatestDshVersion（含 next/rc 等非 latest 标签），装到当前实际最新，而非 latest 标签。
func (m *NodeEnvManager) installDSH(ctx context.Context, onMsg func(string)) error {
	return m.runNpmInstall(ctx, m.LatestDshVersion(), onMsg)
}

// runNpmInstall 用当前 node 的 npm 将 dsh 安装到 installDir()（--prefix），国内走 npmmirror 镜像。
// target 非空时显式安装 @<target>（更新/安装到指定版本）；为空时装默认 latest（npm 解析 latest 标签）。
// DSH_HOME 指向数据目录（DshHome），让 dsh 的 postinstall 在数据目录初始化 profile。
func (m *NodeEnvManager) runNpmInstall(ctx context.Context, target string, onMsg func(string)) error {
	node := m.NodePath()
	if node == "" {
		return fmt.Errorf("node 未就绪")
	}
	// 确保 DSH_HOME 数据目录存在（全新机器 ~/.dsh 不存在时，npm postinstall/后续启动
	// 会因工作目录或写入目录无效而失败）
	if err := os.MkdirAll(m.DshHome(), 0755); err != nil {
		return err
	}
	npmCli := m.npmCli()
	if onMsg != nil {
		onMsg("使用 npm: " + npmCli)
	}
	pkgArg := dshPkg
	if target != "" && target != "latest" {
		pkgArg = dshPkg + "@" + target
	}
	// 不指定 --prefix：npm install -g 装到 npm 默认全局位置（node 所在环境内），
	// 符合用户「装到默认位置、不装软件目录」的要求。便携 node 的默认全局即 runtimeDir。
	args := []string{npmCli, "install", "-g", pkgArg}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Dir = m.DshHome()
	env := cleanNodeEnv(os.Environ())
	env = append(env,
		"DSH_HOME="+m.DshHome(),
		"npm_config_registry=https://registry.npmmirror.com/",
	)
	// 便携 node 目录前置到 PATH：npm 的 postinstall 脚本（如 koffi 的 cnoke.cjs）
	// 会经 `cmd /c node ...` 调 node，PATH 里没有 node 时直接失败（新机器无系统 node）。
	// 便携目录不存在（node 来自系统 PATH）时跳过，避免 PATH 出现空项。
	if nodeDir := m.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	// sysutil.Hide：Windows 用 CREATE_NO_WINDOW（0x08000000）让 npm/cmd 子进程继承隐藏控制台；
	// 非 Windows 返回 nil。切勿用 DETACHED_PROCESS(0x00000008)——孙进程会各自弹窗。
	sysutil.Hide(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	consume := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if onMsg != nil {
				onMsg(sc.Text())
			}
		}
	}
	go consume(stdout)
	go consume(stderr)
	if err := cmd.Wait(); err != nil {
		return err
	}
	// 安装完成后校验入口确实存在（npm -g 默认落点可能因平台/前缀不同）
	if _, err := os.Stat(m.DshMainJS()); err != nil {
		return fmt.Errorf("dsh 安装完成但找不到入口，期望路径: %s", m.DshMainJS())
	}
	// 安装 pnpm：dsh plugin add 内部用 pnpm 管理插件依赖，新电脑无 pnpm 会失败
	if err := m.installPnpm(ctx, node, npmCli, env, onMsg); err != nil {
		if onMsg != nil {
			onMsg(fmt.Sprintf("[warn] pnpm 安装失败（不影响 dsh 本体，但插件安装可能需要）: %v", err))
		}
		// 不 return err：pnpm 失败不应阻断 dsh 安装流程
	}
	return nil
}

// installPnpm 全局安装 pnpm（dsh plugin add 依赖它）
func (m *NodeEnvManager) installPnpm(ctx context.Context, node, npmCli string, env []string, onMsg func(string)) error {
	if onMsg != nil {
		onMsg("正在安装 pnpm（dsh 插件管理依赖）…")
	}
	args := []string{npmCli, "install", "-g", "pnpm"}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Dir = m.DshHome()
	cmd.Env = env
	sysutil.Hide(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	consume := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if onMsg != nil {
				onMsg(sc.Text())
			}
		}
	}
	go consume(stdout)
	go consume(stderr)
	return cmd.Wait()
}
