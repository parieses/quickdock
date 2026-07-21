package main

import (
	_ "embed"
	"fmt"
	"os"
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
	trayHICON         atomic.Uintptr
	trayQuitRequested atomic.Bool
	trayRemoved       atomic.Bool

	mainWin          *application.WebviewWindow
	mainWinLock      sync.Mutex
	clipboardWin     *application.WebviewWindow
	nextClipboardViewer atomic.Uintptr

	// 全局服务引用（供 windowProc 回调使用）
	appSvc atomic.Pointer[services.AppService]

	// 当前注册的 GlobalShortcut 加速器
	currentAppAccel     string
	currentClipAccel    string
	currentPaletteAccel string
	currentNoteAccel    string
	accelMu             sync.Mutex

	noteWin      *application.WebviewWindow
	noteWinLock  sync.Mutex

	paletteWin     *application.WebviewWindow
	paletteWinLock sync.Mutex

	// Windows DLL 句柄（包级复用，避免反复创建）
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modShell32  = syscall.NewLazyDLL("shell32.dll")

	// 缓存常用 Windows API proc（避免消息循环内反复 NewProc）
	procPostMessageW     = modUser32.NewProc("PostMessageW")
	procCreateIcon       = modUser32.NewProc("CreateIconFromResourceEx")
	procLoadIconW        = modUser32.NewProc("LoadIconW")
	procRegisterClassW   = modUser32.NewProc("RegisterClassW")
	procCreateWindowExW  = modUser32.NewProc("CreateWindowExW")
	procGetMessageW      = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW = modUser32.NewProc("DispatchMessageW")
	procDefWindowProcW   = modUser32.NewProc("DefWindowProcW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procDestroyIcon      = modUser32.NewProc("DestroyIcon")
	procChangeClipboardChain = modUser32.NewProc("ChangeClipboardChain")
	procSetClipboardViewer   = modUser32.NewProc("SetClipboardViewer")
	procShellNotifyIconW     = modShell32.NewProc("Shell_NotifyIconW")
)

// ---- 主窗口 ----


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
	appSvc.Store(svc)

	trayHICON.Store(loadIconFromEmbed())
	if trayHICON.Load() == 0 {
		fmt.Println("QuickDock: 加载托盘图标失败，使用默认图标")
	}

	go runMessageLoop()
}

func loadIconFromEmbed() uintptr {
	if len(trayIcoEmbed) < 6+16 {
		return 0
	}

	user32 := modUser32
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
	hIcon, _, _ := procCreateIcon.Call(
		uintptr(unsafe.Pointer(&imageData[0])),
		uintptr(imageSize),
		1,
		0x00030000,
		0, 0, 0,
	)

	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(32512))
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
		if svc := appSvc.Load(); svc != nil {
			svc.OnClipboardChange()
		}
		if nv := nextClipboardViewer.Load(); nv != 0 {
			procPostMessageW.Call(nv, WM_DRAWCLIPBOARD, wParam, lParam)
		}
		return 0

	case WM_CHANGECBCHAIN:
		nv := nextClipboardViewer.Load()
		if wParam == nv {
			nextClipboardViewer.Store(lParam)
		} else if nv != 0 {
			procPostMessageW.Call(nv, WM_CHANGECBCHAIN, wParam, lParam)
		}
		return 0
	}

	return callDefWindowProc(hwnd, msg, wParam, lParam)
}

func runMessageLoop() {
	goruntime.LockOSThread()

	user32 := modUser32
	className := syscall.StringToUTF16Ptr("QuickDock_Hotkey_Window_v3")

	kernel32 := modKernel32
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

	procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := procCreateWindowExW.Call(
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
	if svc := appSvc.Load(); svc != nil {
		svc.HiddenHWND.Store(uint64(hwnd))
	}
	fmt.Println("QuickDock: 隐藏窗口已创建")

	// 注册全局快捷键
	if app := getHotkeyApp(); app != nil {
		registerAllHotkeys(app)
	}

	createTrayIcon(hwnd)

	nextViewer, _, _ := procSetClipboardViewer.Call(hwnd)
	nextClipboardViewer.Store(nextViewer)
	fmt.Println("QuickDock: 剪贴板监听已启动")

	var msg struct {
		hwnd    uintptr
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      struct{ x, y int32 }
	}

	for {
		ret, _, _ := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 {
			break
		}

		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	fmt.Println("QuickDock: 消息循环已停止")
}

func createTrayIcon(hwnd uintptr) {
	shell32 := modShell32

	hIcon := trayHICON.Load()
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, uintptr(32512))
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

	ret, _, _ := procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(nid)))
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
	svc := appSvc.Load()
	var hwnd uintptr
	if svc != nil {
		hwnd = uintptr(svc.HiddenHWND.Load())
	}
	if hwnd != 0 && nextClipboardViewer.Load() != 0 {
		procChangeClipboardChain.Call(hwnd, nextClipboardViewer.Load())
		fmt.Println("QuickDock: 已离开剪贴板监听链")
	}

	nid := &NOTIFYICONDATAW{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATAW{})),
		HWnd:   hwnd,
		UID:    1,
	}
	procShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(nid)))
	fmt.Println("QuickDock: 系统托盘图标已移除")

	if th := trayHICON.Load(); th != 0 {
		procDestroyIcon.Call(th)
		trayHICON.Store(0)
	}

	trayQuitRequested.Store(true)
	if app := getHotkeyApp(); app != nil {
		app.Quit()
	} else {
		os.Exit(0)
	}
}

func showTrayMenu(hwnd uintptr) {
	user32 := modUser32

	createPopupMenu := user32.NewProc("CreatePopupMenu")
	hMenu, _, _ := createPopupMenu.Call()

	// 菜单项文本必须保持存活直到 TrackPopupMenu 返回，否则 GC 可能回收
	// StringToUTF16Ptr 分配的临时内存，导致菜单渲染时读取悬垂指针（Use-After-Free）。
	labelShow := syscall.StringToUTF16Ptr("显示窗口")
	labelHide := syscall.StringToUTF16Ptr("隐藏窗口")
	labelExit := syscall.StringToUTF16Ptr("退出")

	appendMenu := user32.NewProc("AppendMenuW")
	appendMenu.Call(hMenu, 0, 1001, uintptr(unsafe.Pointer(labelShow)))
	appendMenu.Call(hMenu, 0, 1003, uintptr(unsafe.Pointer(labelHide)))
	appendMenu.Call(hMenu, 0x800, 0, 0)
	appendMenu.Call(hMenu, 0, 1002, uintptr(unsafe.Pointer(labelExit)))

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

	// 确保菜单文本内存在 TrackPopupMenu 期间持续有效
	goruntime.KeepAlive(labelShow)
	goruntime.KeepAlive(labelHide)
	goruntime.KeepAlive(labelExit)
}

func callDefWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func postQuitMessage(exitCode int32) {
	procPostQuitMessage.Call(uintptr(exitCode))
}

// ===== GlobalShortcut 加速器 =====

func setAccelerators(appAccel, clipAccel, paletteAccel, noteAccel string) {
	accelMu.Lock()
	defer accelMu.Unlock()
	currentAppAccel = appAccel
	currentClipAccel = clipAccel
	currentPaletteAccel = paletteAccel
	currentNoteAccel = noteAccel
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

func getNoteAccel() string {
	accelMu.Lock()
	defer accelMu.Unlock()
	return currentNoteAccel
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
	key := platform.VKToKeyName(vk)
	if key == "" {
		key = fmt.Sprintf("VK_%d", vk)
	}
	parts = append(parts, key)
	return strings.Join(parts, "+")
}

func parseHotkeySetting(raw string) (int, int) {
	var mods, vk int
	fmt.Sscanf(raw, "%d,%d", &mods, &vk)
	if mods <= 0 || vk <= 0 {
		return MOD_CONTROL, VK_SPACE
	}
	return mods, vk
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
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("hotkey", "2,32")
		}
		setAccelerators(fallbackAccel, getClipAccel(), getPaletteAccel(), getNoteAccel())
	} else {
		fmt.Printf("QuickDock: 全局快捷键 [%s] 已更新\n", newAccel)
		setAccelerators(newAccel, getClipAccel(), getPaletteAccel(), getNoteAccel())
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
	cb := toggleClipboardWindow

	if err := app.GlobalShortcut.Register(newAccel, cb); err != nil {
		fmt.Printf("QuickDock: 剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+`\n", newAccel, err)
		fallbackAccel := "Ctrl+`"
		app.GlobalShortcut.Register(fallbackAccel, cb)
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("clipboard_hotkey", "2,192")
		}
		setAccelerators(getAppAccel(), fallbackAccel, getPaletteAccel(), getNoteAccel())
	} else {
		fmt.Printf("QuickDock: 剪贴板快捷键 [%s] 已更新\n", newAccel)
		setAccelerators(getAppAccel(), newAccel, getPaletteAccel(), getNoteAccel())
	}
}

func ReregisterNoteHotkey(modifiers, vk uintptr) {
	app := getHotkeyApp()
	if app == nil {
		fmt.Println("QuickDock: 应用未初始化，跳过热键重注册")
		return
	}

	oldAccel := getNoteAccel()
	if oldAccel != "" {
		app.GlobalShortcut.Unregister(oldAccel)
	}

	newAccel := modVKToAccelerator(int(modifiers), int(vk))
	cb := showNoteWindow

	if err := app.GlobalShortcut.Register(newAccel, cb); err != nil {
		fmt.Printf("QuickDock: 笔记热键 [%s] 注册失败: %v，回退到 Ctrl+Shift+N\n", newAccel, err)
		fallbackAccel := "Ctrl+Shift+N"
		app.GlobalShortcut.Register(fallbackAccel, cb)
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("note_hotkey", "6,78")
		}
		setAccelerators(getAppAccel(), getClipAccel(), getPaletteAccel(), fallbackAccel)
	} else {
		fmt.Printf("QuickDock: 笔记快捷键 [%s] 已注册\n", newAccel)
		setAccelerators(getAppAccel(), getClipAccel(), getPaletteAccel(), newAccel)
	}
}

// showNoteWindow 切换笔记独立窗口的显隐状态
func showNoteWindow() {
	nw := getNoteWindow()
	if nw == nil {
		return
	}
	if noteMode.Load() {
		noteMode.Store(false)
		nw.Hide()
	} else {
		platform.SetWindowToCursorScreen(nw, clipWinWidth, clipWinHeight)
		noteMode.Store(true)
		nw.Show()
		nw.Focus()
	}
}

// getNoteWindow 获取笔记窗口（延迟创建，独立于剪贴板）
func getNoteWindow() *application.WebviewWindow {
	noteWinLock.Lock()
	defer noteWinLock.Unlock()
	if noteWin == nil {
		app := getHotkeyApp()
		if app == nil {
			return nil
		}
		noteWin = initNoteWindow(app)
	}
	return noteWin
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
	noteAccel := getNoteAccel()
	if noteAccel != "" {
		app.GlobalShortcut.Unregister(noteAccel)
	}
	fmt.Println("QuickDock: 热键已暂停（设置页捕获中）")
}

func ResumeHotkeys() {
	if app := getHotkeyApp(); app != nil {
		registerAllHotkeys(app)
	}
	fmt.Println("QuickDock: 热键已恢复")
}

// registerAllHotkeys 统一注册主窗口/剪贴板/命令面板三个全局快捷键
// 从 DB 读取配置，注册失败时回退到默认值

// toggleClipboardWindow 切换剪贴板独立窗口的显隐状态
func toggleClipboardWindow() {
	cw := getClipboardWindow()
	if cw == nil {
		return
	}
	if clipboardMode.Load() {
		clipboardMode.Store(false)
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		cw.Hide()
	} else {
		platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
		clipboardMode.Store(true)
		// 窗口创建时 URL 已固定为 /#/clipboard，无需重复 SetURL
		cw.Show()
		cw.Focus()
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:shown")
		}
	}
}

func registerAllHotkeys(app *application.App) {
	// 读取热键配置
	appMods, appVk := MOD_CONTROL, int(VK_SPACE)
	clipMods, clipVk := MOD_CONTROL, int(VK_OEM_3)
	paletteMods, paletteVk := MOD_CONTROL, int(0x4B)
	noteMods, noteVk := MOD_CONTROL|MOD_SHIFT, int(0x4E)
	if svc := appSvc.Load(); svc != nil && svc.DB != nil {
		if raw, err := svc.DB.GetSetting("hotkey"); err == nil && raw != "" {
			appMods, appVk = parseHotkeySetting(raw)
		}
		if raw, err := svc.DB.GetSetting("clipboard_hotkey"); err == nil && raw != "" {
			clipMods, clipVk = parseHotkeySetting(raw)
		}
		if raw, err := svc.DB.GetSetting("palette_hotkey"); err == nil && raw != "" {
			paletteMods, paletteVk = parseHotkeySetting(raw)
		}
		if raw, err := svc.DB.GetSetting("note_hotkey"); err == nil && raw != "" {
			noteMods, noteVk = parseHotkeySetting(raw)
		}
	}

	appAccel := modVKToAccelerator(appMods, appVk)
	clipAccel := modVKToAccelerator(clipMods, clipVk)
	paletteAccel := modVKToAccelerator(paletteMods, paletteVk)
	noteAccel := modVKToAccelerator(noteMods, noteVk)

	// 主窗口热键回调
	registeredAppAccel := appAccel
	if err := app.GlobalShortcut.Register(appAccel, func() {
		toggleMainWindow()
	}); err != nil {
		fmt.Printf("QuickDock: 热键 [%s] 注册失败: %v，回退到 Ctrl+Space\n", appAccel, err)
		app.GlobalShortcut.Register("Ctrl+Space", func() { toggleMainWindow() })
		registeredAppAccel = "Ctrl+Space"
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("hotkey", "2,32")
		}
	} else {
		fmt.Printf("QuickDock: 全局快捷键 [%s] 已注册\n", appAccel)
	}

	// 剪贴板窗口热键回调
	registeredClipAccel := clipAccel
	if err := app.GlobalShortcut.Register(clipAccel, toggleClipboardWindow); err != nil {
		fmt.Printf("QuickDock: 剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+`\n", clipAccel, err)
		app.GlobalShortcut.Register("Ctrl+`", func() {
			cw := getClipboardWindow()
			if cw == nil { return }
			clipboardMode.Store(true)
			platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
			cw.Show()
			cw.Focus()
			if a := getHotkeyApp(); a != nil { a.Event.Emit("clipboard:shown") }
		})
		registeredClipAccel = "Ctrl+`"
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("clipboard_hotkey", "2,192")
		}
	} else {
		fmt.Printf("QuickDock: 剪贴板快捷键 [%s] 已注册\n", clipAccel)
	}

	// 命令面板热键（默认 Ctrl+K），注册前先读取 palette 配置
	registeredPaletteAccel := paletteAccel
	if err := app.GlobalShortcut.Register(paletteAccel, func() {
		pw := getPaletteWindow()
		if pw == nil { return }
		if paletteMode.Load() {
			paletteMode.Store(false)
			pw.Hide()
		} else {
			platform.SetWindowToCursorScreen(pw, paletteWinWidth, paletteWinHeight)
			paletteMode.Store(true)
			pw.Show()
			pw.Focus()
			if a := getHotkeyApp(); a != nil {
				a.Event.Emit("palette:shown")
			}
		}
	}); err != nil {
		fmt.Printf("QuickDock: 命令面板热键 [%s] 注册失败: %v\n", paletteAccel, err)
		app.GlobalShortcut.Register("Ctrl+K", func() {
			pw := getPaletteWindow()
			if pw == nil { return }
			platform.SetWindowToCursorScreen(pw, paletteWinWidth, paletteWinHeight)
			paletteMode.Store(true)
			pw.Show(); pw.Focus()
			if a := getHotkeyApp(); a != nil { a.Event.Emit("palette:shown") }
		})
		registeredPaletteAccel = "Ctrl+K"
	} else {
		fmt.Printf("QuickDock: 命令面板快捷键 [%s] 已注册\n", paletteAccel)
	}

	// 快捷笔记热键（默认 Ctrl+Shift+N），复用剪贴板独立窗口导航到 #/note
	registeredNoteAccel := noteAccel
	if err := app.GlobalShortcut.Register(noteAccel, showNoteWindow); err != nil {
		fmt.Printf("QuickDock: 笔记热键 [%s] 注册失败: %v，回退到 Ctrl+Shift+N\n", noteAccel, err)
		app.GlobalShortcut.Register("Ctrl+Shift+N", showNoteWindow)
		registeredNoteAccel = "Ctrl+Shift+N"
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("note_hotkey", "6,78")
		}
	} else {
		fmt.Printf("QuickDock: 笔记快捷键 [%s] 已注册\n", noteAccel)
	}

	setAccelerators(registeredAppAccel, registeredClipAccel, registeredPaletteAccel, registeredNoteAccel)
}
