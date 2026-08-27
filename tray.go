package main

// 托盘 / 全局热键 / 窗口显隐调度。
//
// 本文件全部基于 Wails v3 框架 API 实现（app.SystemTray / app.GlobalShortcut），
// 不含任何 Win32 调用，可跨平台编译：
//   - 托盘：SystemTray.SetIcon/SetMenu/OnClick，右键默认弹出菜单（框架 smart default）
//   - 全局热键：GlobalShortcut.Register，支持 Win/macOS/Linux(X11+Wayland portal)
//   - 剪贴板监听：平台相关，拆分至 clipboard_listener_windows.go（Win32 消息窗口）
//     与 clipboard_listener_other.go（非 Windows no-op 占位）

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 热键修饰位与虚拟键码常量（与 DB 中 "modifiers,vk" 存储格式兼容）
const (
	MOD_ALT     = 0x0001
	MOD_CONTROL = 0x0002
	MOD_SHIFT   = 0x0004
	VK_SPACE    = 0x20
	VK_OEM_3    = 0xC0
)

// ctrlBackquote 反引号字符：Go 源码中反引号是 raw string 定界符，只能拼接生成。
const ctrlBackquote = "`"

// CTRL_BACKQUOTE 剪贴板热键回退加速器（Ctrl+`）
const CTRL_BACKQUOTE = "Ctrl+" + ctrlBackquote

//go:embed build/tray.ico
var trayIcoEmbed []byte

// trayIconData 从内嵌 ICO 中抽出最适合作托盘小图标的一帧的资源载荷（PNG/DIB）。
//
// 背景：框架 w32.CreateSmallHIconFromImage 会把传入的整份 .ico（含 ICONDIR 头）
// 直接交给 Windows CreateIconFromResourceEx，而该 API 期望的是"去掉 ICONDIR 头的
// 帧内资源数据"；整份文件传入会创建失败，并留下误导性的
// "The operation completed successfully."（ERROR_SUCCESS 残留）警告。
// 此函数先按面积最小抽出一帧，再交给框架转换——与旧版手工 loadIconFromEmbed 等价，
// 但保持本文件纯净（零 Win32 调用，跨平台可编译）。解析失败时回退原字节。
func trayIconData() []byte {
	data := trayIcoEmbed
	if len(data) < 6+16 {
		return data
	}
	count := int(data[4]) | int(data[5])<<8
	if count == 0 {
		return data
	}
	// 选面积最小的一帧（托盘小图标，缩放损耗最小；同面积取首个）
	best, bestArea := 0, 1<<30
	for i := 0; i < count; i++ {
		off := 6 + i*16
		if off+16 > len(data) {
			break
		}
		w, h := int(data[off]), int(data[off+1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if a := w * h; a < bestArea {
			bestArea = a
			best = i
		}
	}
	eOff := 6 + best*16
	imgOff := int(data[eOff+12]) | int(data[eOff+13])<<8 | int(data[eOff+14])<<16 | int(data[eOff+15])<<24
	imgSize := int(data[eOff+8]) | int(data[eOff+9])<<8 | int(data[eOff+10])<<16 | int(data[eOff+11])<<24
	if imgSize == 0 || imgOff+imgSize > len(data) {
		return data
	}
	return data[imgOff : imgOff+imgSize]
}

// 全局状态
var (
	hotkeyApp     *application.App
	hotkeyAppLock sync.Mutex

	// trayQuitRequested 标记「真退出」：托盘菜单退出时置位，
	// 主窗口 WindowClosing 钩子据此放行关闭（否则隐藏到托盘）。
	trayQuitRequested atomic.Bool

	trayInstance *application.SystemTray
	trayLock     sync.Mutex

	mainWin     *application.WebviewWindow
	mainWinLock sync.Mutex

	clipboardWin *application.WebviewWindow

	// appSvc 全局服务引用（热键/托盘回调使用）
	appSvc atomic.Pointer[services.AppService]

	// 当前注册的 GlobalShortcut 加速器（供 Suspend/Resume/Reregister 使用）
	currentAppAccel     string
	currentClipAccel    string
	currentPaletteAccel string
	currentNoteAccel    string
	accelMu             sync.Mutex

	noteWin     *application.WebviewWindow
	noteWinLock sync.Mutex

	paletteWin     *application.WebviewWindow
	paletteWinLock sync.Mutex
)

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

// StartHotkeyListener 启动托盘、全局热键与剪贴板监听（由 services.ServiceStartup 回调）。
//
// 托盘与热键均为框架跨平台实现，不再限制 GOOS；
// 剪贴板监听依赖平台消息机制，见 internal/platform/clipboard_listener_*.go。
func StartHotkeyListener(app *application.App, svc *services.AppService) {
	SetHotkeyApp(app)
	appSvc.Store(svc)

	createSystemTray(app)
	registerAllHotkeys(app)

	// 回调在 Win32 消息线程上触发，OnClipboardChange 内部的入库逻辑自行异步。
	platform.StartClipboardListener(func() {
		if svc := appSvc.Load(); svc != nil {
			svc.OnClipboardChange()
		}
	})
}

// createSystemTray 用框架 SystemTray 创建托盘图标与菜单。
// 左键显示主窗口；右键未设置回调时框架自动弹出菜单（applySmartDefaults）。
func createSystemTray(app *application.App) {
	menu := app.Menu.New()
	menu.Add("显示窗口").OnClick(func(*application.Context) {
		if win := GetMainWindow(); win != nil {
			showMainWindow(win)
		}
	})
	menu.Add("隐藏窗口").OnClick(func(*application.Context) {
		if win := GetMainWindow(); win != nil {
			hideMainWindow(win)
		}
	})
	menu.Add("打开设置").OnClick(func(*application.Context) {
		if win := GetMainWindow(); win != nil {
			showMainWindow(win)
			if a := getHotkeyApp(); a != nil {
				a.Event.Emit("app:open-settings")
			}
		}
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		requestQuit()
	})

	// SetIcon 接收一帧资源数据（见 trayIconData）：框架 CreateSmallHIconFromImage
	// 会按 SM_CXSMICON 缩放到托盘标准尺寸。注意不能传整份 .ico 文件，否则框架会
	// 把含 ICONDIR 头的整包交给 CreateIconFromResourceEx 而创建失败（误导性 WARN）。
	trayLock.Lock()
	tray := app.SystemTray.New()
	tray.SetIcon(trayIconData())
	tray.SetTooltip("快启坞 QuickDock")
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if win := GetMainWindow(); win != nil {
			showMainWindow(win)
		}
	})
	trayInstance = tray
	trayLock.Unlock()

	tray.Show()
	fmt.Println("QuickDock: 系统托盘已创建 (Wails SystemTray)")
}

// requestQuit 统一退出路径：置真退出标记 → 移除剪贴板监听 → app.Quit()。
func requestQuit() {
	trayQuitRequested.Store(true)
	saveAllFloatingPositions()
	platform.StopClipboardListener()
	if app := getHotkeyApp(); app != nil {
		app.Quit()
	} else {
		os.Exit(0)
	}
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

// modVKToAccelerator 把 DB 存储的 (modifiers,vk) 转为框架加速器字符串（如 "Ctrl+Space"）。
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
		fmt.Printf("QuickDock: 剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+backquote\n", newAccel, err)
		fallbackAccel := CTRL_BACKQUOTE
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
		cw.Show()
		cw.Focus()
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:shown")
		}
	}
}

// registerAllHotkeys 统一注册主窗口/剪贴板/命令面板/快捷笔记四个全局快捷键。
// 从 DB 读取配置，注册失败时回退到默认值并写回 DB。
func registerAllHotkeys(app *application.App) {
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
		logger.W("热键 [%s] 注册失败: %v，回退到 Ctrl+Space", appAccel, err)
		app.GlobalShortcut.Register("Ctrl+Space", func() { toggleMainWindow() })
		registeredAppAccel = "Ctrl+Space"
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("hotkey", "2,32")
		}
	} else {
		logger.I("全局快捷键 [%s] 已注册", appAccel)
	}

	// 剪贴板窗口热键回调
	registeredClipAccel := clipAccel
	if err := app.GlobalShortcut.Register(clipAccel, toggleClipboardWindow); err != nil {
		logger.W("剪贴板热键 [%s] 注册失败: %v，回退到 Ctrl+backquote", clipAccel, err)
		app.GlobalShortcut.Register(CTRL_BACKQUOTE, func() {
			cw := getClipboardWindow()
			if cw == nil {
				return
			}
			clipboardMode.Store(true)
			platform.SetWindowToCursorScreen(cw, clipWinWidth, clipWinHeight)
			cw.Show()
			cw.Focus()
			if a := getHotkeyApp(); a != nil {
				a.Event.Emit("clipboard:shown")
			}
		})
		registeredClipAccel = CTRL_BACKQUOTE
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("clipboard_hotkey", "2,192")
		}
	} else {
		logger.I("剪贴板快捷键 [%s] 已注册", clipAccel)
	}

	// 命令面板热键（默认 Ctrl+K）：纯开 / 关切换。
	// 打开后再次按下 → 直接 HidePaletteWindow 关闭面板（不再转发 palette:hotkey）。
	// 面板内的「动作菜单」改用 Ctrl+Enter 触发（见 CommandPalette.vue），
	// 避免系统级热键与页面 keydown 抢焦点、导致菜单永远不可达的问题。
	handlePaletteHotkey := func() {
		pw := getPaletteWindow()
		if pw == nil {
			return
		}
		if paletteMode.Load() {
			if svc := appSvc.Load(); svc != nil {
				svc.HidePaletteWindow()
			}
			return
		}
		platform.SetWindowToCursorScreen(pw, paletteWinWidth, paletteWinHeight)
		paletteMode.Store(true)
		pw.Show()
		pw.Focus()
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("palette:shown")
		}
	}
	registeredPaletteAccel := paletteAccel
	if err := app.GlobalShortcut.Register(paletteAccel, handlePaletteHotkey); err != nil {
		logger.W("命令面板热键 [%s] 注册失败: %v", paletteAccel, err)
		app.GlobalShortcut.Register("Ctrl+K", handlePaletteHotkey)
		registeredPaletteAccel = "Ctrl+K"
	} else {
		logger.I("命令面板快捷键 [%s] 已注册", paletteAccel)
	}

	// 快捷笔记热键（默认 Ctrl+Shift+N），复用剪贴板独立窗口导航到 #/note
	registeredNoteAccel := noteAccel
	if err := app.GlobalShortcut.Register(noteAccel, showNoteWindow); err != nil {
		logger.W("笔记热键 [%s] 注册失败: %v，回退到 Ctrl+Shift+N", noteAccel, err)
		app.GlobalShortcut.Register("Ctrl+Shift+N", showNoteWindow)
		registeredNoteAccel = "Ctrl+Shift+N"
		if svc := appSvc.Load(); svc != nil && svc.DB != nil {
			svc.DB.SetSetting("note_hotkey", "6,78")
		}
	} else {
		logger.I("笔记快捷键 [%s] 已注册", noteAccel)
	}

	setAccelerators(registeredAppAccel, registeredClipAccel, registeredPaletteAccel, registeredNoteAccel)
}
