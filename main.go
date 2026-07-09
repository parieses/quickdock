package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"

	"quickdock/internal/db"
	"quickdock/internal/plugin"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed all:plugins/builtin
var builtinPlugins embed.FS

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
)

func main() {
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
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	pluginsDir := filepath.Join(homeDir, ".quickdock", "plugins")
	os.MkdirAll(pluginsDir, 0755)
	pluginMgr := plugin.NewManager(pluginsDir)
	appService.PluginMgr = pluginMgr
	appService.PluginHotkeys = services.NewPluginHotkeyRegistry()

	// 注入内置插件自动安装回调（在 ServiceStartup DB 就绪后执行）
	appService.InstallBuiltinPluginsFn = func(mgr *plugin.Manager, database *db.Database) {
		autoInstallBuiltins(mgr, database, &builtinPlugins)
	}

	// 扫描并加载已安装插件（非关键，失败不影响主程序启动）
	pluginMgr.DiscoverAndLoad()

	app := application.New(application.Options{
		Name:        "快启坞",
		Description: "快启坞 QuickDock — 开发者资源集合与快速启动工具",
		Services: []application.Service{
			application.NewService(appService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// 传入 App 引用给 AppService
	appService.SetApp(app)

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

	// 同步窗口可见状态：用户点击最小化/恢复时，让 windowVisible 反映真实状态。
	// 否则“最小化后按热键仍走 Hide 分支、主窗口无法重新打开”的 bug 会发生。
	mainWindow.RegisterHook(events.Common.WindowMinimise, func(event *application.WindowEvent) {
		windowVisible.Store(false)
	})
	mainWindow.RegisterHook(events.Common.WindowRestore, func(event *application.WindowEvent) {
		windowVisible.Store(true)
	})

	// 创建剪贴板独立窗口
	clipboardWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - 剪贴板",
		Width:            clipWinWidth,
		Height:           clipWinHeight,
		Frameless:        true,
		AlwaysOnTop:      true,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/#/clipboard",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})
	clipboardWindow.Hide()
	clipboardWindow.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		clipboardMode.Store(false)
		clipboardWindow.Hide()
	})
	SetClipboardWindow(clipboardWindow)
	appService.ClipboardWindow = clipboardWindow

	// 创建命令面板窗口
	paletteWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - 命令面板",
		Width:            paletteWinWidth,
		Height:           paletteWinHeight,
		Frameless:        true,
		AlwaysOnTop:      true,
		BackgroundColour: application.RGBA{Red: 0, Green: 0, Blue: 0, Alpha: 1},
		URL:              "/#/command-palette",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})
	paletteWindow.Hide()
	paletteWindow.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		paletteMode.Store(false)
		paletteWindow.Hide()
	})
	SetPaletteWindow(paletteWindow)
	appService.PaletteWindow = paletteWindow

	// 创建插件独立窗口（初始隐藏，首次打开时导航到具体插件）
	pluginWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - 插件",
		Width:            800,
		Height:           600,
		MinWidth:         600,
		MinHeight:        400,
		Frameless:        true,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/#/plugin",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})
	pluginWindow.Hide()
	pluginWindow.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		pluginWindow.Hide()
		event.Cancel()
	})
	SetPluginWindow(pluginWindow)
	appService.PluginWindow = pluginWindow

	// 运行应用
	err = app.Run()
	if err != nil {
		log.Fatal(err)
	}

	// 应用退出时停止所有插件并清理 PID 文件
	pluginMgr.ShutdownAll()
}

// autoInstallBuiltins 提取内置插件到 ~/.quickdock/plugins/（启动时自动执行）
func autoInstallBuiltins(mgr *plugin.Manager, database *db.Database, builtinFS *embed.FS) {
	entries, err := builtinFS.ReadDir("plugins/builtin")
	if err != nil {
		fmt.Println("QuickDock: 读取内置插件目录失败:", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginID := entry.Name()

		// 检查是否已安装
		targetDir := filepath.Join(mgr.PluginsDir(), pluginID)
		if _, err := os.Stat(targetDir); err == nil {
			continue // 已安装，跳过
		}

		// 读取 plugin.json
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

		// 创建目标目录
		os.MkdirAll(targetDir, 0755)

		// 提取所有文件
		err = extractEmbeddedDir(builtinFS, path.Join("plugins/builtin", pluginID), targetDir)
		if err != nil {
			fmt.Printf("QuickDock: 提取内置插件 %s 失败: %v\n", pluginID, err)
			os.RemoveAll(targetDir)
			continue
		}

		// 写入数据库记录（含 capabilities / permissions / category）
		perms := make(map[string]interface{})
		if mf.Permissions.Network || mf.Permissions.Filesystem || mf.Permissions.Clipboard {
			perms["network"] = mf.Permissions.Network
			perms["filesystem"] = mf.Permissions.Filesystem
			perms["clipboard"] = mf.Permissions.Clipboard
		}
		if err := database.InsertPluginFull(mf.ID, mf.Name, mf.Version, mf.Author, mf.Description, mf.Category, mf.Capabilities, perms); err != nil {
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
