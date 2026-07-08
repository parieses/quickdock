package main

import (
	"embed"
	"log"
	"sync/atomic"

	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	appTitle         = "快启坞 QuickDock"
	appWidth         = 1100
	appHeight        = 700
	clipWinWidth     = 480
	clipWinHeight    = 420
	paletteWinWidth  = 620
	paletteWinHeight = 480
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
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
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

	// 运行应用
	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
