package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/registry"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/internal/plugin"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// ---- 主窗口尺寸/位置记忆 ----
// 启动时 DB 尚未初始化（ServiceStartup 在 app.Run 内执行），故用独立的 JSON 文件
// 持久化窗口矩形，避免依赖数据库时序。启动时 loadWindowState 恢复，移动/缩放后防抖写回。
type windowState struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func windowStatePath() string {
	return filepath.Join(EnsureConfigDir(), "window_state.json")
}

func loadWindowState() *windowState {
	data, err := os.ReadFile(windowStatePath())
	if err != nil {
		return nil
	}
	var s windowState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	// 基本合理性校验：小于最小尺寸或越界视为无效
	if s.Width < 800 || s.Height < 500 {
		return nil
	}
	return &s
}

var (
	windowStateMu        sync.Mutex
	windowStateSaveTimer *time.Timer
)

// scheduleSaveWindowState 防抖保存窗口矩形。最大化/最小化态不记录，避免恢复成畸形窗口。
func scheduleSaveWindowState(w *application.WebviewWindow) {
	if w == nil || w.IsMaximised() || w.IsMinimised() {
		return
	}
	windowStateMu.Lock()
	if windowStateSaveTimer != nil {
		windowStateSaveTimer.Stop()
	}
	windowStateSaveTimer = time.AfterFunc(400*time.Millisecond, func() {
		b := w.Bounds()
		s := windowState{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
		if data, err := json.Marshal(s); err == nil {
			_ = os.WriteFile(windowStatePath(), data, 0644)
		}
	})
	windowStateMu.Unlock()
}

//go:embed all:frontend/dist
var assets embed.FS

//go:embed all:plugins/builtin
var builtinPlugins embed.FS

//go:embed updater.key.pub
var updaterPublicKey []byte

// appVersion 在编译时通过 -ldflags="-X main.appVersion=0.2.0" 注入版本号
var appVersion = "0.0.0"

const (
	appTitle         = "快启坞 QuickDock"
	appWidth         = 1100
	appHeight        = 700
	clipWinWidth     = 480
	clipWinHeight    = 540
	paletteWinWidth  = 900
	paletteWinHeight = 600
)

// 全局状态标志（main/tray.go 与 services 共享）
var (
	windowVisible atomic.Bool
	clipboardMode atomic.Bool
	paletteMode   atomic.Bool
	noteMode      atomic.Bool
)

func main() {
	// 单实例检查：若已有实例运行，将其窗口提到前台并退出
	if ensureSingleInstance() {
		return
	}

	// 创建 AppService 实例
	appService := services.NewAppService()

	// 注入共享状态（同一 atomic.Bool，main 包和 services 包共享）
	appService.WindowVisible = &windowVisible
	appService.ClipboardMode = &clipboardMode
	appService.PaletteMode = &paletteMode
	appService.NoteMode = &noteMode

	// 注入热键监听回调（避免循环依赖）
	appService.StartHotkeyListenerFn = StartHotkeyListener
	appService.SuspendHotkeysFn = SuspendHotkeys
	appService.ResumeHotkeysFn = ResumeHotkeys

	// 真退出标记：与托盘"退出"走同一路径，让 WindowClosing 钩子放行
	appService.PrepareQuitFn = func() { trayQuitRequested.Store(true) }

	// 初始化插件管理器
	pluginsDir := filepath.Join(platform.DefaultDataDir(), "plugins")
	os.MkdirAll(pluginsDir, 0755)
	pluginMgr := plugin.NewManager(pluginsDir)
	appService.PluginMgr = pluginMgr
	appService.PluginHotkeys = services.NewPluginHotkeyRegistry()
	appService.PluginsDir = pluginsDir

	// 注入内置插件自动安装回调（在 ServiceStartup DB 就绪后执行）
	appService.InstallBuiltinPluginsFn = func(mgr *plugin.Manager, database *db.Database) {
		autoInstallBuiltins(mgr, database, &builtinPlugins)
	}

	// 先提取插件文件（确保 system-tools.exe 等最新版本已写入磁盘）
	// 必须早于 DiscoverAndLoad，否则旧版 console 类型 system-tools.exe 会被启动，弹出 CMD 窗口
	extractBuiltinPluginFiles(pluginMgr, &builtinPlugins)

	// 扫描并加载已安装插件（非关键，失败不影响主程序启动）
	pluginMgr.DiscoverAndLoad()

	// 系统通知服务（用于待办定时提醒）
	notifier := notifications.New()

	app := application.New(application.Options{
		Name:        "快启坞",
		Description: "快启坞 QuickDock — 开发者资源集合与快速启动工具",
		Services: []application.Service{
			application.NewService(appService),
			application.NewService(notifier),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		// WebView2 全局优化：减少内存占用 + 设置正确的用户数据路径
		Windows: application.WindowsOptions{
			WebviewUserDataPath:   EnsureConfigDir() + "\\WebView2",
			AdditionalBrowserArgs: memoryOptimizedArgs,
			DisabledFeatures:      disabledFeatures,
		},
	})

	// 传入 App 引用给 AppService
	appService.SetApp(app)

	// 启动本地 AI 流式服务（127.0.0.1 随机端口，供前端 fetch 流式读取）
	appService.StartAIStreamServer()

	// 初始化自动更新器（GitHub Releases Provider）
	if err := initUpdater(app, appVersion); err != nil {
		fmt.Printf("QuickDock: 更新器初始化失败（非关键错误）: %v\n", err)
	}

	// 启动后台定时检查（仅"检查+通知"，下载/重启复用手动检测路径）
	appService.StartAutoUpdateChecker()

	// 注入版本号到 AppService（供前端获取）
	appService.AppVersion = appVersion

	// 注入通知服务引用（供待办提醒调度器使用）
	appService.Notifier = notifier

	// 创建插件窗口管理器（需要 app 引用，只能放在 New 之后）
	appService.PluginWindowMgr = plugin.NewPluginWindowManager(app)

	// 创建主窗口（启动时恢复上次记住的尺寸/位置）
	mainRect := loadWindowState()
	mainOpts := application.WebviewWindowOptions{
		Title:            appTitle,
		Width:            appWidth,
		Height:           appHeight,
		MinWidth:         800,
		MinHeight:        500,
		Frameless:        false,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/",
	}
	if mainRect != nil {
		mainOpts.Width = mainRect.Width
		mainOpts.Height = mainRect.Height
		mainOpts.X = mainRect.X
		mainOpts.Y = mainRect.Y
	}
	mainWindow := app.Window.NewWithOptions(mainOpts)

	// 保存主窗口引用供 tray.go 使用
	SetMainWindow(mainWindow)
	appService.MainWindow = mainWindow

	// 窗口尺寸/位置记忆：移动或缩放结束后（防抖）把矩形写入配置文件，
	// 下次启动 loadWindowState() 恢复。最大化/最小化态不记录，避免恢复成畸形窗口。
	mainWindow.RegisterHook(events.Common.WindowDidResize, func(*application.WindowEvent) {
		scheduleSaveWindowState(mainWindow)
	})
	mainWindow.RegisterHook(events.Common.WindowDidMove, func(*application.WindowEvent) {
		scheduleSaveWindowState(mainWindow)
	})

	// 窗口关闭时隐藏到托盘（而不是退出）
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !trayQuitRequested.Load() {
			windowVisible.Store(false)
			clipboardMode.Store(false)
			event.Cancel()
			go mainWindow.Hide()
		}
	})

	// 同步窗口可见状态
	mainWindow.RegisterHook(events.Common.WindowMinimise, func(event *application.WindowEvent) {
		windowVisible.Store(false)
	})
	mainWindow.RegisterHook(events.Common.WindowRestore, func(event *application.WindowEvent) {
		windowVisible.Store(true)
	})

	// 剪贴板/命令面板/插件窗口使用延迟创建（按需初始化，减少启动内存占用）
	// 将延迟工厂函数注入 AppService，供前端 Wails 绑定调用
	InjectWindowGetters(appService, app)

	// 运行应用
	err := app.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "QuickDock: 应用运行失败: %v\n", err)
		// 不调用 log.Fatal，确保下面的 ShutdownAll 执行
	}

	// 应用退出时停止所有插件、清理 PID 文件、关闭所有插件窗口
	appService.StopAIStreamServer()
	pluginMgr.ShutdownAll()
	if appService.PluginWindowMgr != nil {
		appService.PluginWindowMgr.CloseAll()
	}
}

// updaterProxyFunc 优先使用环境变量代理（HTTP_PROXY/HTTPS_PROXY/NO_PROXY），
// 未配置时回退读取 Windows 系统代理注册表（WinINET）——兼容 Clash/v2rayN 等
// 工具的"系统代理"模式（它们只写注册表，不设环境变量）。
func updaterProxyFunc(req *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(req)
	if u != nil || err != nil {
		return u, err
	}
	return windowsSystemProxy(req)
}

// windowsSystemProxy 读取 HKCU 系统代理注册表（ProxyEnable/ProxyServer）。
// 支持 "host:port" 与 "http=host:port;https=host:port" 两种 ProxyServer 格式。
// 未启用或无法解析时返回 nil（直连）。PAC（AutoConfigURL）不支持，返回直连。
func windowsSystemProxy(req *http.Request) (*url.URL, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return nil, nil
	}
	server = strings.TrimSpace(server)

	// ProxyServer 可能按协议分号分隔（http=...;https=...），此时按请求 scheme 选择
	proxy := server
	if strings.Contains(server, "=") {
		proxy = ""
		for _, part := range strings.Split(server, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), req.URL.Scheme) {
				proxy = strings.TrimSpace(kv[1])
				break
			}
		}
		if proxy == "" {
			return nil, nil
		}
	}

	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, nil
	}
	return u, nil
}

// initUpdater 初始化 Wails 自动更新器（endpoint provider + Ed25519 签名验证）
//
// 安全模型：
//
//	CI 用 Ed25519 私钥（仓库 Secret UPDATER_PRIVATE_KEY）对裸二进制签名，
//	产出 manifest.json（含 sha512 摘要 + ed25519ph 签名），随每个 release 发布。
//	应用经 GitHub 稳定地址 releases/latest/download/manifest.json 拉取该 manifest，
//	用编译期内嵌的公钥（updater.key.pub，见文件顶部 //go:embed）验签——
//	fail closed：签名或摘要不符直接报错、不安装。
//	这能防"发布账号 / CI 被攻破"级别的供应链攻击：攻击者即便替换了 release 资产，
//	没有私钥也伪造不出有效签名，更新会被拒绝。（相较于此前的 SHA256 校验和，
//	校验和只能防传输损坏 / 下错文件，挡不住发布者被冒充。）
func initUpdater(app *application.App, version string) error {
	// 自定义 HTTP 客户端：
	// 1) 沿用 Go 默认传输层各项超时（连接/TLS 握手等）。
	// 2) 代理优先级：环境变量 HTTP_PROXY/HTTPS_PROXY/NO_PROXY 优先；
	//    未配置时回退读取 Windows 系统代理注册表（Clash/v2rayN 等工具的"系统代理"模式
	//    只写 WinINET 注册表、不设环境变量，Go 的 ProxyFromEnvironment 默认读不到）。
	// 3) 不设整体 Timeout（默认 30s 会把大体积安装包下载直接掐断），
	//    真正的超时由调用方传入的 ctx 控制（见 services/update.go）。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = updaterProxyFunc
	httpClient := &http.Client{Transport: transport, Timeout: 0}

	// endpoint provider：从签名 manifest 拉取更新信息并验签。
	// 镜像重试由 NewMirrorUpdaterProvider 包装（国内网络加速），它透传 Check/Download。
	ep, err := endpoint.New(endpoint.Config{
		URL:        "https://github.com/parieses/quickdock/releases/latest/download/manifest.json",
		HTTPClient: httpClient,
	})
	if err != nil {
		return fmt.Errorf("创建 endpoint provider 失败: %w", err)
	}

	// 用镜像 provider 包装：直连 GitHub 下载失败时自动尝试加速镜像（国内网络）。
	// 签名验证不受影响（镜像只改传输 URL，无法篡改内容）。
	provider := updater.Provider(services.NewMirrorUpdaterProvider(ep, httpClient))

	return app.Updater.Init(updater.Config{
		CurrentVersion: version,
		Providers:      []updater.Provider{provider},
		PublicKey:      updaterPublicKey,
		// 不启用 Wails 内置周期检查：它会走 CheckAndInstall 自动下载却永不重启
		// （Restart 仅由 Wails 内置窗口触发，本项目用自定义 UI）。后台"检查+通知"
		// 改由 AppService.StartAutoUpdateChecker 驱动，复用手动检测的同一套 UI 与重启逻辑。
		CheckInterval: 0,
	})
}

// extractBuiltinPluginFiles 增量同步内置插件文件到 ~/.quickdock/plugins/（不含 DB 写入和 LoadPlugin）
// 在 DiscoverAndLoad 之前调用，确保插件二进制文件（如 system-tools.exe）是最新版本。
// 采用增量同步：仅覆盖内容变化的文件，保留插件目录内的本地运行状态（如 sqlite、日志）。
func extractBuiltinPluginFiles(mgr *plugin.Manager, builtinFS *embed.FS) {
	entries, err := builtinFS.ReadDir("plugins/builtin")
	if err != nil {
		fmt.Println("QuickDock: 读取内置插件目录失败:", err)
		return
	}

	// 确保 builtin 共享目录存在，同步 common.css / common.js
	builtinDir := filepath.Join(mgr.PluginsDir(), "builtin")
	os.MkdirAll(builtinDir, 0755)
	for _, name := range []string{"common.css", "common.js"} {
		if data, cer := builtinFS.ReadFile(path.Join("plugins/builtin", name)); cer == nil {
			syncEmbeddedFile(filepath.Join(builtinDir, name), data)
		}
	}

	// 提取共享二进制目录（如 system-tools.exe），embed 中仅保留一份，避免产物膨胀
	sharedDir := filepath.Join(mgr.PluginsDir(), "_shared")
	os.MkdirAll(sharedDir, 0755)
	if err := syncEmbeddedDir(builtinFS, path.Join("plugins/builtin", "_shared"), sharedDir); err != nil {
		fmt.Printf("QuickDock: 同步共享插件资源失败: %v\n", err)
	}

	// 当前仍内置的插件目录名集合（含 _shared，用于清理已删除插件的残留）
	currentBuiltin := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			currentBuiltin[entry.Name()] = true
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "_shared" {
			continue
		}
		pluginID := entry.Name()
		targetDir := filepath.Join(mgr.PluginsDir(), pluginID)

		// 读取 manifest
		manifestPath := path.Join("plugins/builtin", pluginID, "plugin.json")
		data, err := builtinFS.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var mf plugin.PluginManifest
		if err := json.Unmarshal(data, &mf); err != nil {
			continue
		}

		// 增量同步插件文件（不删除本地目录，保留插件运行状态）
		os.MkdirAll(targetDir, 0755)
		if err := syncEmbeddedDir(builtinFS, path.Join("plugins/builtin", pluginID), targetDir); err != nil {
			fmt.Printf("QuickDock: 同步内置插件 %s 失败: %v\n", pluginID, err)
			continue
		}

		// 分发共享二进制到引用它的插件目录（manifest.Backend.Entry 命中 _shared 内文件时）
		if mf.Backend.Entry != "" {
			entryBase := filepath.Base(mf.Backend.Entry)
			if sdata, ser := builtinFS.ReadFile(path.Join("plugins/builtin", "_shared", entryBase)); ser == nil {
				if err := syncEmbeddedFile(filepath.Join(targetDir, entryBase), sdata); err != nil {
					fmt.Printf("QuickDock: 分发共享资源 %s 到 %s 失败: %v\n", entryBase, pluginID, err)
				}
			}
		}

		// 把 common.css / common.js 同步到每个插件根目录
		for _, name := range []string{"common.css", "common.js"} {
			if cd, cer := builtinFS.ReadFile(path.Join("plugins/builtin", name)); cer == nil {
				syncEmbeddedFile(filepath.Join(targetDir, name), cd)
			}
		}
	}

	// 清理已删除内置插件的残留目录：磁盘上仍存在、但已不在嵌入集中、且 ID 属于内置命名空间
	pruneStaleBuiltins(mgr, currentBuiltin)
}

// pruneStaleBuiltins 删除 pluginsDir 下残留的、已不在内置集合中的旧内置插件目录
func pruneStaleBuiltins(mgr *plugin.Manager, currentBuiltin map[string]bool) {
	dirEntries, err := os.ReadDir(mgr.PluginsDir())
	if err != nil {
		return
	}
	for _, e := range dirEntries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "builtin" || currentBuiltin[name] {
			continue
		}
		mfPath := filepath.Join(mgr.PluginsDir(), name, "plugin.json")
		data, rerr := os.ReadFile(mfPath)
		if rerr != nil {
			continue
		}
		var mf plugin.PluginManifest
		if jerr := json.Unmarshal(data, &mf); jerr != nil {
			continue
		}
		// 仅清理内置命名空间，避免误删用户自行安装的插件
		if !strings.HasPrefix(mf.ID, "com.quickdock.") {
			continue
		}
		fmt.Printf("QuickDock: 清理已删除的内置插件 %s (%s)\n", name, mf.ID)
		mgr.UnloadPlugin(mf.ID)
		os.RemoveAll(filepath.Join(mgr.PluginsDir(), name))
	}
}

// autoInstallBuiltins 注册内置插件到数据库并加载（文件已由 extractBuiltinPluginFiles 提取到磁盘）
func autoInstallBuiltins(mgr *plugin.Manager, database *db.Database, builtinFS *embed.FS) {
	entries, err := builtinFS.ReadDir("plugins/builtin")
	if err != nil {
		fmt.Println("QuickDock: 读取内置插件目录失败:", err)
		return
	}

	// 当前有效的内置插件 ID 集合，用于清理残留的数据库记录
	validBuiltinIDs := make(map[string]bool)

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "_shared" {
			continue
		}
		pluginID := entry.Name()
		targetDir := filepath.Join(mgr.PluginsDir(), pluginID)

		// 读取 plugin.json 获取 ID
		data, err := builtinFS.ReadFile(path.Join("plugins/builtin", pluginID, "plugin.json"))
		if err != nil {
			fmt.Printf("QuickDock: 读取内置插件 %s plugin.json 失败: %v\n", pluginID, err)
			continue
		}

		var mf plugin.PluginManifest
		if err := json.Unmarshal(data, &mf); err != nil {
			fmt.Printf("QuickDock: 解析内置插件 %s plugin.json 失败: %v\n", pluginID, err)
			continue
		}

		// 记录当前有效的内置插件 ID，用于清理残留数据库记录
		validBuiltinIDs[mf.ID] = true

		// 目录不存在说明 extractBuiltinPluginFiles 失败，跳过
		if _, err := os.Stat(targetDir); os.IsNotExist(err) {
			fmt.Printf("QuickDock: 内置插件 %s 目录不存在，跳过\n", pluginID)
			continue
		}

		// 读取图标
		iconData := ""
		if mf.Icon != "" {
			iconPath := filepath.Join(targetDir, mf.Icon)
			if icoBytes, err := os.ReadFile(iconPath); err == nil && len(icoBytes) > 0 {
				mime := platform.IconMIME(filepath.Ext(mf.Icon))
				iconData = fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(icoBytes))
			}
		}

		// 写入数据库记录（含 capabilities / permissions / category / icon）
		perms := make(map[string]interface{})
		if mf.Permissions.Network || mf.Permissions.Filesystem || mf.Permissions.Clipboard {
			perms["network"] = mf.Permissions.Network
			perms["filesystem"] = mf.Permissions.Filesystem
			perms["clipboard"] = mf.Permissions.Clipboard
		}
		if err := database.InsertPluginFull(mf.ID, mf.Name, mf.Version, mf.Author, mf.Description, mf.Category, iconData, mf.Capabilities, perms); err != nil {
			fmt.Printf("QuickDock: 内置插件 %s 写入数据库失败: %v\n", pluginID, err)
		}

		// 加载插件（DiscoverAndLoad 已加载运行中实例则跳过，避免 stop→重启 双重加载）
		if inst := mgr.GetPlugin(mf.ID); inst != nil && inst.GetStatus() == "running" {
			fmt.Printf("[plugin %s] %s (%s) 已加载，跳过重复加载\n", mf.ID, mf.Name, mf.Version)
			continue
		}
		if err := mgr.LoadPlugin(mf, targetDir); err != nil {
			fmt.Printf("QuickDock: 加载内置插件 %s 失败: %v\n", pluginID, err)
		} else {
			fmt.Printf("[plugin %s] %s (%s) 已安装并加载\n", mf.ID, mf.Name, mf.Version)
		}
	}

	// 清理已删除内置插件的残留数据库记录（磁盘目录已由 pruneStaleBuiltins 移除，
	// 但历史版本的数据库记录可能残留，需按内置命名空间 + 不在有效集合中删除）
	if allIDs, derr := database.ListAllPluginIDs(); derr == nil {
		for _, id := range allIDs {
			if strings.HasPrefix(id, "com.quickdock.") && !validBuiltinIDs[id] {
				if rerr := database.DeletePlugin(id); rerr != nil {
					fmt.Printf("QuickDock: 删除残留内置插件记录 %s 失败: %v\n", id, rerr)
				} else {
					fmt.Printf("QuickDock: 已删除残留内置插件记录 %s\n", id)
				}
			}
		}
	}
}

// syncEmbeddedDir 将 embed.FS 中的目录增量同步到本地文件系统：
// 仅写入不存在或内容变化的文件，保留本地已有的其他文件（如插件运行状态）
func syncEmbeddedDir(fs *embed.FS, embedPath, targetDir string) error {
	entries, err := fs.ReadDir(embedPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := path.Join(embedPath, entry.Name())
		dstPath := filepath.Join(targetDir, entry.Name())

		if entry.IsDir() {
			os.MkdirAll(dstPath, 0755)
			if err := syncEmbeddedDir(fs, srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		data, err := fs.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := syncEmbeddedFile(dstPath, data); err != nil {
			return err
		}
	}
	return nil
}

// syncEmbeddedFile 写入文件，内容与本地一致时跳过（避免每次启动全量重写磁盘）
func syncEmbeddedFile(dstPath string, data []byte) error {
	if cur, err := os.ReadFile(dstPath); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	return os.WriteFile(dstPath, data, 0644)
}
