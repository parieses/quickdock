package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/platform"
	"quickdock/internal/plugin"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

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
	clipWinHeight    = 420
	paletteWinWidth  = 680
	paletteWinHeight = 460
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

	// 注入热键监听回调（避免循环依赖）
	appService.StartHotkeyListenerFn = StartHotkeyListener
	appService.SuspendHotkeysFn = SuspendHotkeys
	appService.ResumeHotkeysFn = ResumeHotkeys

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
			WebviewUserDataPath: EnsureConfigDir() + "\\WebView2",
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

	// 注入版本号到 AppService（供前端获取）
	appService.AppVersion = appVersion

	// 注入通知服务引用（供待办提醒调度器使用）
	appService.Notifier = notifier

	// 创建插件窗口管理器（需要 app 引用，只能放在 New 之后）
	appService.PluginWindowMgr = plugin.NewPluginWindowManager(app)

	// 创建主窗口
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            appTitle,
		Width:            appWidth,
		Height:           appHeight,
		MinWidth:         800,
		MinHeight:        500,
		Frameless:        false,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/",
	})

	// 保存主窗口引用供 tray.go 使用
	SetMainWindow(mainWindow)
	appService.MainWindow = mainWindow

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

// initUpdater 初始化 Wails 自动更新器（使用 GitHub Releases + Ed25519 签名验证）
func initUpdater(app *application.App, version string) error {
	// 自定义 AssetMatcher：先试默认规则（文件名含 platform+arch），
	// 不匹配时补充检查 .exe 后缀，支持 quickdock-amd64-installer.exe 这类不含"windows"的命名。
	assetMatcher := func(req updater.CheckRequest, assets []github.ReleaseAsset) int {
		idx := github.DefaultAssetMatcher(req, assets)
		if idx >= 0 {
			return idx
		}
		// 对于 windows/amd64，文件名含 "amd64" 且以 .exe 结尾即为匹配
		if req.Platform == "windows" && req.Arch == "amd64" {
			for i, a := range assets {
				name := strings.ToLower(a.Name)
				if strings.HasSuffix(name, ".exe") && strings.Contains(name, "amd64") {
					return i
				}
			}
		}
		return -1
	}

	gh, err := github.New(github.Config{
		Repository:    "parieses/quickdock",
		AssetMatcher:  assetMatcher,
	})
	if err != nil {
		return fmt.Errorf("创建 GitHub provider 失败: %w", err)
	}

	return app.Updater.Init(updater.Config{
		CurrentVersion: version,
		Providers:      []updater.Provider{gh},
		PublicKey:      updaterPublicKey,
		CheckInterval:  24 * time.Hour, // 每 24 小时后台自动检查
	})
}

// extractBuiltinPluginFiles 提取内置插件文件到 ~/.quickdock/plugins/（不含 DB 写入和 LoadPlugin）
// 在 DiscoverAndLoad 之前调用，确保插件二进制文件（如 system-tools.exe）是最新版本
func extractBuiltinPluginFiles(mgr *plugin.Manager, builtinFS *embed.FS) {
	entries, err := builtinFS.ReadDir("plugins/builtin")
	if err != nil {
		fmt.Println("QuickDock: 读取内置插件目录失败:", err)
		return
	}

	// 确保 builtin 共享目录存在，提取 common.css / common.js
	builtinDir := filepath.Join(mgr.PluginsDir(), "builtin")
	os.MkdirAll(builtinDir, 0755)
	for _, name := range []string{"common.css", "common.js"} {
		if data, cer := builtinFS.ReadFile(path.Join("plugins/builtin", name)); cer == nil {
			os.WriteFile(filepath.Join(builtinDir, name), data, 0644)
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginID := entry.Name()
		targetDir := filepath.Join(mgr.PluginsDir(), pluginID)

		// 读取 manifest 获取 ID（用于卸载旧实例）
		manifestPath := path.Join("plugins/builtin", pluginID, "plugin.json")
		data, err := builtinFS.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var mf plugin.PluginManifest
		if err := json.Unmarshal(data, &mf); err != nil {
			continue
		}

		// 卸载旧实例 + 删除旧目录
		mgr.UnloadPlugin(pluginID)
		mgr.UnloadPlugin(mf.ID)
		os.RemoveAll(targetDir)

		// 提取新文件
		os.MkdirAll(targetDir, 0755)
		if err := extractEmbeddedDir(builtinFS, path.Join("plugins/builtin", pluginID), targetDir); err != nil {
			fmt.Printf("QuickDock: 提取内置插件 %s 失败: %v\n", pluginID, err)
			os.RemoveAll(targetDir)
			continue
		}

		// 把 common.css / common.js 拷贝到每个插件根目录
		for _, name := range []string{"common.css", "common.js"} {
			if cd, cer := builtinFS.ReadFile(path.Join("plugins/builtin", name)); cer == nil {
				os.WriteFile(filepath.Join(targetDir, name), cd, 0644)
			}
		}
	}
}

// autoInstallBuiltins 提取内置插件到 ~/.quickdock/plugins/（启动时自动执行）
func autoInstallBuiltins(mgr *plugin.Manager, database *db.Database, builtinFS *embed.FS) {
	entries, err := builtinFS.ReadDir("plugins/builtin")
	if err != nil {
		fmt.Println("QuickDock: 读取内置插件目录失败:", err)
		return
	}

	// 确保 builtin 共享目录存在，提取 common.css（所有插件共享的主题样式）
	builtinDir := filepath.Join(mgr.PluginsDir(), "builtin")
	os.MkdirAll(builtinDir, 0755)
	commonCSSData, err := builtinFS.ReadFile("plugins/builtin/common.css")
	if err == nil {
		os.WriteFile(filepath.Join(builtinDir, "common.css"), commonCSSData, 0644)
	} else {
		fmt.Println("QuickDock: 读取 common.css 失败:", err)
	}
	// 提取 common.js（所有插件共享的前端工具函数，由后端注入到插件页面）
	commonJSData, err := builtinFS.ReadFile("plugins/builtin/common.js")
	if err == nil {
		os.WriteFile(filepath.Join(builtinDir, "common.js"), commonJSData, 0644)
	} else {
		fmt.Println("QuickDock: 读取 common.js 失败:", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginID := entry.Name()
		targetDir := filepath.Join(mgr.PluginsDir(), pluginID)

		// 先读取 embedded plugin.json 获取插件 ID（用于卸载旧实例）
		manifestPath := path.Join("plugins/builtin", pluginID, "plugin.json")
		data, err := builtinFS.ReadFile(manifestPath)
		if err != nil {
			fmt.Printf("QuickDock: 读取内置插件 %s plugin.json 失败: %v\n", pluginID, err)
			continue
		}

		var mf plugin.PluginManifest
		if err := json.Unmarshal(data, &mf); err != nil {
			fmt.Printf("QuickDock: 解析内置插件 %s plugin.json 失败: %v\n", pluginID, err)
			continue
		}

		// 检查是否已安装
		if _, err := os.Stat(targetDir); err == nil {
			// 用 manifest.ID 卸载旧实例（兼容不同 ID 格式）
			mgr.UnloadPlugin(pluginID)      // 也按目录名尝试卸载
			mgr.UnloadPlugin(mf.ID)         // 按新 ID 卸载
			os.RemoveAll(targetDir)
		}

		// 创建目标目录
		os.MkdirAll(targetDir, 0755)

		// 提取所有文件
		err = extractEmbeddedDir(builtinFS, path.Join("plugins/builtin", pluginID), targetDir)
		if err != nil {
			fmt.Printf("QuickDock: 提取内置插件 %s 失败: %v\n", pluginID, err)
			os.RemoveAll(targetDir)
			continue
		}

		// 把 common.css 和 common.js 拷贝到每个插件根目录，
		// 确保插件 HTML 中的 <link href="../common.css"> 在文件系统层面也能正确解析
		for _, name := range []string{"common.css", "common.js"} {
			if data, cer := builtinFS.ReadFile(path.Join("plugins/builtin", name)); cer == nil {
				os.WriteFile(filepath.Join(targetDir, name), data, 0644)
			}
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

		// 加载插件
		if err := mgr.LoadPlugin(mf, targetDir); err != nil {
			fmt.Printf("QuickDock: 加载内置插件 %s 失败: %v\n", pluginID, err)
		} else {
			fmt.Printf("QuickDock: 内置插件 %s (%s) 已安装并加载\n", mf.Name, mf.Version)
		}
	}
}

// extractEmbeddedDir 将 embed.FS 中的目录提取到本地文件系统
func extractEmbeddedDir(fs *embed.FS, embedPath, targetDir string) error {
	entries, err := fs.ReadDir(embedPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := path.Join(embedPath, entry.Name())
		dstPath := filepath.Join(targetDir, entry.Name())

		if entry.IsDir() {
			os.MkdirAll(dstPath, 0755)
			if err := extractEmbeddedDir(fs, srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		data, err := fs.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
