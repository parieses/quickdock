package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const ollamaBaseRel = "runtime/ollama"

// ollamaDefaultPort Ollama HTTP API 默认端口。serve 独占该端口，无法同时跑两个实例。
const ollamaDefaultPort = 11434

// ollamaEnvFileName 每版本的启动环境变量文件（KEY=VALUE 每行一条，井号开头为注释）。
// Ollama 没有配置文件，全部可调项（模型目录/监听地址/跨域/并发/显存驻留…）都靠环境变量传入，
// 故用该文本文件承载并实现 ConfigProvider，从而复用通用配置读写与前端已有的配置弹窗
// （同 FTPRuntime 用 ftpdmin.args 承载启动参数的做法）。
const ollamaEnvFileName = "ollama.env"

// reOllamaVersionOutput `ollama --version` 输出形如 "ollama version is 0.34.0"。
var reOllamaVersionOutput = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// ollamaEnvTemplate ollama.env 首次创建的模板。全部条目默认注释掉——注释行不含 '='，
// 会被 loadEnvVars 与 mergeEnv 一并跳过，从而保持 Ollama 自身的默认行为（尤其是 OLLAMA_MODELS
// 的默认值），避免出现「配了空值反而改变行为」。
const ollamaEnvTemplate = `# Ollama 启动环境变量（每行 KEY=VALUE，井号开头为注释）。修改后需重启服务生效。
#
# 模型目录。不配置则用 Ollama 默认（Windows: %USERPROFILE%\.ollama\models），
# 与系统安装版、命令行 ollama 共享同一份模型，无需重复下载。
# OLLAMA_MODELS=
#
# 监听地址。改端口时改这里（例如 127.0.0.1:11500），状态列会同步显示。
# OLLAMA_HOST=127.0.0.1:11434
#
# 允许跨域的来源，多个用逗号分隔（浏览器插件/网页直连 Ollama 时需要）。
# OLLAMA_ORIGINS=
#
# 模型显存驻留时长（默认 5m，-1 表示常驻不卸载）。
# OLLAMA_KEEP_ALIVE=
#
# 并发请求数（显存不足时调小）。
# OLLAMA_NUM_PARALLEL=
`

// OllamaRuntime 管理便携 Ollama 运行时（官方 Windows zip：顶层为 ollama.exe + lib/ollama/*.dll，
// 解压后约 1.4 GB，其中 1.36 GB 是 CUDA v12/v13 双轨 dll）。
//
// 多版本语义同 redis/nginx：允许并存安装，但 11434 独占，同一时刻只能有一个 serve 实例在跑；
// Status 按「运行进程所属版本目录」精确归属，避免多版本互相串状态（多版本全亮）。
type OllamaRuntime struct {
	baseDir string
}

func NewOllamaRuntime() *OllamaRuntime {
	return &OllamaRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), ollamaBaseRel)}
}

func (o *OllamaRuntime) Kind() Runtime        { return RuntimeOllama }
func (o *OllamaRuntime) DisplayName() string  { return DisplayName(RuntimeOllama) }
func (o *OllamaRuntime) DetectArgs() []string { return []string{"--version"} }

func (o *OllamaRuntime) ParseVersion(out string) (string, error) {
	if v := parseOllamaVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeOllama))
}

// SupportedPlatforms 一期仅 Windows。macOS 资产 Ollama-darwin.zip 解出来是 .app bundle
// （exe 在 Ollama.app/Contents/MacOS/），Linux 资产 ollama-linux-amd64.tar.zst 需要 zstd 解压
// 而 extract.go 只支持 zip/tar.gz——两者的路径与解压链路都得单独适配，不在一期范围内。
func (o *OllamaRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (o *OllamaRuntime) Recommended() []string        { return Versions(RuntimeOllama) }

// versionDir 单版本语义：忽略 version，目录恒为 runtime/ollama。
// Ollama 无可按项目切版本的使用场景，且 1.4 GB/版本、11434 端口独占、模型库全局共享，
// 多版本并存是纯成本零收益。程序升级走「原地替换」（见 Install），旧版本不做保留。
func (o *OllamaRuntime) versionDir(version string) string {
	return o.baseDir
}

// legacyVersionDir 多版本时代的目录形态 runtime/ollama/<version>，仅用于迁移。
func (o *OllamaRuntime) legacyVersionDir(version string) string {
	return filepath.Join(o.baseDir, version)
}

// migrateLegacyLayout 把旧的 runtime/ollama/<version>/ 内容上提到 runtime/ollama/，并清掉空目录。
// 只在目标目录尚无 ollama.exe 时执行（幂等）；失败不阻断调用方，交由后续流程按「未安装」处理。
// 注：只搬 ollama.exe / lib / olama.env / 日志等文件，不碰任何模型目录（模型本就在 ~/.ollama）。
func (o *OllamaRuntime) migrateLegacyLayout() {
	if _, err := os.Stat(o.ExeFor("")); err == nil {
		return // 已是单版本布局
	}
	entries, err := os.ReadDir(o.baseDir)
	if err != nil {
		return
	}
	// 从新到旧遍历，取版本号最大的那个作为迁移源（最可能包含最新程序）。
	best := ""
	for _, e := range entries {
		if !e.IsDir() || !reOllamaVersion.MatchString(e.Name()) {
			continue
		}
		if best == "" || semverLess(best, e.Name()) {
			best = e.Name()
		}
	}
	if best == "" {
		return
	}
	src := o.legacyVersionDir(best)
	names, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, n := range names {
		_ = os.Rename(filepath.Join(src, n.Name()), filepath.Join(o.baseDir, n.Name()))
	}
	// 目录空才删；残留内容说明有文件搬运失败，留着不删避免误伤。
	if rest, err := os.ReadDir(src); err == nil && len(rest) == 0 {
		_ = os.Remove(src)
	}
}

// DetectInstalledVersion 返回当前实际安装的版本号（探测 ollama.exe --version）。
// 单版本语义下它就是「已安装的那个版本」，供前端判断是否需要更新。
func (o *OllamaRuntime) DetectInstalledVersion() string {
	exe := o.ExeFor("")
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	return parseOllamaVersion(RunVersion(exe, "--version"))
}

func (o *OllamaRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(o.baseDir, "ollama.exe")
	}
	return filepath.Join(o.baseDir, "ollama")
}

// LogPath 运行日志路径。由宿主重定向写入，不读 %LOCALAPPDATA%\Ollama\server.log
// （那条路径由官方安装器与 app 管控，与便携版行为不一致）。
func (o *OllamaRuntime) LogPath(version string) string {
	return filepath.Join(o.versionDir(version), "ollama.log")
}

// envPath 该版本的启动环境变量文件路径。
func (o *OllamaRuntime) envPath(version string) string {
	return filepath.Join(o.versionDir(version), ollamaEnvFileName)
}

// systemExeCandidates 官方安装版的可执行文件候选路径：官方安装器装到
// %LOCALAPPDATA%\Programs\Ollama 并写入用户 PATH，故 LookPath 通常能命中；
// 若用户 PATH 被清理过，再按固定路径兜底检测一次。
func (o *OllamaRuntime) systemExeCandidates() []string {
	var out []string
	if p, err := exec.LookPath("ollama"); err == nil {
		out = append(out, p)
	}
	if runtime.GOOS == "windows" {
		if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
			p := filepath.Join(lad, "Programs", "Ollama", "ollama.exe")
			if len(out) == 0 || !strings.EqualFold(out[0], p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func (o *OllamaRuntime) InstalledVersions() []Install {
	o.migrateLegacyLayout()

	var out []Install
	dirs := managedDirs{}
	v := o.DetectInstalledVersion()
	if v == "" {
		// exe 缺失或 --version 探测失败时仍按「已安装」登记，避免用户看到空列表却删不掉残留目录。
		if _, err := os.Stat(o.ExeFor("")); err == nil {
			v = "unknown"
			if _, e := os.Stat(filepath.Join(o.baseDir, "VERSION")); e == nil {
				if b, e2 := os.ReadFile(filepath.Join(o.baseDir, "VERSION")); e2 == nil {
					if s := strings.TrimSpace(string(b)); s != "" {
						v = s
					}
				}
			}
		}
	}
	if v != "" {
		out = append(out, Install{Version: v, Scope: "portable", Path: o.baseDir})
		dirs.record(o.baseDir)
	}

	for _, p := range o.systemExeCandidates() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		// LookPath 命中本就由 QuickDock 托管（并已写进 PATH）的便携版时，不重复登记为 system。
		if dirs.dedupeByDir(p) {
			continue
		}
		v := parseOllamaVersion(RunVersion(p, "--version"))
		if v == "" {
			continue
		}
		out = append(out, Install{Version: v, Scope: "system", Path: p})
	}
	return out
}

// DeleteVersion 卸载 Ollama 程序文件（单版本语义：忽略 version，删整个 runtime/ollama）。
// ⚠️ 只删程序文件（约 1.4 GB/版本），绝不触碰模型目录（默认 ~/.ollama/models，动辄数十 GB，
// 且与系统安装版共享同一份）。
func (o *OllamaRuntime) DeleteVersion(version string) error {
	exe := o.ExeFor("")
	if _, err := os.Stat(o.baseDir); err != nil {
		return fmt.Errorf("未安装 Ollama")
	}
	// 若程序正在运行，Windows 上会因文件占用删不掉，提前给出明确提示。
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未找到 Ollama 可执行文件: %s", exe)
	}
	if err := os.RemoveAll(o.baseDir); err != nil {
		return fmt.Errorf("删除失败（可能服务正在运行，请先停止）: %w", err)
	}
	return nil
}

func (o *OllamaRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeOllama)[0]
	}
	o.migrateLegacyLayout()

	installed := o.DetectInstalledVersion()
	// 同版本已装：直接返回，避免无谓重下 1.4 GB。
	if installed == version && installed != "" {
		if cb.OnLog != nil {
			cb.OnLog("Ollama " + version + " 已是最新，无需安装")
		}
		return nil
	}
	isUpgrade := installed != "" && installed != "unknown"

	urls := CandidateURLs(RuntimeOllama, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Ollama 下载源（当前平台暂未支持）")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-ollama-"+version+".zip")
	const sizeHint = "（约 1.4 GB）"
	if cb.OnStage != nil {
		stage := "正在下载 Ollama " + version + sizeHint
		if isUpgrade {
			stage = "正在下载新版本 " + version + sizeHint + "，完成前保留当前 " + installed
		}
		cb.OnStage("download", stage+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Ollama " + version + sizeHint + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Ollama 失败: %w（当前版本未受影响）", err)
	}
	defer os.Remove(zipPath)

	// 先解压到同级的临时目录，全部成功后再替换——避免「旧版已删、新版没装成」。
	// 刻意放在 baseDir 同级（同一卷）以保证 os.Rename 不跨卷。
	staging := filepath.Join(filepath.Dir(o.baseDir), ".ollama-staging")
	// 上次失败可能留下残留，先清掉。
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(staging)

	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Ollama（约 1.4 GB）…")
	}
	// 归档顶层是 ollama.exe 与 lib/ 两个不同段，commonTopDir 判定为「非单一顶层目录」而不剥离，
	// 正是 PHP/redis 那种扁平归档形态，无需改动 extract.go。
	if err := Extract(zipPath, staging); err != nil {
		return fmt.Errorf("解压 Ollama 失败: %w（当前版本未受影响）", err)
	}
	if _, err := os.Stat(filepath.Join(staging, filepath.Base(o.ExeFor("")))); err != nil {
		return fmt.Errorf("解压完成但未找到 %s（当前版本未受影响）", o.ExeFor(""))
	}

	// ---- 校验通过，开始替换 ----
	// 升级时保留用户的 ollama.env（配置里可能改过 OLLAMA_MODELS/HOST，必须延续）。
	envBak := ""
	if isUpgrade {
		if b, err := os.ReadFile(o.envPath("")); err == nil {
			envBak = string(b)
		}
	}
	if cb.OnStage != nil {
		cb.OnStage("install", "正在替换程序文件…")
	}
	// 只清程序相关文件，不动目录本身（避免并发启动时路径消失）。
	if isUpgrade {
		for _, n := range []string{"ollama.exe", "ollama", "lib"} {
			_ = os.RemoveAll(filepath.Join(o.baseDir, n))
		}
	}
	if err := os.MkdirAll(o.baseDir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := moveDirContents(staging, o.baseDir); err != nil {
		return fmt.Errorf("替换程序文件失败: %w", err)
	}
	if envBak != "" {
		_ = os.WriteFile(o.envPath(""), []byte(envBak), 0o644)
	}
	_ = os.WriteFile(filepath.Join(o.baseDir, "VERSION"), []byte(version), 0o644)

	if cb.OnLog != nil {
		if isUpgrade {
			cb.OnLog("Ollama 已从 " + installed + " 更新到 " + version + "，配置已保留")
		} else {
			cb.OnLog("Ollama " + version + " 安装完成")
		}
	}
	return nil
}

// moveDirContents 把 src 下的条目逐个移动到 dst。
// staging 刻意建在 baseDir 同级（同一卷），故 os.Rename 不会跨卷失败，无需复制回退。
func moveDirContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ---- 配置（ConfigProvider）----

// ConfigPath 返回该版本的 ollama.env，实现通用 ConfigProvider（读写由通用层统一提供）。
// 首次访问时落一份全注释模板，避免前端配置弹窗「文件不存在」。
func (o *OllamaRuntime) ConfigPath(version string) string {
	p := o.envPath(version)
	if _, err := os.Stat(p); err != nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, []byte(ollamaEnvTemplate), 0o644)
		}
	}
	return p
}

// loadEnvVars 解析 ollama.env，返回 "KEY=VALUE" 覆盖项。
// 注释与空行已由 readConfLines 剔除；无 '=' 的行（如残留说明文字）在此再兜一次。
func (o *OllamaRuntime) loadEnvVars(version string) []string {
	var out []string
	for _, line := range readConfLines(o.envPath(version)) {
		if i := strings.IndexByte(line, '='); i > 0 {
			out = append(out, line)
		}
	}
	return out
}

// port 返回该版本实际监听的端口（解析 ollama.env 的 OLLAMA_HOST），未配置时回退默认 11434。
func (o *OllamaRuntime) port(version string) int {
	if ps := portsInConf(o.envPath(version), reOllamaHostPort); len(ps) > 0 {
		return ps[0]
	}
	return ollamaDefaultPort
}

// ConfiguredPorts 实现 ConfigPortsProvider：把 ollama.env 配置的端口回显到前端状态列。
func (o *OllamaRuntime) ConfiguredPorts(version string) []int {
	return []int{o.port(version)}
}

// ---- ServiceController ----

func (o *OllamaRuntime) DefaultPort() int { return ollamaDefaultPort }

func (o *OllamaRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	o.migrateLegacyLayout()

	// 单版本语义：目录固定，exe 只有一份。空 version 表示「启动已装的那个」。
	installs := o.InstalledVersions()
	if version == "" && len(installs) > 0 {
		version = installs[0].Version
	}
	var exe, wd string
	for _, ins := range installs {
		if version != "" && ins.Version != version {
			continue
		}
		if ins.Scope == "system" {
			exe, wd = ins.Path, filepath.Dir(ins.Path)
		} else {
			exe, wd = o.ExeFor(""), o.baseDir
		}
		break
	}
	if exe == "" {
		if _, err := os.Stat(o.ExeFor("")); err == nil {
			exe, wd = o.ExeFor(""), o.baseDir
		} else {
			return fmt.Errorf("请先安装 Ollama")
		}
	}

	port := o.port(version)
	// Ollama 独占端口：本会话已跑、或端口被外部实例（含官方安装版）占用时明确拒绝。
	if running, _ := svcMgr.info(RuntimeOllama); running != "" {
		return fmt.Errorf("Ollama 已在运行（%s），请先停止再启动", running)
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); processIsExeAny(pid, "ollama.exe", "ollama") {
			return fmt.Errorf("端口 %d 已被外部 Ollama 占用（pid=%d），请先从托盘退出或结束该进程", port, pid)
		}
		return fmt.Errorf("端口 %d 已被其它程序占用，请先释放该端口再启动 Ollama", port)
	}

	logger.I("[env][ollama] Start version=%s exe=%s wd=%s port=%d", version, exe, wd, port)

	// 未配置任何覆盖项时传 nil，保持与其它运行时一致的「继承宿主环境」行为。
	var env []string
	if overrides := o.loadEnvVars(version); len(overrides) > 0 {
		env = mergeEnv(os.Environ(), overrides)
		logger.I("[env][ollama] 应用 ollama.env 覆盖项 %d 条", len(overrides))
	}
	if onLog != nil {
		onLog("启动 Ollama " + version + " …")
	}
	// 一律用 `serve` 子命令：无参启动会拉起托盘 GUI，不是我们要的托管形态。
	if err := svcMgr.startWithEnv(RuntimeOllama, version, exe, wd, []string{"serve"}, o.LogPath(version), onLog, env); err != nil {
		if onLog != nil {
			onLog("Ollama 启动失败: " + err.Error())
		}
		return err
	}
	return nil
}

// LogGet 读取某版本运行日志尾部（实现 LogProvider）。
func (o *OllamaRuntime) LogGet(version string) (string, error) {
	return readLogTail(o.LogPath(version))
}

func (o *OllamaRuntime) Stop(version string) error {
	logger.I("[env][ollama] Stop version=%s", version)
	stopByPort(o.port(version), "ollama.exe")
	svcMgr.forget(RuntimeOllama)
	logger.I("[env][ollama] Stop 完成")
	return nil
}

func (o *OllamaRuntime) Status(version string) ServiceStatus {
	port := o.port(version)
	st := ServiceStatus{Running: false, Port: port}
	r := Probe(RuntimeRunningProbe{
		Kind:     RuntimeOllama,
		Port:     port,
		ExeNames: []string{"ollama.exe", "ollama"},
		Installs: o.InstalledVersions(),
	})
	if !r.Running {
		return st
	}
	// 单版本：跑着就是跑着，无需按版本归属（进程可能属于系统安装版，此时用查询版本回填）。
	st.PID = r.PID
	st.Running = true
	st.Version = r.Version
	if st.Version == "" {
		st.Version = version
	}
	return st
}

// parseOllamaVersion 从 `ollama --version` 输出（"ollama version is 0.34.0"）解析纯版本号。
func parseOllamaVersion(out string) string {
	if m := reOllamaVersionOutput.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// 保证实现对可选能力的承诺（接口未实现时编译期即报错，避免运行期类型断言静默失败）。
var (
	_ RuntimeAdapter      = (*OllamaRuntime)(nil)
	_ ServiceController   = (*OllamaRuntime)(nil)
	_ LogProvider         = (*OllamaRuntime)(nil)
	_ ConfigProvider      = (*OllamaRuntime)(nil)
	_ ConfigPortsProvider = (*OllamaRuntime)(nil)
)
