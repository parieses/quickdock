package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Windows API 常量
const (
	MOD_ALT          = 0x0001
	MOD_CONTROL      = 0x0002
	MOD_SHIFT        = 0x0004
	VK_SPACE         = 0x20
	VK_OEM_3         = 0xC0
	WM_DESTROY       = 0x0002
	WM_COMMAND       = 0x0111
	WS_EX_TOOLWINDOW = 0x00000080
	WS_POPUP         = 0x80000000
	WM_TRAYICON      = 0x0400 + 100
	NIM_ADD          = 0
	NIM_DELETE       = 2
	NIF_MESSAGE      = 1
	NIF_ICON         = 2
	NIF_TIP          = 4
	WM_LBUTTONUP     = 0x0202
	WM_RBUTTONUP     = 0x0205
	WM_LBUTTONDBLCLK = 0x0203
	IMAGE_ICON       = 1
	LR_LOADFROMFILE  = 0x0010
	LR_DEFAULTSIZE   = 0x0040

	WM_DRAWCLIPBOARD = 0x0308
	WM_CHANGECBCHAIN = 0x030D
	CF_TEXT          = 1
	CF_DIB           = 8
	CF_HDROP         = 15
	CF_UNICODETEXT   = 13
)

//go:embed build/tray.ico
var trayIcoEmbed []byte

// 全局状态（tray/message loop 专用）
var (
	hotkeyApp         *application.App
	hotkeyAppLock     sync.Mutex
	trayHICON         uintptr
	trayQuitRequested atomic.Bool
	trayRemoved       atomic.Bool

	mainWin          *application.WebviewWindow
	mainWinLock      sync.Mutex
	clipboardWin     *application.WebviewWindow
	clipboardWinLock sync.Mutex
	nextClipboardViewer uintptr

	// 全局服务引用（供 windowProc 回调使用）
	appSvc *services.AppService

	// 当前注册的 GlobalShortcut 加速器
	currentAppAccel     string
	currentClipAccel    string
	currentPaletteAccel string
	accelMu             sync.Mutex

	paletteWin     *application.WebviewWindow
	paletteWinLock sync.Mutex

	pluginWin     *application.WebviewWindow
	pluginWinLock sync.Mutex
)

func SetClipboardWindow(win *application.WebviewWindow) {
	clipboardWinLock.Lock()
	defer clipboardWinLock.Unlock()
	clipboardWin = win
}

func getClipboardWindow() *application.WebviewWindow {
	clipboardWinLock.Lock()
	defer clipboardWinLock.Unlock()
	return clipboardWin
}

func SetPaletteWindow(win *application.WebviewWindow) {
	paletteWinLock.Lock()
	defer paletteWinLock.Unlock()
	paletteWin = win
}

func getPaletteWindow() *application.WebviewWindow {
	paletteWinLock.Lock()
	defer paletteWinLock.Unlock()
	return paletteWin
}

// ---- 插件窗口 ----

func SetPluginWindow(win *application.WebviewWindow) {
	pluginWinLock.Lock()
	defer pluginWinLock.Unlock()
	pluginWin = win
}

func getPluginWindow() *application.WebviewWindow {
	pluginWinLock.Lock()
	defer pluginWinLock.Unlock()
	return pluginWin
}

type NOTIFYICONDATAW struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

func SetHotkeyApp(app *application.App) {
	hotkeyAppLock.Lock()
	defer hotkeyAppLock.Unlock()
	hotkeyApp = app
}

func getHotkeyApp() *application.App {
	hotkeyAppLock.Lock()
	defer hotkeyAppLock.Unlock()
	return hotkeyApp
}

func SetMainWindow(win *application.WebviewWindow) {
	mainWinLock.Lock()
	defer mainWinLock.Unlock()
	mainWin = win
}

func GetMainWindow() *application.WebviewWindow {
	mainWinLock.Lock()
	defer mainWinLock.Unlock()
	return mainWin
}

// showMainWindow 显示主窗口：若处于最小化状态则先恢复，并定位到鼠标所在屏幕。
func showMainWindow(win *application.WebviewWindow) {
	if win.IsMinimised() {
		win.Restore()
	}
	platform.SetWindowToCursorScreen(win, appWidth, appHeight)
	win.Show()
	windowVisible.Store(true)
}

// hideMainWindow 隐藏主窗口并同步状态标志。
func hideMainWindow(win *application.WebviewWindow) {
	win.Hide()
	windowVisible.Store(false)
	clipboardMode.Store(false)
}

// toggleMainWindow 切换主窗口显隐，供热键/托盘回调统一调用。
func toggleMainWindow() {
	win := GetMainWindow()
	if win == nil {
		return
	}
	if windowVisible.Load() {
		hideMainWindow(win)
	} else {
		showMainWindow(win)
	}
}

// StartHotkeyListener 启动热键和托盘（由 services.ServiceStartup 回调）
func StartHotkeyListener(app *application.App, svc *services.AppService) {
	if goruntime.GOOS != "windows" {
		return
	}

	SetHotkeyApp(app)
	appSvc = svc

	trayHICON = loadIconFromEmbed()
	if trayHICON == 0 {
		fmt.Println("QuickDock: 加载托盘图标失败，使用默认图标")
	}

	go runMessageLoop()
}

func loadIconFromEmbed() uintptr {
	if len(trayIcoEmbed) < 6+16 {
		return 0
	}

	user32 := syscall.NewLazyDLL("user32.dll")
	count := int(trayIcoEmbed[4]) | int(trayIcoEmbed[5])<<8
	if count == 0 {
		return 0
	}

	bestIdx := 0
	bestSize := 0
	for i := 0; i < count; i++ {
		entryOffset := 6 + i*16
		if entryOffset+16 > len(trayIcoEmbed) {
			break
		}
		w := int(trayIcoEmbed[entryOffset])
		h := int(trayIcoEmbed[entryOffset+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if w*h > bestSize {
			bestSize = w * h
			bestIdx = i
		}
	}

	entryOffset := 6 + bestIdx*16
	imageOffset := int(trayIcoEmbed[entryOffset+12]) |
		int(trayIcoEmbed[entryOffset+13])<<8 |
		int(trayIcoEmbed[entryOffset+14])<<16 |
		int(trayIcoEmbed[entryOffset+15])<<24
	imageSize := int(trayIcoEmbed[entryOffset+8]) |
		int(trayIcoEmbed[entryOffset+9])<<8 |
		int(trayIcoEmbed[entryOffset+10])<<16 |
		int(trayIcoEmbed[entryOffset+11])<<24

	if imageOffset+imageSize > len(trayIcoEmbed) || imageSize == 0 {
		return 0
	}

	imageData := trayIcoEmbed[imageOffset : imageOffset+imageSize]
	createIcon := user32.NewProc("CreateIconFromResourceEx")
	hIcon, _, _ := createIcon.Call(
		uintptr(unsafe.Pointer(&imageData[0])),
		uintptr(imageSize),
		1,
		0x00030000,
		0, 0, 0,
	)

	if hIcon == 0 {
		loadIcon := user32.NewProc("LoadIconW")
		hIcon, _, _ = loadIcon.Call(0, uintptr(32512))
	}

	return hIcon
}

// windowProc 隐藏窗口的回调函数
func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		switch lParam {
		case WM_LBUTTONDBLCLK, WM_LBUTTONUP:
			if win := GetMainWindow(); win != nil {
				showMainWindow(win)
			}
		case WM_RBUTTONUP:
			showTrayMenu(hwnd)
		}
		return 0

	case WM_COMMAND:
		switch wParam {
		case 1001:
			if win := GetMainWindow(); win != nil {
				showMainWindow(win)
			}
		case 1003:
			if win := GetMainWindow(); win != nil {
				hideMainWindow(win)
			}
		case 1002:
			removeTrayIcon()
		}
		return 0

	case WM_DESTROY:
		removeTrayIcon()
		postQuitMessage(0)
		return 0

	case WM_DRAWCLIPBOARD:
		if appSvc != nil {
			appSvc.OnClipboardChange()
		}
		if nextClipboardViewer != 0 {
			user32 := syscall.NewLazyDLL("user32.dll")
			postMsg := user32.NewProc("PostMessageW")
			postMsg.Call(nextClipboardViewer, WM_DRAWCLIPBOARD, wParam, lParam)
		}
		return 0

	case WM_CHANGECBCHAIN:
		if wParam == nextClipboardViewer {
			nextClipboardViewer = lParam
		} else if nextClipboardViewer != 0 {
			user32 := syscall.NewLazyDLL("user32.dll")
			postMsg := user32.NewProc("PostMessageW")
			postMsg.Call(nextClipboardViewer, WM_CHANGECBCHAIN, wParam, lParam)
		}
		return 0
	}

	return callDefWindowProc(hwnd, msg, wParam, lParam)
}

func runMessageLoop() {
	goruntime.LockOSThread()

	user32 := syscall.NewLazyDLL("user32.dll")
	className := syscall.StringToUTF16Ptr("QuickDock_Hotkey_Window_v3")

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	hinstance, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)

	wc := struct {
		style         uint32
		lpfnWndProc   uintptr
		cbClsExtra    int32
		cbWndExtra    int32
		hinstance     uintptr
		hIcon         uintptr
		hCursor       uintptr
		hbrBackground uintptr
		lpszMenuName  *uint16
		lpszClassName *uint16
	}{
		style:         0,
		lpfnWndProc:   syscall.NewCallback(windowProc),
		hinstance:     hinstance,
		hCursor:       0,
		lpszClassName: className,
	}

	regClass := user32.NewProc("RegisterClassW")
	_, _, _ = regClass.Call(uintptr(unsafe.Pointer(&wc)))

	createWindow := user32.NewProc("CreateWindowExW")
	hwnd, _, _ := createWindow.Call(
		WS_EX_TOOLWINDOW,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("QuickDockHotkey"))),
		WS_POPUP,
		0, 0, 0, 0,
		0, 0, hinstance, 0,
	)

	if hwnd == 0 {
		fmt.Println("QuickDock: 创建隐藏窗口失败")
		return
	}

	// 将 HWND 存储到 AppService（供剪贴板 API 使用）
	if appSvc != nil {
		appSvc.HiddenHWND.Store(uint64(hwnd))
	}
	fmt.Println("QuickDock: 隐藏窗口已创建")

	// 使用 Wails GlobalShortcut 注册全局快捷键
	app := getHotkeyApp()
	if app != nil {
		registerAppShortcut := func(accel string) error {
			return app.GlobalShortcut.Register(accel, func() {
					toggleMainWindow()
			})
		}
		registerClipShortcut := func(accel string) error {
			return app.GlobalShortcut.Register(accel, func() {
				cw := getClipboardWindow()
				if cw == nil {
					return
				}
				if clipboardMode.Load() {
					clipboardMode.Store(false)
					cw.Hide()
				} else {
					platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
					clipboardMode.Store(true)
					cw.Show()
					cw.Focus()
					if a := getHotkeyApp(); a != nil {
						a.Event.Emit("clipboard:toggle")
					}
				}
			})
		}

		// 从数据库读取热键配置
		appMods, appVk := MOD_CONTROL, int(VK_SPACE)
		clipMods, clipVk := MOD_CONTROL, int(VK_OEM_3)
		if appSvc != nil && appSvc.DB != nil {
			if raw, err := appSvc.DB.GetSetting("hotkey"); err == nil && raw != "" {
				appMods, appVk = parseHotkeySetting(raw)
			}
			if raw, err := appSvc.DB.GetSetting("clipboard_hotkey"); err == nil && raw != "" {
				clipMods, clipVk = parseHotkeySetting(raw)
			}
		}

		appAccel := modVKToAccelerator(appMods, appVk)
		clipAccel := modVKToAccelerator(clipMods, clipVk)

		registeredAppAccel := appAccel
		if err := registerAppShortcut(appAccel); err != nil {
			fmt.Printf("QuickDock: 热键 [%s] 注册失败: %v，回退到 Ctrl+Space\n", appAccel, err)
			registerAppShortcut("Ctrl+Space")
			registeredAppAccel = "Ctrl+Space"
			if appSvc != nil && appSvc.DB != nil {
				appSvc.DB.SetSetting("hotkey", "2,32")
			}
		} else {
			fmt.Printf("QuickDock: 全局快捷键 [%s] 已注册\n", appAccel)
		}

		registeredClipAccel := clipAccel
		if err := registerClipShortcut(clipAccel); err != nil {
			fmt.Printf("QuickDock: 剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+`\n", clipAccel, err)
			registerClipShortcut("Ctrl+`")
			registeredClipAccel = "Ctrl+`"
			if appSvc != nil && appSvc.DB != nil {
				appSvc.DB.SetSetting("clipboard_hotkey", "2,192")
			}
		} else {
			fmt.Printf("QuickDock: 剪贴板快捷键 [%s] 已注册\n", clipAccel)
		}
		setAccelerators(registeredAppAccel, registeredClipAccel, "")

		// 注册命令面板热键（默认 Ctrl+K）
		paletteMods, paletteVk := MOD_CONTROL, int(0x4B)
		if appSvc != nil && appSvc.DB != nil {
			if raw, err := appSvc.DB.GetSetting("palette_hotkey"); err == nil && raw != "" {
				paletteMods, paletteVk = parseHotkeySetting(raw)
			}
		}
		registerPaletteShortcut := func(accel string) error {
			return app.GlobalShortcut.Register(accel, func() {
				pw := getPaletteWindow()
				if pw == nil {
					return
				}
				if paletteMode.Load() {
					paletteMode.Store(false)
					pw.Hide()
				} else {
				platform.SetWindowToCursorScreen(pw, paletteWinWidth, paletteWinHeight)
				paletteMode.Store(true)
				pw.Show()
				pw.Focus()
				if a := getHotkeyApp(); a != nil {
					a.Event.Emit("palette:toggle")
				}
			}
		})
	}
	paletteAccel := modVKToAccelerator(paletteMods, paletteVk)
	registeredPaletteAccel := paletteAccel
	if err := registerPaletteShortcut(paletteAccel); err != nil {
			fmt.Printf("QuickDock: 命令面板热键 [%s] 注册失败: %v\n", paletteAccel, err)
			registerPaletteShortcut("Ctrl+K")
			registeredPaletteAccel = "Ctrl+K"
		} else {
			fmt.Printf("QuickDock: 命令面板快捷键 [%s] 已注册\n", paletteAccel)
		}
		setAccelerators(registeredAppAccel, registeredClipAccel, registeredPaletteAccel)
	}

	createTrayIcon(hwnd)

	setClipboardViewer := user32.NewProc("SetClipboardViewer")
	nextViewer, _, _ := setClipboardViewer.Call(hwnd)
	nextClipboardViewer = nextViewer
	fmt.Println("QuickDock: 剪贴板监听已启动")

	getMessage := user32.NewProc("GetMessageW")

	var msg struct {
		hwnd    uintptr
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      struct{ x, y int32 }
	}

	for {
		ret, _, _ := getMessage.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 {
			break
		}

		translateMessage := user32.NewProc("TranslateMessage")
		dispatchMessage := user32.NewProc("DispatchMessageW")
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	fmt.Println("QuickDock: 消息循环已停止")
}

func createTrayIcon(hwnd uintptr) {
	shell32 := syscall.NewLazyDLL("shell32.dll")

	hIcon := trayHICON
	if hIcon == 0 {
		user32 := syscall.NewLazyDLL("user32.dll")
		loadIcon := user32.NewProc("LoadIconW")
		hIcon, _, _ = loadIcon.Call(0, uintptr(32512))
	}

	nid := &NOTIFYICONDATAW{
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:             hwnd,
		UID:              1,
		UFlags:           NIF_MESSAGE | NIF_ICON | NIF_TIP,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            hIcon,
	}
	copy(nid.SzTip[:], syscall.StringToUTF16("快启坞 QuickDock"))

	shellNotifyIcon := shell32.NewProc("Shell_NotifyIconW")
	ret, _, _ := shellNotifyIcon.Call(NIM_ADD, uintptr(unsafe.Pointer(nid)))
	if ret != 0 {
		fmt.Println("QuickDock: 系统托盘图标已创建")
	} else {
		fmt.Println("QuickDock: 系统托盘图标创建失败")
	}
}

func removeTrayIcon() {
	if trayRemoved.Swap(true) {
		return
	}

	// 离开剪贴板监听链
	if hwnd := uintptr(appSvc.HiddenHWND.Load()); hwnd != 0 && nextClipboardViewer != 0 {
		user32 := syscall.NewLazyDLL("user32.dll")
		changeChain := user32.NewProc("ChangeClipboardChain")
		changeChain.Call(hwnd, nextClipboardViewer)
		fmt.Println("QuickDock: 已离开剪贴板监听链")
	}

	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellNotifyIcon := shell32.NewProc("Shell_NotifyIconW")
	nid := &NOTIFYICONDATAW{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:   uintptr(appSvc.HiddenHWND.Load()),
		UID:    1,
	}
	shellNotifyIcon.Call(NIM_DELETE, uintptr(unsafe.Pointer(nid)))
	fmt.Println("QuickDock: 系统托盘图标已移除")

	if trayHICON != 0 {
		user32 := syscall.NewLazyDLL("user32.dll")
		destroyIcon := user32.NewProc("DestroyIcon")
		destroyIcon.Call(trayHICON)
		trayHICON = 0
	}

	trayQuitRequested.Store(true)
	if app := getHotkeyApp(); app != nil {
		app.Quit()
	} else {
		os.Exit(0)
	}
}

func showTrayMenu(hwnd uintptr) {
	user32 := syscall.NewLazyDLL("user32.dll")

	createPopupMenu := user32.NewProc("CreatePopupMenu")
	hMenu, _, _ := createPopupMenu.Call()

	appendMenu := user32.NewProc("AppendMenuW")
	appendMenu.Call(hMenu, 0, 1001, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("显示窗口"))))
	appendMenu.Call(hMenu, 0, 1003, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("隐藏窗口"))))
	appendMenu.Call(hMenu, 0x800, 0, 0)
	appendMenu.Call(hMenu, 0, 1002, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("退出"))))

	getCursorPos := user32.NewProc("GetCursorPos")
	var pt struct{ x, y int32 }
	getCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	setForegroundWindow := user32.NewProc("SetForegroundWindow")
	setForegroundWindow.Call(hwnd)

	trackPopupMenu := user32.NewProc("TrackPopupMenu")
	trackPopupMenu.Call(hMenu, 0, uintptr(pt.x), uintptr(pt.y), 0, hwnd, 0)

	postMessage := user32.NewProc("PostMessageW")
	postMessage.Call(hwnd, 0x0100, 0, 0)

	destroyMenu := user32.NewProc("DestroyMenu")
	destroyMenu.Call(hMenu)
}

func callDefWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	user32 := syscall.NewLazyDLL("user32.dll")
	defProc := user32.NewProc("DefWindowProcW")
	ret, _, _ := defProc.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func postQuitMessage(exitCode int32) {
	user32 := syscall.NewLazyDLL("user32.dll")
	postQuit := user32.NewProc("PostQuitMessage")
	postQuit.Call(uintptr(exitCode))
}

// ===== GlobalShortcut 加速器 =====

func setAccelerators(appAccel, clipAccel, paletteAccel string) {
	accelMu.Lock()
	defer accelMu.Unlock()
	currentAppAccel = appAccel
	currentClipAccel = clipAccel
	currentPaletteAccel = paletteAccel
}

func getAppAccel() string {
	accelMu.Lock()
	defer accelMu.Unlock()
	return currentAppAccel
}

func getClipAccel() string {
	accelMu.Lock()
	defer accelMu.Unlock()
	return currentClipAccel
}

func getPaletteAccel() string {
	accelMu.Lock()
	defer accelMu.Unlock()
	return currentPaletteAccel
}

func modVKToAccelerator(modifiers, vk int) string {
	var parts []string
	if modifiers&MOD_ALT != 0 {
		parts = append(parts, "Alt")
	}
	if modifiers&MOD_CONTROL != 0 {
		parts = append(parts, "Ctrl")
	}
	if modifiers&MOD_SHIFT != 0 {
		parts = append(parts, "Shift")
	}
	if modifiers&8 != 0 {
		parts = append(parts, "Super")
	}
	parts = append(parts, vkToAccelKey(vk))
	return strings.Join(parts, "+")
}

func vkToAccelKey(vk int) string {
	switch vk {
	case 0x20:
		return "Space"
	case 0x0D:
		return "Enter"
	case 0x1B:
		return "Escape"
	case 0x09:
		return "Tab"
	case 0x08:
		return "Backspace"
	case 0x2E:
		return "Delete"
	case 0x2D:
		return "Insert"
	case 0x21:
		return "PageUp"
	case 0x22:
		return "PageDown"
	case 0x24:
		return "Home"
	case 0x23:
		return "End"
	case 0x25:
		return "Left"
	case 0x26:
		return "Up"
	case 0x27:
		return "Right"
	case 0x28:
		return "Down"
	case 0xC0:
		return "`"
	case 0x70:
		return "F1"
	case 0x71:
		return "F2"
	case 0x72:
		return "F3"
	case 0x73:
		return "F4"
	case 0x74:
		return "F5"
	case 0x75:
		return "F6"
	case 0x76:
		return "F7"
	case 0x77:
		return "F8"
	case 0x78:
		return "F9"
	case 0x79:
		return "F10"
	case 0x7A:
		return "F11"
	case 0x7B:
		return "F12"
	}
	if vk >= 0x30 && vk <= 0x39 {
		return string(rune('0' + vk - 0x30))
	}
	if vk >= 0x41 && vk <= 0x5A {
		return string(rune('A' + vk - 0x41))
	}
	return fmt.Sprintf("VK_%d", vk)
}

func parseHotkeySetting(raw string) (int, int) {
	var mods, vk int
	fmt.Sscanf(raw, "%d,%d", &mods, &vk)
	if mods <= 0 || vk <= 0 {
		return MOD_CONTROL, VK_SPACE
	}
	return mods, vk
}

func getConfigDir() string {
	if goruntime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = os.Getenv("LOCALAPPDATA")
		}
		return filepath.Join(appData, "QuickDock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quickdock")
}

func ReregisterHotkey(modifiers, vk uintptr) {
	app := getHotkeyApp()
	if app == nil {
		fmt.Println("QuickDock: 应用未初始化，跳过热键重注册")
		return
	}

	oldAccel := getAppAccel()
	if oldAccel != "" {
		app.GlobalShortcut.Unregister(oldAccel)
	}

	newAccel := modVKToAccelerator(int(modifiers), int(vk))
	if err := app.GlobalShortcut.Register(newAccel, func() {
		toggleMainWindow()
	}); err != nil {
		fmt.Printf("QuickDock: 新热键 [%s] 注册失败: %v，回退到 Ctrl+Space\n", newAccel, err)
		fallbackAccel := "Ctrl+Space"
		app.GlobalShortcut.Register(fallbackAccel, func() {
			toggleMainWindow()
		})
		if appSvc != nil && appSvc.DB != nil {
			appSvc.DB.SetSetting("hotkey", "2,32")
		}
		setAccelerators(fallbackAccel, getClipAccel(), getPaletteAccel())
	} else {
		fmt.Printf("QuickDock: 全局快捷键 [%s] 已更新\n", newAccel)
		setAccelerators(newAccel, getClipAccel(), getPaletteAccel())
	}
}

func ReregisterClipboardHotkey(modifiers, vk uintptr) {
	app := getHotkeyApp()
	if app == nil {
		fmt.Println("QuickDock: 应用未初始化，跳过热键重注册")
		return
	}

	oldAccel := getClipAccel()
	if oldAccel != "" {
		app.GlobalShortcut.Unregister(oldAccel)
	}

	newAccel := modVKToAccelerator(int(modifiers), int(vk))
	cb := func() {
		cw := getClipboardWindow()
		if cw == nil {
			return
		}
		if clipboardMode.Load() {
			clipboardMode.Store(false)
			cw.Hide()
		} else {
			platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
			clipboardMode.Store(true)
			cw.Show()
			cw.Focus()
			if a := getHotkeyApp(); a != nil {
				a.Event.Emit("clipboard:toggle")
			}
		}
	}

	if err := app.GlobalShortcut.Register(newAccel, cb); err != nil {
		fmt.Printf("QuickDock: 剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+`\n", newAccel, err)
		fallbackAccel := "Ctrl+`"
		app.GlobalShortcut.Register(fallbackAccel, cb)
		if appSvc != nil && appSvc.DB != nil {
			appSvc.DB.SetSetting("clipboard_hotkey", "2,192")
		}
		setAccelerators(getAppAccel(), fallbackAccel, getPaletteAccel())
	} else {
		fmt.Printf("QuickDock: 剪贴板快捷键 [%s] 已更新\n", newAccel)
		setAccelerators(getAppAccel(), newAccel, getPaletteAccel())
	}
}

func SuspendHotkeys() {
	app := getHotkeyApp()
	if app == nil {
		return
	}
	appAccel := getAppAccel()
	if appAccel != "" {
		app.GlobalShortcut.Unregister(appAccel)
	}
	clipAccel := getClipAccel()
	if clipAccel != "" {
		app.GlobalShortcut.Unregister(clipAccel)
	}
	paletteAccel := getPaletteAccel()
	if paletteAccel != "" {
		app.GlobalShortcut.Unregister(paletteAccel)
	}
	fmt.Println("QuickDock: 热键已暂停（设置页捕获中）")
}

func ResumeHotkeys() {
	app := getHotkeyApp()
	if app == nil {
		return
	}

	appMods, appVk := MOD_CONTROL, int(VK_SPACE)
	clipMods, clipVk := MOD_CONTROL, int(VK_OEM_3)
	paletteMods, paletteVk := MOD_CONTROL, int(0x4B)

	if appSvc != nil && appSvc.DB != nil {
		if raw, err := appSvc.DB.GetSetting("hotkey"); err == nil && raw != "" {
			appMods, appVk = parseHotkeySetting(raw)
		}
		if raw, err := appSvc.DB.GetSetting("clipboard_hotkey"); err == nil && raw != "" {
			clipMods, clipVk = parseHotkeySetting(raw)
		}
		if raw, err := appSvc.DB.GetSetting("palette_hotkey"); err == nil && raw != "" {
			paletteMods, paletteVk = parseHotkeySetting(raw)
		}
	}

	appAccel := modVKToAccelerator(appMods, appVk)
	clipAccel := modVKToAccelerator(clipMods, clipVk)
	paletteAccel := modVKToAccelerator(paletteMods, paletteVk)

	registerAppShortcut := func(accel string) error {
		return app.GlobalShortcut.Register(accel, func() {
			toggleMainWindow()
		})
	}
	registerClipShortcut := func(accel string) error {
		return app.GlobalShortcut.Register(accel, func() {
			cw := getClipboardWindow()
			if cw == nil {
				return
			}
			if clipboardMode.Load() {
				clipboardMode.Store(false)
				cw.Hide()
			} else {
				platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
				clipboardMode.Store(true)
				cw.Show()
				cw.Focus()
				if a := getHotkeyApp(); a != nil {
					a.Event.Emit("clipboard:toggle")
				}
			}
		})
	}

	registeredAppAccel := appAccel
	if err := registerAppShortcut(appAccel); err != nil {
		registerAppShortcut("Ctrl+Space")
		registeredAppAccel = "Ctrl+Space"
	}
	registeredClipAccel := clipAccel
	if err := registerClipShortcut(clipAccel); err != nil {
		registerClipShortcut("Ctrl+`")
		registeredClipAccel = "Ctrl+`"
	}
	registerPaletteShortcut := func(accel string) error {
		return app.GlobalShortcut.Register(accel, func() {
			pw := getPaletteWindow()
			if pw == nil {
				return
			}
			if paletteMode.Load() {
				paletteMode.Store(false)
				pw.Hide()
			} else {
				platform.SetWindowToCursorScreen(pw, paletteWinWidth, paletteWinHeight)
				paletteMode.Store(true)
				pw.Show()
				pw.Focus()
				if a := getHotkeyApp(); a != nil {
					a.Event.Emit("palette:toggle")
				}
			}
		})
	}
	registeredPaletteAccel := paletteAccel
	if err := registerPaletteShortcut(paletteAccel); err != nil {
		registerPaletteShortcut("Ctrl+K")
		registeredPaletteAccel = "Ctrl+K"
	}
	setAccelerators(registeredAppAccel, registeredClipAccel, registeredPaletteAccel)
	fmt.Println("QuickDock: 热键已恢复")
}
