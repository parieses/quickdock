package main

import (
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

var (
	modkernel32  = windows.NewLazySystemDLL("kernel32.dll")
	moduser32    = windows.NewLazySystemDLL("user32.dll")
	procGetWindow = moduser32.NewProc("GetWindow")
)

// foregroundIsOwnedModal 判断当前前台窗口是否为被本应用窗口拥有的模态对话框
// （例如命令面板内插件 <input type="file"> 弹出的系统文件选择框）。
// 命令面板等窗口在失焦时会隐藏自身；若失焦是自家模态框打开所致，则不应隐藏，
// 否则会出现"在插件里选文件时页面被关掉"的问题。
func foregroundIsOwnedModal() bool {
	hwnd := windows.GetForegroundWindow()
	if hwnd == 0 {
		return false
	}
	// GW_OWNER = 4 / GW_PARENT = 1：取窗口的拥有者/父窗口。
	// 顶层无主窗口（如其它应用窗口、本应用主窗口）两者均为 0；
	// 被本窗口拥有的模态对话框（如系统文件选择框，WebView2 可能以 owner 或 child 形式挂载）返回非零。
	owner, _, _ := procGetWindow.Call(uintptr(hwnd), 4)
	if owner != 0 {
		return true
	}
	parent, _, _ := procGetWindow.Call(uintptr(hwnd), 1)
	return parent != 0
}

// instanceMutexName 单实例锁名称。
// 开发版与正式版共用同一数据库（~/.quickdock），因此共用同一把锁，
// 保证同一机器上只运行一个 QuickDock 实例，避免多进程同时写同一 SQLite 库。
var instanceMutexName = "Local\\QuickDock-Instance"

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
		// 已有实例运行，找到它的主窗口并提到前台。
		// 注意：不能只用 FindWindowW("Chrome_WidgetWin_0")—— 该窗口类被大量其它
		// Chromium/WebView2/Electron 应用共用，直接命中会把无关应用错误顶到前台
		//（尤其本应用常隐藏到托盘时）。因此用 EnumWindows + 类名 + 标题双重匹配
		// 精确定位到 QuickDock 自己的窗口。
		if hwnd := findQuickDockWindow(); hwnd != 0 {
			showWindow := moduser32.NewProc("ShowWindow")
			showWindow.Call(hwnd, 9)      // SW_RESTORE
			setFg := moduser32.NewProc("SetForegroundWindow")
			setFg.Call(hwnd)
		}
		return true
	}

	// 首次启动，互斥体句柄会在进程退出时自动关闭
	return false
}

// findQuickDockWindow 使用的 Win32 API proc（缓存，避免每次调用 NewProc）
var procEnumWindows = moduser32.NewProc("EnumWindows")
var procGetWindowTextW = moduser32.NewProc("GetWindowTextW")
var procGetClassNameW = moduser32.NewProc("GetClassNameW")

// findQuickDockWindow 遍历所有顶层窗口，返回第一个「类名为常见 WebView2/Chromium 类
// 且标题包含 QuickDock/快启坞」的窗口句柄；找不到返回 0。
func findQuickDockWindow() uintptr {
	var found uintptr
	// EnumWindows 回调（syscall.NewCallback 需要保持存活到调用结束）
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		// 过滤窗口类：仅接受 WebView2/Chromium 使用的类名
		var cls [64]uint16
		r1, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
		if r1 == 0 {
			return 1 // 继续枚举
		}
		className := windows.UTF16ToString(cls[:])
		if className != "Chrome_WidgetWin_0" && className != "Chrome_WidgetWin_1" {
			return 1
		}
		// 标题必须包含本应用标识，避免把其它 Chromium/WebView2 应用误判为自家窗口
		var title [256]uint16
		r2, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		if r2 == 0 {
			return 1
		}
		t := windows.UTF16ToString(title[:])
		if strings.Contains(t, "QuickDock") || strings.Contains(t, "快启坞") {
			found = hwnd
			return 0 // 停止枚举
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// clipboardWinLock 保护剪贴板窗口的懒创建（与 paletteWinLock 同模式）
var clipboardWinLock sync.Mutex

// ===== WebView2 优化配置（全局，所有窗口共享）=====

// memoryOptimizedArgs 减少 WebView2 内存/进程数量的 Chromium 标志
// 这些标志传递给全局 WebView2 浏览器进程，影响所有窗口。
//
// 注意：
//  - 不传 --disable-renderer-backgrounding：让 WebView2 在 PutIsVisible(false) 时
//    自动释放渲染/GPU 资源（后台窗口降级）。
//  - --in-process-gpu：把 GPU 进程合并进浏览器进程，省掉一个独立子进程。
//  - --renderer-process-limit=N：限制渲染进程数量，主窗口 + 剪贴板/命令面板等
//    小窗尽量共用渲染进程，显著减少任务管理器里的 msedgewebview2 进程数，
//    同时保留 WebView2 的站点/进程隔离（不用破坏性的 --single-process）。
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
	"--in-process-gpu",
	"--renderer-process-limit=4",
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
		if foregroundIsOwnedModal() {
			return // 自家模态对话框（文件选择框等）导致失焦，不隐藏
		}
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

// noteWindowGetter 返回 AppService 使用的快捷笔记窗口 getter
// 由 main.go 注入到 appService.GetNoteWindow
func noteWindowGetter(app *application.App) func() *application.WebviewWindow {
	return func() *application.WebviewWindow {
		noteWinLock.Lock()
		defer noteWinLock.Unlock()
		if noteWin == nil {
			if app == nil {
				return nil
			}
			noteWin = initNoteWindow(app)
		}
		return noteWin
	}
}

// InjectWindowGetters 将延迟窗口创建函数注入到 AppService（由 main.go 调用）
// 剪贴板/命令面板窗口均延迟创建，确保都在 app.Run() 之后初始化 WebView2 运行时，
// 避免在主窗口之前预创建导致次级窗口白屏（Wails v3 已知约束）。
func InjectWindowGetters(svc *services.AppService, app *application.App) {
	svc.GetClipboardWindow = clipboardWindowGetter(app)
	svc.GetPaletteWindow = paletteWindowGetter(app)
	svc.GetNoteWindow = noteWindowGetter(app)
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
