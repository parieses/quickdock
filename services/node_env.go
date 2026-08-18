package services

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows/registry"
)

const (
	nodeVersion    = "v22.22.2" // LTS，匹配本机托管运行时；DSH 要求 ^22.19.0 || >=24.0.0
	nodeRuntimeRel = "runtime/node"
	dshHomeRel     = "dsh"
	dshPkg         = "@deepseek-ai/dsh"
	// npm registry 元数据（与安装镜像一致，用于最新版本检测）
	dshRegistryJSON = "https://registry.npmmirror.com/@deepseek-ai/dsh"
	// 便携 Node 解压安全上限（Node v22 win-x64 解压后 ~200MB，node.exe 单文件 ~120MB，
	// 旧值 80MB 会让 node.exe 误报「文件过大」，放大到足够容纳）
	maxNodeZipSize  int64 = 600 << 20
	maxNodeFileSize int64 = 300 << 20
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

// setupProgress 一键安装进度（经 quickdock:dsh:progress 事件推前端）
type setupProgress struct {
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

// NodeEnvManager 检测并提供 Node 运行时；缺失时下载便携版并在其下安装 dsh。
// 不依赖系统 PATH、不写注册表、不请求管理员权限——纯用户态、随 QuickDock 清理。
type NodeEnvManager struct {
	app        *application.App
	dataDir    string
	runtimeDir string
	installing atomic.Bool
	mu         sync.Mutex
}

func NewNodeEnvManager() *NodeEnvManager {
	dataDir := platform.DefaultDataDir()
	return &NodeEnvManager{
		dataDir:    dataDir,
		runtimeDir: filepath.Join(dataDir, nodeRuntimeRel),
	}
}

func (m *NodeEnvManager) SetApp(app *application.App) { m.app = app }

// EmitLog 向前端 DSH 日志面板推送一行日志
func (m *NodeEnvManager) EmitLog(level, msg string) {
	if m.app != nil {
		m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
	}
}

func (m *NodeEnvManager) nodeExe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.runtimeDir, "node.exe")
	}
	return filepath.Join(m.runtimeDir, "node")
}

func (m *NodeEnvManager) npxExe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.runtimeDir, "npx.cmd")
	}
	return filepath.Join(m.runtimeDir, "npx")
}

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
	if m.runtimeDir != "" {
		dirs = append(dirs, m.runtimeDir)
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

func npmGlobalRoot() string {
	npmGlobalRootMu.Lock()
	defer npmGlobalRootMu.Unlock()
	if npmGlobalRootCached {
		return npmGlobalRootVal
	}
	out, err := exec.Command("npm", "root", "-g").Output()
	if err == nil {
		npmGlobalRootVal = strings.TrimSpace(string(out))
		npmGlobalRootCached = true
	}
	return npmGlobalRootVal
}

// npmDefaultBinDir npm 默认全局 bin 目录（Windows 上为 %AppData%/npm，存放 dsh.cmd 等 shim）
func npmDefaultBinDir() string {
	if ad, err := os.UserConfigDir(); err == nil { // Windows: %AppData%
		return filepath.Join(ad, "npm")
	}
	return ""
}

// npxCacheDshBins 扫描 npx 临时缓存中的 dsh 入口（Windows 缓存可能落在 ~/.npm 或 %LocalAppData%/npm-cache）
func npxCacheDshBins() []string {
	var out []string
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	roots := []string{filepath.Join(home, ".npm")}
	if ld, err := os.UserCacheDir(); err == nil { // Windows: %LocalAppData%
		roots = append(roots, filepath.Join(ld, "npm-cache"))
	}
	for _, r := range roots {
		if st, err := os.Stat(filepath.Join(r, "_npx")); err != nil || !st.IsDir() {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(r, "_npx", "*", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
		if len(matches) > 0 {
			out = append(out, matches...)
		}
	}
	return out
}

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
	if m.runtimeDir != "" {
		cands = append(cands,
			filepath.Join(m.runtimeDir, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
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

// nodeVersionOK 判断 node -v 输出是否满足 dsh 要求：^22.19.0 || >=24.0.0
func nodeVersionOK(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major == 22 {
		return minor >= 19
	}
	return major >= 24
}

// nodeSupportPath 探测 exe 的 node 版本是否满足要求
func nodeSupportPath(exe string) (string, bool) {
	v := runVersion(exe, "--version")
	return v, v != "" && nodeVersionOK(v)
}

// NodePath 返回实际可用的 node：优先系统 PATH（版本满足要求），其次便携目录。
// 系统 node 版本过低时降级到便携版，避免把 dsh 装进一个跑不起来的旧环境。
func (m *NodeEnvManager) NodePath() string {
	if p, err := exec.LookPath("node"); err == nil {
		if _, ok := nodeSupportPath(p); ok {
			return p
		}
	}
	if _, err := os.Stat(m.nodeExe()); err == nil {
		if _, ok := nodeSupportPath(m.nodeExe()); ok {
			return m.nodeExe()
		}
	}
	return ""
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
	if p := m.NodePath(); p != "" {
		st.NodeFound = true
		st.NodePath = p
		st.NodeVersion = runVersion(p, "--version")
		st.NodeSupport = nodeVersionOK(st.NodeVersion)
	} else if p, err := exec.LookPath("node"); err == nil {
		// 系统里有 node 但版本不满足——说明原因，安装会走内置 Node
		if v := runVersion(p, "--version"); v != "" {
			st.NodeFound = true
			st.NodePath = p
			st.NodeVersion = v
			st.NodeSupport = false
			st.Message = "系统 Node 版本不满足要求（需 ^22.19.0 或 >=24.0.0），将使用内置 Node"
		}
	}
	if p, err := exec.LookPath("npx"); err == nil {
		st.NpxFound = true
		st.NpxVersion = runVersion(p, "--version")
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
	resp, err := (&http.Client{Transport: m.proxyTransport()}).Do(req)
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
	if v := info.DistTags["latest"]; v != "" {
		return v
	}
	return info.Version
}

// UpdateDSH 将 dsh 更新到最新版（stage: update-dsh）。与安装共用 installing 锁防并发。
func (m *NodeEnvManager) UpdateDSH(ctx context.Context) error {
	emit := func(sp setupProgress) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:progress", sp)
		}
	}
	logf := func(level, msg string) { m.EmitLog(level, msg) }

	if m.installing.Swap(true) {
		// 锁被占用也补发 error 事件，前端 installingPlugin/settingUp/updating 才能复位
		emit(setupProgress{Stage: "error", Message: "安装/更新正在进行中，请稍候"})
		return fmt.Errorf("安装/更新正在进行中")
	}
	defer m.installing.Store(false)

	if m.NodePath() == "" {
		emit(setupProgress{Stage: "error", Message: "运行环境未就绪，无法更新"})
		return fmt.Errorf("运行环境未就绪，无法更新")
	}
	emit(setupProgress{Stage: "update-dsh", Message: "正在更新 DeepSeek Harness…"})
	logf("info", "更新 "+dshPkg+"@latest（registry: npmmirror）")
	if err := m.runNpmInstall(ctx, true, func(msg string) {
		emit(setupProgress{Stage: "update-dsh", Message: msg})
		logf("info", msg)
	}); err != nil {
		emit(setupProgress{Stage: "error", Message: "更新 dsh 失败: " + err.Error()})
		logf("error", "更新 dsh 失败: "+err.Error())
		return err
	}
	logf("info", "DeepSeek Harness 更新完成（版本 "+m.DshVersion()+"）")
	emit(setupProgress{Stage: "done", Message: "更新完成"})
	return nil
}

func runVersion(exe string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetupDSH 一键安装：node 缺失则下载便携版，dsh 缺失则 npm 安装；分阶段回调进度。
// 进度同时经 a.app.Event.Emit("quickdock:dsh:progress", ...) 推前端，故 onProgress 可为 nil。
func (m *NodeEnvManager) SetupDSH(ctx context.Context, onProgress func(setupProgress)) error {
	emit := func(sp setupProgress) {
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
		emit(setupProgress{Stage: "error", Message: "安装正在进行中，请稍候"})
		return fmt.Errorf("安装正在进行中")
	}
	defer m.installing.Store(false)

	logf("info", "开始准备运行环境（缺失 Node 将下载，缺失 dsh 将安装）")

	// 1. 确保 node
	if m.NodePath() == "" {
		emit(setupProgress{Stage: "download-node", Message: "正在下载 Node 运行时…"})
		zipPath := filepath.Join(os.TempDir(), "quickdock-node.zip")
		if err := m.downloadNode(ctx, zipPath, func(w, t int64) {
			emit(setupProgress{Stage: "download-node", Written: w, Total: t, Message: "正在下载 Node 运行时…"})
		}, func(msg string) { logf("info", msg) }); err != nil {
			emit(setupProgress{Stage: "error", Message: "下载 Node 失败: " + err.Error()})
			logf("error", "下载 Node 失败: "+err.Error())
			return err
		}
		emit(setupProgress{Stage: "extract-node", Message: "正在解压 Node…"})
		logf("info", "解压 Node 到 "+m.runtimeDir)
		if err := extractNodeZip(zipPath, m.runtimeDir); err != nil {
			emit(setupProgress{Stage: "error", Message: "解压 Node 失败: " + err.Error()})
			logf("error", "解压 Node 失败: "+err.Error())
			return err
		}
		// 强制校验：解压后 node.exe 必须落在预期位置，否则后续 fork/exec 报"目录名无效"
		if _, err := os.Stat(m.nodeExe()); err != nil {
			msg := fmt.Sprintf("解压完成但未找到 %s，请删除 %s 后重试", m.nodeExe(), m.runtimeDir)
			emit(setupProgress{Stage: "error", Message: msg})
			logf("error", msg)
			return fmt.Errorf("%s", msg)
		}
		logf("info", "Node 解压完成")
		os.Remove(zipPath)
	} else {
		logf("info", "Node 已就绪: "+m.NodePath())
	}

	// 2. 确保 dsh（任意来源已安装即跳过：便携安装 / 全局 npm / ~/.dsh 手动初始化过）
	if !m.dshInstalledAnywhere() {
		emit(setupProgress{Stage: "install-dsh", Message: "正在安装 DeepSeek Harness…"})
		logf("info", "安装 "+dshPkg+"（registry: npmmirror）")
		if err := m.installDSH(ctx, func(msg string) {
			emit(setupProgress{Stage: "install-dsh", Message: msg})
			logf("info", msg)
		}); err != nil {
			emit(setupProgress{Stage: "error", Message: "安装 dsh 失败: " + err.Error()})
			logf("error", "安装 dsh 失败: "+err.Error())
			return err
		}
		logf("info", "DeepSeek Harness 安装完成")
	} else {
		logf("info", "DeepSeek Harness 已安装（跳过安装）")
	}

	emit(setupProgress{Stage: "done", Message: "运行环境已就绪"})
	logf("info", "运行环境已就绪，可打开 DeepSeek Harness")
	return nil
}

// downloadNode 依次尝试 npmmirror 镜像与 nodejs.org 官方源
func (m *NodeEnvManager) downloadNode(ctx context.Context, dst string, onProgress func(int64, int64), onLog func(string)) error {
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	urls := []string{
		fmt.Sprintf("https://registry.npmmirror.com/-/binary/node/%s/node-%s-win-%s.zip", nodeVersion, nodeVersion, arch),
		fmt.Sprintf("https://nodejs.org/dist/%s/node-%s-win-%s.zip", nodeVersion, nodeVersion, arch),
	}
	var lastErr error
	for _, u := range urls {
		if onLog != nil {
			onLog("尝试下载源: " + u)
		}
		if err := downloadFile(ctx, m.proxyTransport(), u, dst, onProgress); err == nil {
			if onLog != nil {
				onLog("下载源可用: " + u)
			}
			return nil
		} else {
			lastErr = err
			if onLog != nil {
				onLog("下载源失败: " + err.Error() + "，尝试下一个")
			}
		}
	}
	return lastErr
}

// downloadFile 下载到临时文件，完整成功后才写入 dst（避免中途失败污染 dst）
func downloadFile(ctx context.Context, transport http.RoundTripper, urlStr, dst string, onProgress func(int64, int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("下载失败 HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "quickdock-dl-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpPath) }()

	total := resp.ContentLength
	written := int64(0)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, tmp); err != nil {
		return err
	}
	return nil
}

// extractNodeZip 解压 Node zip 到 dest，并扁平化顶层目录（node-vX-win-x64/）
func extractNodeZip(zipPath, dest string) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	var total int64
	for _, f := range r.File {
		// 去掉顶部目录 node-vX-win-x64/（兼容镜像 zip 可能用反斜杠分隔）
		name := strings.ReplaceAll(f.Name, "\\", "/")
		parts := strings.SplitN(name, "/", 2)
		rel := parts[0]
		if len(parts) == 2 {
			rel = parts[1]
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, rel)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("非法路径: %s", f.Name)
		}
		if f.UncompressedSize64 > uint64(maxNodeFileSize) {
			return fmt.Errorf("文件过大: %s", f.Name)
		}
		if total+int64(f.UncompressedSize64) > maxNodeZipSize {
			return fmt.Errorf("解压总大小超出限制")
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.CopyN(out, rc, maxNodeFileSize); err != nil && err != io.EOF {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
		total += int64(f.UncompressedSize64)
	}
	return nil
}

// installDSH 安装最新发布的 dsh（等价 npm install -g @deepseek-ai/dsh --prefix <installDir>）
func (m *NodeEnvManager) installDSH(ctx context.Context, onMsg func(string)) error {
	return m.runNpmInstall(ctx, false, onMsg)
}

// runNpmInstall 用当前 node 的 npm 将 dsh 安装到 installDir()（--prefix），国内走 npmmirror 镜像。
// latest=true 时显式安装 @latest（更新场景）。DSH_HOME 指向数据目录（DshHome），
// 让 dsh 的 postinstall 在数据目录初始化 profile。
func (m *NodeEnvManager) runNpmInstall(ctx context.Context, latest bool, onMsg func(string)) error {
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
	if latest {
		pkgArg = dshPkg + "@latest"
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
	if nodeDir := m.runtimeDir; nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	// hideWindowAttr：Windows 用 CREATE_NO_WINDOW（0x08000000）让 npm/cmd 子进程继承隐藏控制台；
	// 非 Windows 返回 nil。切勿用 DETACHED_PROCESS(0x00000008)——孙进程会各自弹窗。
	cmd.SysProcAttr = hideWindowAttr()
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
	return nil
}

// proxyTransport 返回代理感知的 http transport：优先环境变量，回退 WinINET 系统代理
func (m *NodeEnvManager) proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: systemProxyURL, TLSHandshakeTimeout: 10 * time.Second}
}

func systemProxyURL(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return nil, nil
	}
	host := server
	if i := strings.Index(server, "="); i >= 0 {
		rest := server[i+1:]
		if j := strings.Index(rest, ";"); j >= 0 {
			rest = rest[:j]
		}
		host = rest
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return url.Parse(host)
}
