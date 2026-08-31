package main

import (
	"os"
	"sync"

	"quickdock/internal/platform"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/kvstore"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// foregroundIsOwnedModal 判断当前前台窗口是否为被本应用窗口拥有的模态对话框
// （例如命令面板内插件 <input type="file"> 弹出的系统文件选择框）。
// 命令面板等窗口在失焦时会隐藏自身；若失焦是自家模态框打开所致，则不应隐藏，
// 否则会出现"在插件里选文件时页面被关掉"的问题。
//
// 判定方式：取前台窗口的拥有者（GW_OWNER=4）。其值为非 0 说明这是一个被其它窗口
// 拥有的弹出式窗口（如文件选择框），通常正属于本应用——此时不应隐藏面板。
// 注意：不可叠加 GW_HWNDLAST(=1) 判断——该值对任意有效 HWND 都返回非零（返回 Z 序链尾
// 窗口，通常是桌面窗口），会导致本函数恒为 true、面板失焦后永不隐藏（已修复）。
func foregroundIsOwnedModal() bool {
	fg := w32.GetForegroundWindow()
	if fg == 0 {
		return false
	}
	return w32.GetWindow(fg, 4) != 0
}

// 单实例检查已迁移到 Wails v3 框架：main.go 中 application.Options.SingleInstance。
// 框架实现为 CreateMutex + 隐藏事件窗口：二次启动自动通知首实例（OnSecondInstanceLaunch
// 回调里把主窗口带到前台）后以 ExitCode 退出，等价于旧的 CreateMutexW + EnumWindows 方案。

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

// ===== 浮窗位置记忆（kvstore Service 持久化，main.go 注入）=====

var winPosKV *kvstore.KVStoreService

// saveWinPos 记录浮窗位置；失败静默（位置记忆属锦上添花，不值得打扰用户）。
func saveWinPos(key string, x, y int) {
	if winPosKV == nil {
		return
	}
	_ = winPosKV.Set("winpos/"+key, map[string]any{"x": x, "y": y})
}

// loadWinPos 读取并校验浮窗位置：越出所有屏幕（拔掉外接显示器等场景）时
// 按最近屏幕收拢，避免窗口"消失"在不可达坐标。
func loadWinPos(app *application.App, key string, w, h int) (int, int, bool) {
	if winPosKV == nil {
		return 0, 0, false
	}
	raw := winPosKV.Get("winpos/" + key)
	m, ok := raw.(map[string]any)
	if !ok {
		return 0, 0, false
	}
	toInt := func(v any) (int, bool) {
		switch n := v.(type) {
		case float64:
			return int(n), true
		case int:
			return n, true
		}
		return 0, false
	}
	x, okx := toInt(m["x"])
	y, oky := toInt(m["y"])
	if !okx || !oky {
		return 0, 0, false
	}
	x, y = clampWinPos(app, x, y, w, h)
	return x, y, true
}

// clampWinPos 把坐标收拢到中心点命中的屏幕（未命中取中心最近屏），
// 坐标空间与框架窗口一致（Bounds 为 DIP）。
func clampWinPos(app *application.App, x, y, w, h int) (int, int) {
	if app == nil {
		return x, y
	}
	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return x, y
	}
	cx, cy := x+w/2, y+h/2
	best, bestDist := 0, int64(1)<<62
	for i, s := range screens {
		b := s.Bounds
		if cx >= b.X && cx < b.X+b.Width && cy >= b.Y && cy < b.Y+b.Height {
			best = i
			break
		}
		mbx, mby := b.X+b.Width/2, b.Y+b.Height/2
		dx, dy := cx-mbx, cy-mby
		if d := int64(dx)*int64(dx) + int64(dy)*int64(dy); d < bestDist {
			bestDist = d
			best = i
		}
	}
	b := screens[best].Bounds
	if x < b.X {
		x = b.X
	}
	if y < b.Y {
		y = b.Y
	}
	if x+w > b.X+b.Width {
		x = b.X + b.Width - w
	}
	if y+h > b.Y+b.Height {
		y = b.Y + b.Height - h
	}
	return x, y
}

// saveAllFloatingPositions 退出前统一保存三个浮窗的当前位置（未创建则跳过）。
func saveAllFloatingPositions() {
	type entry struct {
		key string
		mu  *sync.Mutex
		w   *application.WebviewWindow
	}
	for _, e := range []entry{
		{"clipboard", &clipboardWinLock, clipboardWin},
		{"palette", &paletteWinLock, paletteWin},
		{"note", &noteWinLock, noteWin},
	} {
		e.mu.Lock()
		w := e.w
		e.mu.Unlock()
		if w == nil {
			continue
		}
		x, y := w.Position()
		saveWinPos(e.key, x, y)
	}
}

// applySavedWinPos 若有记忆位置则写入选项（含越界校正）。
func applySavedWinPos(app *application.App, opts *application.WebviewWindowOptions, key string) {
	if x, y, ok := loadWinPos(app, key, opts.Width, opts.Height); ok {
		opts.X, opts.Y = x, y
	}
}

// ===== 延迟窗口工厂 =====

// initClipboardWindow 创建剪贴板独立窗口（延迟初始化）
func initClipboardWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
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
	}
	applySavedWinPos(app, &opts, "clipboard")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		x, y := win.Position()
		saveWinPos("clipboard", x, y)
		clipboardMode.Store(false)
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		win.Hide()
	})
	// Alt+F4 / 系统关闭会触发真实销毁；若此时直接销毁，缓存的指针不会被清空，
	// 之后对应热键仍返回已销毁的窗口、无法恢复，只能重启。因此默认取消关闭并改为隐藏，
	// 仅当「真退出」标记置位（托盘退出/更新重启）时才放行。
	win.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if trayQuitRequested.Load() {
			return
		}
		event.Cancel()
		clipboardMode.Store(false)
		x, y := win.Position()
		saveWinPos("clipboard", x, y)
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		win.Hide()
	})
	return win
}

// initNoteWindow 创建笔记独立窗口（延迟初始化，独立于剪贴板/命令面板）
func initNoteWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
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
	}
	applySavedWinPos(app, &opts, "note")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		x, y := win.Position()
		saveWinPos("note", x, y)
		noteMode.Store(false)
		win.Hide()
	})
	// 同剪贴板窗口：默认取消 Alt+F4 销毁并隐藏，仅真退出时放行。
	win.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if trayQuitRequested.Load() {
			return
		}
		event.Cancel()
		noteMode.Store(false)
		x, y := win.Position()
		saveWinPos("note", x, y)
		win.Hide()
	})
	return win
}

// initPaletteWindow 创建命令面板独立窗口（延迟初始化）
func initPaletteWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
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
	}
	applySavedWinPos(app, &opts, "palette")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		x, y := win.Position()
		saveWinPos("palette", x, y)
		if foregroundIsOwnedModal() {
			return // 自家模态对话框（文件选择框等）导致失焦，不隐藏
		}
		paletteMode.Store(false)
		win.Hide()
	})
	// 同剪贴板窗口：默认取消 Alt+F4 销毁并隐藏，仅真退出时放行。
	win.OnWindowEvent(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if trayQuitRequested.Load() {
			return
		}
		event.Cancel()
		paletteMode.Store(false)
		x, y := win.Position()
		saveWinPos("palette", x, y)
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
