package main

import (
	"os"
	"sync"
	"unsafe"

	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	moduser32   = windows.NewLazySystemDLL("user32.dll")
)

// instanceMutexName 单实例锁名称，按构建标签区分：
//   - production 标签（wails3 build 正式版）→ "Local\\QuickDock-Instance"
//   - 无 production 标签（wails3 dev / 普通 go build）→ "Local\\QuickDock-Instance-Dev"
// 这样正式版与开发版各自独立加锁，互不抢占，可同时运行。
// 具体取值在 windows_prod.go / windows_dev.go 中定义。
var instanceMutexName string

// ensureSingleInstance 检查是否已有 QuickDock 实例在运行。
// 如果已有实例，将其窗口提到前台并返回 true（主函数应退出）；
// 否则返回 false 继续启动。
func ensureSingleInstance() bool {
	createMutex := modkernel32.NewProc("CreateMutexW")
	mutexName, _ := windows.UTF16PtrFromString(instanceMutexName)

	ret, _, err := createMutex.Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if ret == 0 {
		// 创建互斥体失败，放行（主程序继续启动）
		return false
	}

	// 检查是否已经存在
	if err == windows.ERROR_ALREADY_EXISTS {
		// 已有实例运行，找到它的主窗口并提到前台
		className, _ := windows.UTF16PtrFromString("Chrome_WidgetWin_0") // WebView2 窗口类
		findWindow := moduser32.NewProc("FindWindowW")
		hwnd, _, _ := findWindow.Call(uintptr(unsafe.Pointer(className)), 0)

		if hwnd != 0 {
			showWindow := moduser32.NewProc("ShowWindow")
			showWindow.Call(hwnd, 9)      // SW_RESTORE
			setFg := moduser32.NewProc("SetForegroundWindow")
			setFg.Call(hwnd)
		} else {
			// 按标题搜索作为备选
			title, _ := windows.UTF16PtrFromString("快启坞 QuickDock")
			hwnd, _, _ = findWindow.Call(0, uintptr(unsafe.Pointer(title)))
			if hwnd != 0 {
				showWindow := moduser32.NewProc("ShowWindow")
				showWindow.Call(hwnd, 9)
				setFg := moduser32.NewProc("SetForegroundWindow")
				setFg.Call(hwnd)
			}
		}
		return true
	}

	// 首次启动，互斥体句柄会在进程退出时自动关闭
	return false
}

// clipboardWinLock 保护剪贴板窗口的懒创建（与 paletteWinLock 同模式）
var clipboardWinLock sync.Mutex

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

// initNoteWindow 创建笔记独立窗口（延迟初始化，独立于剪贴板/命令面板）
func initNoteWindow(app *application.App) *application.WebviewWindow {
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - 笔记",
		Width:            clipWinWidth,
		Height:           clipWinHeight,
		Frameless:        true,
		AlwaysOnTop:      true,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/#/note",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		noteMode.Store(false)
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

// InjectWindowGetters 将延迟窗口创建函数注入到 AppService（由 main.go 调用）
// 剪贴板/命令面板窗口均延迟创建，确保都在 app.Run() 之后初始化 WebView2 运行时，
// 避免在主窗口之前预创建导致次级窗口白屏（Wails v3 已知约束）。
func InjectWindowGetters(svc *services.AppService, app *application.App) {
	svc.GetClipboardWindow = clipboardWindowGetter(app)
	svc.GetPaletteWindow = paletteWindowGetter(app)
}

// ===== 热键回调用的窗口 getter =====

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
	dir := platform.DefaultConfigDir()
	os.MkdirAll(dir, 0755)
	return dir
}
