package env

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

// Everything 状态机四态 / 探测逻辑见下方常量与 EverythingRuntime 实现。
// RuntimeEverything 常量本身定义在 source.go（与其它 Runtime 常量同处），
// 这里只承载 Everything 专属的状态常量与引擎实现。

// Everything 状态机四态：未安装 / 缺少 es.exe / 已停止 / 运行中。
// 语义优先级：先看 Everything.exe 是否存在（决定 not_installed / 其余三态），
// 存在时再看 es.exe 是否在（决定 missing_es / 其余两态），最后看进程是否在跑（stopped / running）。
const (
	EverythingStateNotInstalled = "not_installed"
	EverythingStateMissingES    = "missing_es"
	EverythingStateStopped      = "stopped"
	EverythingStateRunning      = "running"
)

// everythingExeCandidates 除固定程序目录外，Everything 安装器还可能在 Program Files (x86) 下放 32 位，
// 以及 64 位在 1.4 时代曾用 Everything64.exe 命名。这里把常见可执行名一并纳入扫描。
var everythingExeNames = []string{"Everything.exe", "Everything64.exe"}

// esExeName es.exe（命令行工具）独立发行，文件名固定。
const esExeName = "es.exe"

// ---- 程序托管下载（便捷版/便携版，按系统架构自动选 x64 / x86）----
// Everything 与 es.exe 均从 voidtools 官方下载；便携 zip 免安装、解压即用，
// 落盘到 QuickDock 托管的 runtime/everything 目录（baseDir），不写注册表、不申请管理员。
// everythingStableVer / esStableVer 即 QuickDock 认定的「最新稳定版」目标版本：
// 全新安装拉它；已安装且版本更旧时，Install 自动覆盖为它（一键更新）；它即「最新」。
// 官方发布新稳定版时，只改这两个常量即可。
const (
	everythingDownloadHost = "https://www.voidtools.com/"
	everythingStableVer    = "1.4.1.1032"
	esStableVer            = "1.1.0.38"
)

// 卡死实例自愈（Search 拿到 exit 1 = IPC 回复异常时触发）的节奏参数：
//   - 冷却：自愈后 60s 内不再自愈，避免索引未就绪时反复杀进程；
//   - 预热：自愈后最多等 15s 索引出现内容（冷索引要把所有 NTFS 卷扫一遍，可能几十秒），
//     超时也照常重试——索引在后台继续建，后续检索即可命中；
//   - 轮询：每 700ms 用 es -get-result-count 探一次索引条数。
const (
	everythingRecoverCooldown   = 60 * time.Second
	everythingIndexWarmup       = 15 * time.Second
	everythingIndexPollInterval = 700 * time.Millisecond
	// everythingStopWait / everythingStopPollInterval：Stop 发出结束指令后等待进程
	// 真正退出的上限与轮询间隔。旧实例不退干净（单实例互斥未释放）就拉起新实例，
	// 新实例会把自己误判成"第二个实例"而秒退——自愈反而把检索搞废。
	everythingStopWait         = 3 * time.Second
	everythingStopPollInterval = 100 * time.Millisecond
)

// everythingArch 根据编译目标架构决定下载 x64 还是 x86 便携包。
// QuickDock 本身按架构分发，本地编译架构即系统原生架构（arm64 走 x64 模拟兼容）。
func everythingArch() string {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return "x64"
	default:
		return "x86"
	}
}

// installDirCandidates 系统默认安装目录（含用户自定义项，自定义项由 settings 注入）。
func everythingDefaultDirs() []string {
	dirs := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Everything"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Everything"),
	}
	// 兜底：ProgramFiles 在某些环境下为空（如 32 位进程读到的是特殊名），再用字面量兜底。
	if pf := os.Getenv("ProgramFiles"); pf == "" {
		dirs = append(dirs, `C:\Program Files\Everything`)
	}
	return dirs
}

// EverythingRuntime 管理外部安装的 Everything + es.exe：探测、启停、检索。
// 同时实现 RuntimeAdapter（让环境管理页把它当普通运行时列出、刷新、走通用服务启停）
// 与 ServiceController（控制 Everything.exe 进程）。
type EverythingRuntime struct {
	// 仅用于满足 RuntimeAdapter 的「便携目录」契约；Everything 是外部安装，dir 不参与实际探测。
	baseDir string

	// searchDiag 最近一次文件检索的诊断码（见 EverythingDiag* 常量；空=正常）。
	// Ctrl+K 与 MCP 可能并发检索，读写都过 searchDiagMu。
	searchDiag   string
	searchDiagMu sync.Mutex

	// lastRecoverAt 最近一次「卡死实例自愈」的时间。自愈后索引尚未就绪时，紧接着的检索
	// 仍会拿到 exit 1；没有冷却就会每次都杀一遍进程，把「等索引」恶化成「反复重启」。
	recoverMu     sync.Mutex
	lastRecoverAt time.Time
}

func NewEverythingRuntime() *EverythingRuntime {
	return &EverythingRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), "runtime", "everything")}
}

// ---- RuntimeAdapter 契约 ----

func (e *EverythingRuntime) Kind() Runtime        { return RuntimeEverything }
func (e *EverythingRuntime) DisplayName() string  { return DisplayName(RuntimeEverything) }
func (e *EverythingRuntime) DetectArgs() []string { return nil } // 不支持从外部导入（无稳定 --version 语义）
func (e *EverythingRuntime) ParseVersion(string) (string, error) {
	return "", fmt.Errorf("%s 为外部工具，不支持版本探测导入", DisplayName(RuntimeEverything))
}
func (e *EverythingRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (e *EverythingRuntime) Recommended() []string        { return nil }
func (e *EverythingRuntime) ExeFor(string) string         { return e.detectEverythingExe() }

// InstalledVersions 探测外部安装的 Everything：找到 Everything.exe 即登记为 system 级已装版本。
// 版本号读自 PE 文件版本资源（readExeVersion，Windows 实现；其它平台返回空）。
// 空返回值表示本机未安装 Everything，环境管理页据此显示「未安装 + 下载安装」。
func (e *EverythingRuntime) InstalledVersions() []Install {
	exe := e.detectEverythingExe()
	if exe == "" {
		return nil
	}
	ver := readExeVersion(exe)
	if ver == "" {
		// 拿不到版本资源也不影响「已安装」判定，标记 unknown 即可。
		ver = "unknown"
	}
	return []Install{{
		Version: ver,
		Scope:   "system",
		Path:    filepath.Dir(exe),
		Active:  true,
	}}
}

// Install 程序托管下载 / 更新 Everything 便携版（便捷版）：按系统架构选 x64/x86，
// 下载 zip 到临时目录后用通用 Extract 解压到 baseDir（托管运行时目录），不写注册表、不申请管理员。
//
// 三态语义（满足「没装→一键装最新；已装→检测更新可更新」）：
//   - 未安装（检测不到 exe）→ 直接下载解压到 baseDir（一键安装最新版）；
//   - 已安装且落在托管目录（managed）→ 若版本比 everythingStableVer 旧则先停进程再覆盖解压（一键更新），
//     否则幂等跳过（已是最新）；
//   - 已安装但落在系统目录（非 managed，无管理员权限无法就地覆盖）→ 返回明确引导，交由用户去官网手动更新。
func (e *EverythingRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	exe := e.detectEverythingExe()
	if exe == "" {
		// 全新安装：下载便携版到托管目录。
		return e.downloadEverything(ctx, cb)
	}
	if !e.isManaged(exe) {
		// 系统目录安装的 Everything，QuickDock 无管理员权限无法就地覆盖，交由用户手动更新。
		msg := fmt.Sprintf("Everything 安装在系统目录（%s），QuickDock 无法自动更新，请前往官网手动更新", filepath.Dir(exe))
		if cb.OnLog != nil {
			cb.OnLog(msg)
		}
		return fmt.Errorf("%s", msg)
	}
	if !e.UpdateAvailable() {
		if cb.OnLog != nil {
			cb.OnLog("Everything 已是最新版本，无需更新")
		}
		return nil
	}
	// 更新：先停掉运行中的实例（否则 exe 被占用无法覆盖），短暂等待句柄释放后再覆盖解压。
	if e.isRunning() {
		if cb.OnLog != nil {
			cb.OnLog("正在停止运行中的 Everything 以应用更新…")
		}
		_ = e.Stop("")
		time.Sleep(500 * time.Millisecond)
	}
	if cb.OnLog != nil {
		cb.OnLog(fmt.Sprintf("正在更新 Everything %s → %s…", e.installedVersion(), everythingStableVer))
	}
	return e.downloadEverything(ctx, cb)
}

// downloadEverything 真正执行「下载 + 解压」：从官方拉 Everything-<ver>.<arch>.zip 到临时目录，
// 再 Extract 到 baseDir。幂等由调用方（Install）保证：此处只管拉取与落盘。
func (e *EverythingRuntime) downloadEverything(ctx context.Context, cb InstallCallback) error {
	arch := everythingArch()
	url := fmt.Sprintf("%sEverything-%s.%s.zip", everythingDownloadHost, everythingStableVer, arch)
	zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("quickdock-everything-%s-%s.zip", everythingStableVer, arch))

	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Everything 便携版…")
	}
	if cb.OnLog != nil {
		cb.OnLog(fmt.Sprintf("正在下载 Everything %s (%s)…", everythingStableVer, arch))
	}
	if err := Download(ctx, zipPath, []string{url}, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Everything 失败: %w", err)
	}
	defer os.Remove(zipPath)

	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Everything…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压到 " + e.baseDir)
	}
	if err := os.MkdirAll(e.baseDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := Extract(zipPath, e.baseDir); err != nil {
		return fmt.Errorf("解压 Everything 失败: %w", err)
	}

	exe := e.detectEverythingExe()
	if exe == "" {
		exe = findExeRecursive(e.baseDir, everythingExeNames)
	}
	if exe == "" {
		return fmt.Errorf("解压完成但未找到 Everything.exe，请删除 %s 后重试", e.baseDir)
	}
	if cb.OnLog != nil {
		cb.OnLog("Everything 安装/更新完成: " + exe)
	}
	return nil
}

// isManaged 判断检测到的 Everything.exe 是否落在 QuickDock 托管目录（baseDir）内。
// 只有托管安装的才允许一键更新（就地覆盖）；系统目录（Program Files 等）无管理员权限，需手动更新。
// baseDir 过一遍 filepath.Clean：调用方传入的路径可能混用正斜杠（如用户配置/测试临时目录），
// 而 filepath.Dir 输出的是规范化的反斜杠路径，直接比较会因分隔符差异误判为「非托管」。
func (e *EverythingRuntime) isManaged(exe string) bool {
	dir := strings.ToLower(filepath.Dir(exe))
	base := strings.ToLower(filepath.Clean(e.baseDir))
	return dir == base || strings.HasPrefix(dir, base+string(os.PathSeparator))
}

// installedVersion 返回检测到的 Everything 版本（PE 资源）；未安装或读不到返回空。
func (e *EverythingRuntime) installedVersion() string {
	exe := e.detectEverythingExe()
	if exe == "" {
		return ""
	}
	return readExeVersion(exe)
}

// UpdateAvailable 是否已检测到新版本：已安装且版本号严格小于 everythingStableVer 时为真。
// 读不到版本（unknown）或非数字版本一律视为「不可判定」，返回 false，避免误更新。
func (e *EverythingRuntime) UpdateAvailable() bool {
	v := e.installedVersion()
	if v == "" {
		return false
	}
	return versionLess(v, everythingStableVer)
}

// versionLess 比较两个点分数字版本号（如 "1.4.1.1024" < "1.4.1.1032"）。
// 任一版本含不可解析为非数字的字段（如预发布后缀未剥离干净）则返回 false，表示「不可判定」。
func versionLess(a, b string) bool {
	as := splitVersion(a)
	bs := splitVersion(b)
	if as == nil || bs == nil {
		return false
	}
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return true
		}
		if av > bv {
			return false
		}
	}
	return false
}

// splitVersion 把 "1.4.1.1024beta" 这类串解析为 []int{1,4,1,1024}：按点分字段逐个取前导数字，
// 遇到首个完全不含数字的字段（如 "beta"）即判定整体不可比较，返回 nil。
// 空白与常见分隔符（-+~_ 与空白）先截断，兼容 "1.4.1 - beta" 这类写法。
func splitVersion(s string) []int {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "-+~_ \t"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		// 取字段的前导数字："1024beta" → 1024；字段无任何数字（如 "beta"）→ 不可比较。
		digits := 0
		for digits < len(p) && p[digits] >= '0' && p[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return nil
		}
		n, err := strconv.Atoi(p[:digits])
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// InstallEs 程序托管下载 es.exe（命令行工具）：es 发行包架构无关（32 位 exe 在 64 位系统上经 WOW64 运行），
// 因此不分架构统一下载 es-<ver>.zip，解压 es.exe 到 Everything 同目录（官方建议放一起），
// 否则落到 baseDir。已检测到 es.exe 则幂等跳过。
func (e *EverythingRuntime) InstallEs(ctx context.Context, cb InstallCallback) error {
	if e.detectEsExe() != "" {
		if cb.OnLog != nil {
			cb.OnLog("es.exe 已存在，无需重复下载")
		}
		return nil
	}
	destDir := e.baseDir
	if evExe := e.detectEverythingExe(); evExe != "" {
		destDir = filepath.Dir(evExe)
	}
	// es 命令行工具（ES）与 Everything 一样按架构分发（x64/x86），文件名大写 ES-<ver>.<arch>.zip。
	url := fmt.Sprintf("%sES-%s.%s.zip", everythingDownloadHost, esStableVer, everythingArch())
	zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("quickdock-es-%s-%s.zip", esStableVer, everythingArch()))

	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 es.exe 命令行工具…")
	}
	if cb.OnLog != nil {
		cb.OnLog(fmt.Sprintf("正在下载 es %s…", esStableVer))
	}
	if err := Download(ctx, zipPath, []string{url}, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 es.exe 失败: %w", err)
	}
	defer os.Remove(zipPath)

	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 es.exe…")
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	if err := Extract(zipPath, destDir); err != nil {
		return fmt.Errorf("解压 es.exe 失败: %w", err)
	}

	if e.detectEsExe() == "" {
		if p := findExeRecursive(destDir, []string{esExeName}); p != "" {
			// 兜底：把找到的 es.exe 直接拷到 destDir 根，确保探测稳定命中。
			_ = copyFile(p, filepath.Join(destDir, esExeName))
		}
	}
	if e.detectEsExe() == "" {
		return fmt.Errorf("解压完成但未找到 es.exe，请删除后重试")
	}
	if cb.OnLog != nil {
		cb.OnLog("es.exe 安装完成: " + e.detectEsExe())
	}
	return nil
}

// DeleteVersion 外部工具，QuickDock 不托管卸载（且可能动了系统 Program Files）。
func (e *EverythingRuntime) DeleteVersion(string) error {
	return fmt.Errorf("Everything 为外部工具，请在系统中卸载")
}

// ---- 探测 ----

// detectEverythingExe 在托管目录（baseDir）+ 默认目录 + 用户自定义目录里找到第一个存在的 Everything 可执行文件。
// 每个目录都做有界（≤3 层）递归，兼容「压缩包多套了一层目录」的布局差异。
func (e *EverythingRuntime) detectEverythingExe() string {
	dirs := []string{e.baseDir}
	dirs = append(dirs, everythingDefaultDirs()...)
	if custom := GetEverythingInstallPath(); custom != "" {
		dirs = append([]string{custom}, dirs...)
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if p := findExeRecursive(dir, everythingExeNames); p != "" {
			return p
		}
	}
	return ""
}

// detectEsExe 定位 es.exe：优先与 Everything.exe 同目录（官方建议放一起），
// 其次托管目录 baseDir，再次系统 PATH（独立分发常直接丢进 PATH），最后用户自定义路径。
func (e *EverythingRuntime) detectEsExe() string {
	evExe := e.detectEverythingExe()
	if evExe != "" {
		dir := filepath.Dir(evExe)
		p := filepath.Join(dir, esExeName)
		if fileExists(p) {
			return p
		}
	}
	if p := filepath.Join(e.baseDir, esExeName); fileExists(p) {
		return p
	}
	if custom := GetEsExePath(); custom != "" && fileExists(custom) {
		return custom
	}
	if p, err := exec.LookPath(esExeName); err == nil {
		return p
	}
	if p := findExeRecursive(e.baseDir, []string{esExeName}); p != "" {
		return p
	}
	return ""
}

// findExeRecursive 在 root 内（有界深度 3 层）查找首个命中 names 之一的可执行文件。
// 用来兼容压缩包解压后多套一层目录、或 exe 落在子目录的情况；超出深度即剪枝，避免误遍历整盘。
func findExeRecursive(root string, names []string) string {
	if root == "" {
		return ""
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	found := ""
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if rel, rerr := filepath.Rel(root, path); rerr == nil {
				depth := strings.Count(rel, string(os.PathSeparator))
				if depth > 3 {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if want[strings.ToLower(info.Name())] {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// copyFile 把 src 复制到 dst（用于把归档内找到的 es.exe 提到目录根，保证探测稳定命中）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// isRunning 通过全机进程快照判定 Everything.exe（或 Everything64.exe）是否有实例在跑。
func (e *EverythingRuntime) isRunning() bool {
	procs, err := sysutil.ListProcesses()
	if err != nil {
		logger.W("[env][everything] 进程枚举失败: %v", err)
		return false
	}
	for _, p := range procs {
		lower := strings.ToLower(p.Name)
		if lower == "everything.exe" || lower == "everything64.exe" {
			return true
		}
	}
	return false
}

// EverythingStatus 返回综合四态状态（供前端状态表展示），不去重、不依赖检测缓存。
func (e *EverythingRuntime) EverythingStatus() EverythingStatusInfo {
	info := EverythingStatusInfo{Enabled: FileSearchEnabled(), UpdatedAt: time.Now().Unix()}
	info.SearchDiag = e.SearchDiag()
	exe := e.detectEverythingExe()
	if exe == "" {
		info.State = EverythingStateNotInstalled
		return info
	}
	info.Installed = true
	info.ExePath = exe
	if v := readExeVersion(exe); v != "" {
		info.Version = v
	}
	info.LatestVersion = everythingStableVer
	info.Managed = e.isManaged(exe)
	if info.Version != "" {
		info.UpdateAvailable = versionLess(info.Version, everythingStableVer)
	}
	esExe := e.detectEsExe()
	if esExe == "" {
		info.State = EverythingStateMissingES
		info.HasEsExe = false
		return info
	}
	info.HasEsExe = true
	info.EsExePath = esExe
	if e.isRunning() {
		info.State = EverythingStateRunning
		info.Running = true
		// 自愈探针：进程在跑 ≠ 实例健康。卡死实例（实测签名：无窗口 / 2 线程 /
		// 0 CPU / 6MB 工作集，占着单实例互斥却不服务 IPC）会让所有检索拿到坏回复
		// （es exit 1），而状态面仍显示"运行中"——用户看到的正是这个矛盾。
		// 状态面每次被查看时探一次，探到卡死就地自愈（不等待索引，快进快出）。
		e.probeAndHeal()
	} else {
		info.State = EverythingStateStopped
	}
	return info
}

// probeAndHeal 用一次 IPC 探针探实例健康度；探到卡死（有 IPC 窗口但回复异常）
// 就地自愈：结束旧实例并无头重启。与 Search 路径共用冷却（60s），不会把状态面的
// 每次刷新都变成一轮杀重启。
//
// 只处理「卡死」这一种签名：ipcUnreachable（IPC not found）在「非管理员宿主 →
// 管理员实例」组合同样出现，那种实例是健康的，杀了也拉不起同等权限的替代品，
// 应走提权引导而不是自愈。
func (e *EverythingRuntime) probeAndHeal() {
	if e.recoverInCooldown() {
		return
	}
	if e.probeInstance() != ipcHung {
		return
	}
	logger.I("[env][everything] 状态探针发现实例卡死（IPC 回复异常），触发自愈")
	e.healHung(false)
}

// instanceHealth IPC 探针的三态结果。
type instanceHealth int

const (
	ipcHealthy     instanceHealth = iota // IPC 正常服务
	ipcHung                              // 有 IPC 窗口但回复异常（卡死僵尸的签名）
	ipcUnreachable                       // 连不上（没窗口 / 跨完整性被 UIPI 拦截 / es 缺失）
)

// probeInstance 以一次 es -get-everything-version（最轻的 IPC 往返，2s 超时）判定
// 运行中实例的健康度。选 -get-everything-version 而不是检索或计数：它不碰索引，
// 冷索引构建期间也能如实回答「IPC 通不通」。
func (e *EverythingRuntime) probeInstance() instanceHealth {
	esExe := e.detectEsExe()
	if esExe == "" {
		return ipcUnreachable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := sysutil.CommandContext(ctx, esExe, "-get-everything-version").CombinedOutput(); err != nil {
		if exitCodeOf(err) == 1 {
			return ipcHung
		}
		return ipcUnreachable
	}
	return ipcHealthy
}

// WarmUp 启动期预热文件检索：已装 + 未运行 + 总开关开 + 本进程建得了索引时，
// 后台无头拉起 Everything 实例。
//
// 为什么不在启动路径上省掉这一步：冷索引要把所有 NTFS 卷扫一遍（本机实测 1-2 分钟），
// 纯懒拉起会把这段空窗留给用户的第一次 Ctrl+K——搜了个空，用户看到的就是
// "还是没有文件分组"。提前到启动期，用户正常摸到面板/按下快捷键时索引已热或接近热。
// 用户自己开着实例（isRunning）则尊重现状，不抢。
func (e *EverythingRuntime) WarmUp() {
	if !FileSearchEnabled() {
		return
	}
	if e.detectEverythingExe() == "" || e.isRunning() {
		return
	}
	if err := e.Start(context.Background(), "", nil); err != nil {
		logger.W("[env][everything] 启动预热拉起失败: %v", err)
		return
	}
	logger.I("[env][everything] 启动预热：已后台拉起无头实例，索引构建中")
}

// IsAvailable 判定文件检索「能力就绪」：Everything 已装 + es.exe 在位 + 进程在跑。
// MCP file_search 工具仅在就绪时注册，避免暴露一个永远返回空的工具。
func (e *EverythingRuntime) IsAvailable() bool {
	st := e.EverythingStatus()
	return st.Installed && st.HasEsExe && st.Running
}

// ---- ServiceController 契约 ----

func (e *EverythingRuntime) DefaultPort() int { return 0 } // Everything 不走固定端口（走本地 IPC）

// Start 启动 Everything.exe（外部工具，直接拉起其 GUI/托盘进程）。
// 已在运行则幂等返回；未安装则返回明确错误引导去官网。
func (e *EverythingRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	exe := e.detectEverythingExe()
	if exe == "" {
		return fmt.Errorf("Everything 未安装，请先下载安装")
	}
	if e.isRunning() {
		// 进程在跑 ≠ 实例可用：卡死实例（IPC 回复异常）会让调用方以为"启动成功"
		// 却永远搜不到东西。探一次，只有探到卡死签名才转为自愈、换一个真能用的实例；
		// ipcUnreachable（典型：提权实例 vs 普通宿主）不杀——杀不动也不是我们的错，
		// 留给诊断指引，别误杀用户自己开着的实例。
		if e.probeInstance() == ipcHung {
			logger.I("[env][everything] Everything 已在运行但 IPC 异常，转为自愈重启")
			e.healHung(false)
			return nil
		}
		if onLog != nil {
			onLog("Everything 已在运行")
		}
		return nil
	}
	// -startup：无头后台运行，不弹 GUI 窗口。QuickDock 只经 es.exe IPC 检索，不需要
	// Everything 的窗口；无头启动也避免用户被多余的托盘图标/窗口干扰。
	//
	// 两个实测教训（2026-10-22，本机）：
	//  1. 实例必须与本进程同完整性级别——跨完整性 IPC 被 UIPI 拦（es exit 8/1），
	//     所以 QuickDock 自身不要以管理员身份运行（子进程会继承令牌）；
	//  2. 拉起前必须确保旧实例已退干净（见 Stop 的退出等待），否则新实例撞上尚未
	//     释放的单实例互斥会把自己误判成"第二个实例"而秒退。
	cmd := sysutil.Command(exe, "-startup")
	cmd.Dir = filepath.Dir(exe)
	if err := sysutil.StartDetached(cmd); err != nil {
		// StartDetached 失败回退：直接 Start（仍不等待，避免阻塞调用方）。
		if ferr := cmd.Start(); ferr != nil {
			return fmt.Errorf("启动 Everything 失败: %w", ferr)
		}
		return nil
	}
	logger.I("[env][everything] 已启动 %s", exe)
	if onLog != nil {
		onLog("已启动 Everything")
	}
	return nil
}

// Stop 停止 Everything.exe：结束所有同名进程（托盘实例）。
// 区分两种失败：目标进程以更高完整性（管理员）运行时终止被拒——此时既杀不掉，
// es.exe 的跨完整性 IPC 同样连不上，必须给用户明确指引，而不是一句「未发现进程」。
func (e *EverythingRuntime) Stop(version string) error {
	exe := e.detectEverythingExe()
	targets := map[string]bool{"everything.exe": true, "everything64.exe": true}
	if exe != "" {
		targets[strings.ToLower(filepath.Base(exe))] = true
	}
	procs, err := sysutil.ListProcesses()
	if err != nil {
		return fmt.Errorf("枚举进程失败: %w", err)
	}
	killed := 0
	denied := 0
	for _, p := range procs {
		if !targets[strings.ToLower(p.Name)] {
			continue
		}
		if kerr := sysutil.KillProcess(p.PID); kerr != nil {
			// 拒绝访问基本只有一种解释：目标以管理员身份运行，而本进程不是。
			logger.W("[env][everything] 结束进程 %d 失败（疑为管理员实例）: %v", p.PID, kerr)
			denied++
			continue
		}
		killed++
	}
	if killed == 0 {
		if denied > 0 {
			return fmt.Errorf("Everything 正以管理员身份运行，QuickDock（非管理员）无法结束它；文件检索的 IPC 同样不可用，请先从系统托盘退出 Everything")
		}
		return fmt.Errorf("未发现正在运行的 Everything 进程")
	}
	logger.I("[env][everything] 已结束 %d 个 Everything 进程", killed)
	// 等进程真正退出再返回：TerminateProcess 是异步生效的，旧实例还攥着单实例互斥时，
	// 紧接着拉起的新实例会把它当"已有实例"而直接退出——实测自愈重启后 es 报
	// IPC not found、新实例秒退，正是这个竞态。不等到"互斥已释放"，Stop→Start
	// 就是在赌运气。
	deadline := time.Now().Add(everythingStopWait)
	for {
		if !e.isRunning() {
			return nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(everythingStopPollInterval)
	}
	return fmt.Errorf("已发出结束指令，但仍有 Everything 进程未在 %s 内退出（疑为更高完整性级别，本进程杀不动）", everythingStopWait)
}

// Status 返回 Everything.exe 的 ServiceStatus（通用运行态，仅含运行/版本/pid）。
func (e *EverythingRuntime) Status(version string) ServiceStatus {
	st := ServiceStatus{Running: false, Version: e.EverythingStatus().Version}
	if e.isRunning() {
		st.Running = true
		if procs, err := sysutil.ListProcesses(); err == nil {
			for _, p := range procs {
				if strings.EqualFold(p.Name, "Everything.exe") || strings.EqualFold(p.Name, "Everything64.exe") {
					st.PID = p.PID
					st.MemBytes = p.MemBytes
					st.ProcCount = 1
					break
				}
			}
		}
	}
	return st
}

// ---- 文件检索 ----

// FileSearchResult 单条文件检索结果（es.exe 每行一个完整路径归一化而来）。
type FileSearchResult struct {
	Path     string `json:"path"`     // 完整路径
	Name     string `json:"name"`     // 文件名（含扩展名）
	Dir      string `json:"dir"`      // 所在目录
	Modified int64  `json:"modified"` // 修改时间 Unix 秒（0=未知）
}

// Search 通过 es.exe 检索本地文件，内部调用 os/exec，默认限 -n 30、按名称排序、超时 2s。
//
// 降级策略（绝不让文件检索阻塞 QuickDock 自有资源检索）：
//   - es.exe 缺失 / Everything 未运行 / 查询超时 / exec 失败 → 返回空结果 + 记录日志，不返回 error；
//   - 仅当 query 为空等参数非法时返回 error（调用方据此提示），其余一律静默降级。
//
// 按需拉起：es.exe 强依赖 Everything IPC（Everything 没在跑时直接报 Error 8）。
// 用户在命令面板输入即表达检索意图，此时若已安装但进程没起，自动拉起一次（Start 幂等），
// 有界等待就绪后再检索；拉不起来仍静默降级——不弹窗、不阻塞。
// 索引就绪前检索会返回空结果（es exit 0 无输出），这是正常空窗，不是故障。
func (e *EverythingRuntime) Search(ctx context.Context, query string, limit int, sortBy string) ([]FileSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("检索词不能为空")
	}
	esExe := e.detectEsExe()
	if esExe == "" {
		logger.W("[env][everything] es.exe 缺失，文件检索降级为空结果")
		return nil, nil
	}
	autoStarted := false
	if !e.isRunning() {
		if e.detectEverythingExe() != "" {
			if serr := e.Start(ctx, "", nil); serr != nil {
				logger.W("[env][everything] 按需拉起 Everything 失败: %v", serr)
			} else {
				autoStarted = true
				e.waitRunning(ctx, everythingWarmupMax)
			}
		}
		if !e.isRunning() {
			logger.W("[env][everything] Everything 未运行且未能自动拉起，文件检索降级为空结果")
			return nil, nil
		}
	}
	if limit <= 0 {
		limit = 30
	}
	sortCol := normalizeSort(sortBy)

	args := []string{"-n", fmt.Sprintf("%d", limit), "-sort", sortCol, query}

	out, err := e.runEs(ctx, esExe, args)
	if err != nil {
		code := exitCodeOf(err)
		// es.exe 的诊断信息只写在 stderr，以前被整体丢弃，排查时只剩一个退出码
		// （这次「exit 1 到底是什么」就是靠重新实证才定位到卡死实例的）。
		logger.W("[env][everything] es.exe 执行失败（query=%q, exit=%d）: %v | 输出: %s",
			query, code, err, truncateLog(string(out)))

		// 冷启动首撞：进程虽已拉起，IPC 窗口可能还没建好（es.exe 报 Error 8）。
		// 对「本次自动拉起」与「刚被自愈换新的实例」重试一次，避免无谓地拖长
		// 正常失败的耗时——后者典型场景：状态面探针刚杀掉卡死实例并无头重启，
		// 用户紧接着按 Ctrl+K，新实例的 IPC 窗口还没就绪。
		if autoStarted || e.recentlyHealed() {
			select {
			case <-ctx.Done():
			case <-time.After(everythingRetryWait):
				out2, err2 := e.runEs(ctx, esExe, args)
				if err2 == nil {
					e.setSearchDiag("")
					return parseEsOutput(string(out2)), nil
				}
			}
		}

		// 可诊断的典型场景单独标记，供状态面给用户明确指引（而不是一句「没有结果」）：
		// Error 8 = IPC not found 而进程在跑：实例与 QuickDock 完整性级别不同
		// （典型是用户自己以管理员身份开着 Everything），跨完整性 IPC 被 UIPI 拦截，
		// 且本进程杀不动那个实例——引导用户从托盘退出它，QuickDock 会重新拉起同级实例。
		if code == 8 {
			e.setSearchDiag(EverythingDiagIpcElevated)
			return nil, nil
		}

		// exit 1 = IPC 收发成功但回复异常：实例卡死的典型表现（占着单实例互斥却不服务，
		// 实测为「无窗口 / 2 线程 / 0 CPU」的僵尸进程）。有界自愈一次：结束旧实例 →
		// 无头重新拉起 → 等索引就绪 → 重试；杀不掉（更高完整性级别）时转为提权引导。
		if code == 1 && e.tryRecoverHung() {
			out3, err3 := e.runEs(ctx, esExe, args)
			if err3 == nil {
				e.setSearchDiag("")
				return parseEsOutput(string(out3)), nil
			}
			logger.W("[env][everything] 自愈后重试仍失败（query=%q, exit=%d）: %v",
				query, exitCodeOf(err3), truncateLog(string(out3)))
		}
		return nil, nil
	}
	e.setSearchDiag("")
	return parseEsOutput(string(out)), nil
}

// runEs 以 2s 超时执行一次 es.exe。每次调用独立计时——自愈重试不能共用首次那个
// 可能已耗尽的超时窗口。
func (e *EverythingRuntime) runEs(ctx context.Context, esExe string, args []string) ([]byte, error) {
	execCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sysutil.CommandContext(execCtx, esExe, args...).CombinedOutput()
}

// recoverInCooldown 自愈是否处于冷却期（纯内存判断，无副作用）。
// 自愈后索引尚未就绪时，紧接着的检索仍会拿到 exit 1；没有冷却就会每次都杀一遍
// 进程，把「等索引」恶化成「反复重启」。
func (e *EverythingRuntime) recoverInCooldown() bool {
	e.recoverMu.Lock()
	defer e.recoverMu.Unlock()
	return !e.lastRecoverAt.IsZero() && time.Since(e.lastRecoverAt) < everythingRecoverCooldown
}

// recentlyHealed 最近一次自愈（30s 内）是否刚把实例换新。自愈重启的实例 IPC 窗口
// 尚未就绪时，紧随的检索同样会撞 exit 8——与「本次自动拉起」同等对待，给一次重试。
func (e *EverythingRuntime) recentlyHealed() bool {
	e.recoverMu.Lock()
	defer e.recoverMu.Unlock()
	return !e.lastRecoverAt.IsZero() && time.Since(e.lastRecoverAt) < 30*time.Second
}

// tryRecoverHung 检索路径的自愈（含索引等待，让紧随的重试有机会直接命中）。
func (e *EverythingRuntime) tryRecoverHung() bool {
	return e.healHung(true)
}

// healHung 卡死实例自愈主体：结束旧实例并无头重启；waitIndex 为真时再有界等待索引
// 出现内容（检索路径用，让重试有机会直接命中；状态面探针路径传 false，快进快出——
// 状态面每次被查看都可能触发，不能为等索引把面板挂住十几秒）。
// 冷却判定与时间戳统一在这里。返回是否值得重试检索。
func (e *EverythingRuntime) healHung(waitIndex bool) bool {
	if e.recoverInCooldown() {
		logger.I("[env][everything] 实例自愈处于 %s 冷却期，本次跳过", everythingRecoverCooldown)
		return false
	}
	e.recoverMu.Lock()
	e.lastRecoverAt = time.Now()
	e.recoverMu.Unlock()

	logger.I("[env][everything] Everything 实例卡死（IPC 回复异常），开始自愈：结束旧实例并无头重启")
	if err := e.Stop(""); err != nil {
		logger.W("[env][everything] 自愈失败：无法结束卡死实例: %v", err)
		// 杀不掉基本只有一种解释：实例比本进程完整性级别高（管理员实例 vs 普通
		// QuickDock）。IPC 同样被 UIPI 拦着，检索没救——给明确指引，别让用户
		// 对着空结果发呆。
		e.setSearchDiag(EverythingDiagIpcElevated)
		return false
	}
	if err := e.Start(context.Background(), "", nil); err != nil {
		logger.W("[env][everything] 自愈失败：重新拉起 Everything 失败: %v", err)
		return false
	}
	if !waitIndex {
		return true
	}
	// 冷索引要把所有 NTFS 卷扫一遍，可能几十秒；最多等 everythingIndexWarmup，
	// 超时也返回 true——索引在后台继续建，用户继续输入时后续检索即可命中。
	if n := e.waitIndexed(everythingIndexWarmup); n > 0 {
		logger.I("[env][everything] 新实例索引就绪（%d 条），重试检索", n)
	} else {
		logger.I("[env][everything] 新实例索引尚未就绪（后台继续构建），本次重试可能为空")
	}
	return true
}

// waitIndexed 轮询 es -get-result-count 直到索引出现内容或超时，返回观测到的条数（0=未就绪）。
// 用独立 background 上下文：调用方的 ctx 可能已被上层取消，不该连累索引等待。
func (e *EverythingRuntime) waitIndexed(max time.Duration) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), max)
	defer cancel()
	esExe := e.detectEsExe()
	if esExe == "" {
		return 0
	}
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(everythingIndexPollInterval):
		}
		if !e.isRunning() {
			return 0
		}
		out, err := e.runEs(ctx, esExe, []string{"-get-result-count", "*"})
		if err != nil {
			continue
		}
		if n, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil && n > 0 {
			return n
		}
	}
}

// truncateLog 截断超长文本再进日志：es.exe 失败时可能带上全量结果，不截断会刷爆日志。
func truncateLog(s string) string {
	const max = 500
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(已截断)"
}

// exitCodeOf 取 exec 错误的退出码；非 *ExitError（如启动失败）返回 -1。
func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Everything 检索诊断码（SearchDiag）：把「静默降级」里可自愈的场景点出来。
const (
	// ipc_elevated：Everything 正以管理员身份运行而 QuickDock 不是（或反之），
	// 跨完整性 IPC 被 UIPI 拦截。解法是让两边同级别——退出提权的 Everything，
	// 由 QuickDock 重新拉起一个同完整性实例。
	EverythingDiagIpcElevated = "ipc_elevated"
)

// setSearchDiag 记录最近一次检索的诊断（空串=恢复正常，清除旧诊断）。
// Ctrl+K（前端防抖异步）与 MCP 工具可能并发检索，故加锁。
func (e *EverythingRuntime) setSearchDiag(diag string) {
	e.searchDiagMu.Lock()
	e.searchDiag = diag
	e.searchDiagMu.Unlock()
}

// SearchDiag 返回最近一次检索留下的诊断码（无诊断返回空串）。
func (e *EverythingRuntime) SearchDiag() string {
	e.searchDiagMu.Lock()
	defer e.searchDiagMu.Unlock()
	return e.searchDiag
}

// everythingWarmupMax / everythingRetryWait 冷启动检索的等待参数：
// 拉起 Everything 后轮询进程就绪的上限，以及首撞 IPC 未就绪时的重试间隔。
const (
	everythingWarmupMax = 1500 * time.Millisecond
	everythingRetryWait = 600 * time.Millisecond
)

// waitRunning 有界轮询 Everything 进程是否就绪。进程出现不等于 IPC 立即可用，
// 就绪后再留一小段固定余量，尽量让紧随其后的 es.exe 首调不撞上 Error 8。
func (e *EverythingRuntime) waitRunning(ctx context.Context, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if e.isRunning() {
			time.Sleep(300 * time.Millisecond) // IPC 窗口建立余量
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// normalizeSort 把调用方可选的排序列规范化；未知值回退到 name（es.exe 默认值之一）。
func normalizeSort(sortBy string) string {
	s := strings.ToLower(strings.TrimSpace(sortBy))
	switch s {
	case "path", "size", "date_modified", "date_created", "date_recently_changed", "name":
		return s
	default:
		return "name"
	}
}

// parseEsOutput 把 es.exe 的逐行输出解析为文件结果。每行一个完整路径；
// 行尾 \r\n 与空行剔除；统计每个文件的修改时间（上限 30 条，开销可忽略）。
func parseEsOutput(out string) []FileSearchResult {
	results := make([]FileSearchResult, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.Trim(line, "\r")
		if line == "" {
			continue
		}
		res := FileSearchResult{Path: line}
		res.Name = filepath.Base(line)
		res.Dir = filepath.Dir(line)
		if info, err := os.Stat(line); err == nil {
			res.Modified = info.ModTime().Unix()
		}
		results = append(results, res)
	}
	return results
}

// EverythingStatusInfo 前端状态表展示用的综合四态信息。
type EverythingStatusInfo struct {
	Installed       bool   `json:"installed"`       // Everything.exe 是否找到
	HasEsExe        bool   `json:"hasEsExe"`        // es.exe 是否就绪
	Running         bool   `json:"running"`         // Everything.exe 进程是否在跑
	State           string `json:"state"`           // 四态之一：not_installed/missing_es/stopped/running
	Version         string `json:"version"`         // Everything 版本（PE 资源）
	LatestVersion   string `json:"latestVersion"`   // QuickDock 认定的最新稳定版目标版本
	UpdateAvailable bool   `json:"updateAvailable"` // 已安装版本是否严格落后于 LatestVersion
	Managed         bool   `json:"managed"`         // 检测到的 Everything 是否落在托管目录（可一键更新）
	ExePath         string `json:"exePath"`         // Everything.exe 路径
	EsExePath       string `json:"esExePath"`       // es.exe 路径
	SearchDiag      string `json:"searchDiag"`      // 最近一次检索的诊断码（空=正常；见 EverythingDiag*）
	Enabled         bool   `json:"enabled"`         // 文件搜索总开关是否开启
	UpdatedAt       int64  `json:"updatedAt"`       // 探测时间戳（毫秒级一致性无所谓，秒即可）
}

// EverythingSettings 文件搜索相关设置（持久化于 env/settings.json）。
type EverythingSettings struct {
	InstallPath       string `json:"installPath"`       // 自定义 Everything 安装目录
	EsExePath         string `json:"esExePath"`         // 自定义 es.exe 路径
	FileSearchEnabled bool   `json:"fileSearchEnabled"` // 是否启用文件搜索（命令面板 / MCP）
}

// ---- 编译期接口断言 ----

var (
	_ RuntimeAdapter    = (*EverythingRuntime)(nil)
	_ ServiceController = (*EverythingRuntime)(nil)
)
