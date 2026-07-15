package main

import (
	"os"

	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// ===== WebView2 优化配置（全局，所有窗口共享）=====

// memoryOptimizedArgs 减少 WebView2 内存占用的 Chromium 标志
// 这些标志传递给全局 WebView2 浏览器进程，影响所有窗口
// 注意：不传 --disable-renderer-backgrounding，让 WebView2 在
// PutIsVisible(false) 时自动释放渲染/GPU 资源（后台窗口降级）
var memoryOptimizedArgs = []string{
	"--disable-features=msSmartScreenProtection,Printing,Translate,ReadingList,MediaSessionService,NotificationService,PasswordManager,ChromeWhatsNewUI",
	"--disable-sync",
	"--disable-background-networking",
	"--disable-background-timer-throttling",
	"--disable-extensions",
	"--disable-component-update",
	"--disable-default-apps",
	"--mute-audio",
	"--autoplay-policy=user-gesture-required",
	"--js-flags=--max_old_space_size=64",
	"--renderer-process-limit=2",
}

// disabledFeatures 禁用的 Chromium 特性
var disabledFeatures = []string{
	"msSmartScreenProtection",
	"Printing",
	"Translate",
	"ReadingList",
}

// ===== 延迟窗口工厂 =====

// initClipboardWindow 创建剪贴板独立窗口（延迟初始化）
func initClipboardWindow(app *application.App) *application.WebviewWindow {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
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
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		clipboardMode.Store(false)
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		win.Hide()
	})
	return win
}

// initPaletteWindow 创建命令面板独立窗口（延迟初始化）
func initPaletteWindow(app *application.App) *application.WebviewWindow {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
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
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		paletteMode.Store(false)
		win.Hide()
	})
	return win
}

// ===== AppService 注入工厂函数 =====

// clipboardWindowGetter 返回 AppService 使用的剪贴板窗口 getter
// 由 main.go 注入到 appService.GetClipboardWindow
func clipboardWindowGetter(app *application.App) func() *application.WebviewWindow {
	return func() *application.WebviewWindow {
		clipboardWinLock.Lock()
		defer clipboardWinLock.Unlock()
		if clipboardWin == nil {
			if app == nil {
				return nil
			}
			clipboardWin = initClipboardWindow(app)
		}
		return clipboardWin
	}
}

// paletteWindowGetter 返回 AppService 使用的命令面板窗口 getter
func paletteWindowGetter(app *application.App) func() *application.WebviewWindow {
	return func() *application.WebviewWindow {
		paletteWinLock.Lock()
		defer paletteWinLock.Unlock()
		if paletteWin == nil {
			if app == nil {
				return nil
			}
			paletteWin = initPaletteWindow(app)
		}
		return paletteWin
	}
}

// InjectWindowGetters 将延迟窗口创建函数注入到 AppService（由 main.go 调用）
func InjectWindowGetters(svc *services.AppService, app *application.App) {
	svc.GetClipboardWindow = clipboardWindowGetter(app)
	svc.GetPaletteWindow = paletteWindowGetter(app)
}

// ===== 热键回调用的窗口 getter（重写 tray.go 中的简单 getter）=====

func getClipboardWindow() *application.WebviewWindow {
	clipboardWinLock.Lock()
	defer clipboardWinLock.Unlock()
	if clipboardWin == nil {
		app := getHotkeyApp()
		if app == nil {
			return nil
		}
		clipboardWin = initClipboardWindow(app)
	}
	return clipboardWin
}

func getPaletteWindow() *application.WebviewWindow {
	paletteWinLock.Lock()
	defer paletteWinLock.Unlock()
	if paletteWin == nil {
		app := getHotkeyApp()
		if app == nil {
			return nil
		}
		paletteWin = initPaletteWindow(app)
	}
	return paletteWin
}

// EnsureConfigDir 确保配置目录存在（用于 WebviewUserDataPath）
func EnsureConfigDir() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = os.Getenv("LOCALAPPDATA")
	}
	dir := appData + "\\QuickDock"
	os.MkdirAll(dir, 0755)
	return dir
}
