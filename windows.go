package main

import (
	"os"
	"sync"
	"sync/atomic"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
	sshxsvc "quickdock/services/sshx"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/kvstore"
)

// 单实例检查已迁移到 Wails v3 框架：main.go 中 application.Options.SingleInstance。
// 框架实现为 CreateMutex + 隐藏事件窗口：二次启动自动通知首实例（OnSecondInstanceLaunch
// 回调里把主窗口带到前台）后以 ExitCode 退出，等价于旧的 CreateMutexW + EnumWindows 方案。

// clipboardWinLock 保护剪贴板窗口的懒创建（与 paletteWinLock 同模式）
var clipboardWinLock sync.Mutex

// winmgrWinLock 保护窗口管理浮窗的懒创建（与 clipboardWinLock 同模式）
var winmgrWinLock sync.Mutex
var winmgrWin *application.WebviewWindow

// sshWinLock 保护 SSH 终端窗口的懒创建（与 clipboardWinLock 同模式）
var sshWinLock sync.Mutex
var sshWin *application.WebviewWindow

// ===== WebView2 优化配置（全局，所有窗口共享）=====

// memoryOptimizedArgs 减少 WebView2 内存/进程数量的 Chromium 标志
// 这些标志传递给全局 WebView2 浏览器进程，影响所有窗口。
//
// 注意（2026-09-18 修正，修复"时间一长页面空白"）：
//   - 已移除 --in-process-gpu：该标志把 GPU 进程合并进浏览器进程。一旦 GPU 出故障，
//     整个浏览器进程会死，本应用所有 WebView2 窗口一起永久白屏且无法恢复（Wails v3
//     未挂 ProcessFailed 恢复）。改为 GPU 独立进程后，GPU 故障可被 WebView2 重启，不再连坐。
//   - 已移除 --renderer-process-limit=4：该限制让主窗口/浮窗/插件窗口共用同一渲染进程池，
//     任一渲染进程崩溃会拖垮共用它的其它窗口。移除后每个 WebView2 窗口各自独立渲染进程，
//     崩溃隔离，代价是任务管理器里多几个 msedgewebview2 进程（几 MB/个，可接受）。
//   - 不传 --disable-renderer-backgrounding：让 WebView2 在 PutIsVisible(false) 时
//     自动释放渲染/GPU 资源（后台窗口降级）。
//   - 已解除 Printing 禁用（2026-09-21，为"字帖打印"插件的 window.print()）：
//     Chromium 的 Printing 特性被禁用时 window.print() 变成空操作（不报错、不弹框），
//     插件侧无法自救。该禁用原本只为收敛功能面，与内存优化无关，解除后无额外收益损失。
//     代价：所有 WebView2 窗口（含插件 iframe）都恢复了打印能力，属全局变更。
var memoryOptimizedArgs = []string{
	"--disable-features=msSmartScreenProtection,Translate,ReadingList,MediaSessionService,NotificationService,PasswordManager,ChromeWhatsNewUI",
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
// 注意：不含 Printing——字帖打印插件需要 window.print()，见上方 memoryOptimizedArgs 注释。
var disabledFeatures = []string{
	"msSmartScreenProtection",
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
	// 构造 entry 切片前先各自持锁读取全局窗口指针：直接在切片字面量里读 clipboardWin 等全局变量
	// 未持锁，会与窗口创建/销毁 goroutine 的写发生 data race（指针读/写非原子）。
	clipboardWinLock.Lock()
	clipboardW := clipboardWin
	clipboardWinLock.Unlock()
	paletteWinLock.Lock()
	paletteW := paletteWin
	paletteWinLock.Unlock()
	noteWinLock.Lock()
	noteW := noteWin
	noteWinLock.Unlock()
	winmgrWinLock.Lock()
	winmgrW := winmgrWin
	winmgrWinLock.Unlock()
	type entry struct {
		key string
		w   *application.WebviewWindow
	}
	for _, e := range []entry{
		{"clipboard", clipboardW},
		{"palette", paletteW},
		{"note", noteW},
		{"winmgr", winmgrW},
	} {
		if e.w == nil {
			continue
		}
		x, y := e.w.Position()
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

// hideOnCloseHook 返回一个 WindowClosing 钩子：默认取消关闭并隐藏窗口，
// 仅当「真退出」标记置位（托盘退出/更新重启）时才放行销毁。
//
// 必须用 RegisterHook 而不能是 OnWindowEvent，原因见 initSshWindow 的长注释：
// Wails 建窗时就给自己注册了一个 WindowClosing 监听，那个监听无条件销毁窗口；
// 普通监听器各自起 goroutine 并发派发，谁先跑完不可控，于是 Cancel() 经常晚一步——
// 窗口已被销毁并从 Window 列表摘除，随后执行的 Hide() 又把 options.Hidden 置回 true，
// 下次 Show() 走「已销毁 → Run()」分支重建出一个看不见的窗口。
// 表现就是「关掉后再点热键/侧栏完全没反应」，要重启进程才恢复。
//
// onHide 在真正隐藏前调用，用于清窗口标志位、发 before-hide 事件等各窗口自己的收尾。
// 这里刻意不做超时/重试：竞态的根源是派发顺序，Hook 的同步语义才是解药。
func hideOnCloseHook(onHide func()) func(event *application.WindowEvent) {
	return func(event *application.WindowEvent) {
		if trayQuitRequested.Load() {
			return // 真退出：放行销毁，否则托盘退出时窗口会拦着不让关
		}
		event.Cancel()
		if onHide != nil {
			onHide()
		}
	}
}

// hideFloatingWindow 是四个浮窗（剪贴板/笔记/命令面板/窗口管理）共用的失焦隐藏逻辑：
// 记位置 → 清标志位 → 隐藏。closeHook 额外复用它，保证 Alt+F4 与失焦两条路径
// 收尾完全一致，不会出现「Alt+F4 关掉后标志位还留着，热键以为它开着」的状态错位。
func hideFloatingWindow(win *application.WebviewWindow, posKey string, flag *atomic.Bool) func() {
	return func() {
		x, y := win.Position()
		saveWinPos(posKey, x, y)
		if flag != nil {
			flag.Store(false)
		}
		win.Hide()
	}
}

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
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "clipboard")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	// 剪贴板比其它浮窗多一步：隐藏前要通知前端落盘，否则剪贴板内容会丢。
	hide := hideFloatingWindow(win, "clipboard", &windowFlags.Clipboard)
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		hide()
	})
	// Alt+F4 / 系统关闭会触发真实销毁；若此时直接销毁，缓存的指针不会被清空，
	// 之后对应热键仍返回已销毁的窗口、无法恢复，只能重启。因此默认取消关闭并改为隐藏，
	// 仅当「真退出」标记置位（托盘退出/更新重启）时才放行。用 Hook 而非监听器，理由见 hideOnCloseHook。
	win.RegisterHook(events.Common.WindowClosing, hideOnCloseHook(func() {
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		hide()
	}))
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
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "note")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	hide := hideFloatingWindow(win, "note", &windowFlags.Note)
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		hide()
	})
	// 同剪贴板窗口：默认取消 Alt+F4 销毁并隐藏，仅真退出时放行。理由见 hideOnCloseHook。
	win.RegisterHook(events.Common.WindowClosing, hideOnCloseHook(hide))
	return win
}

// initSshWindow 创建 SSH 终端独立窗口（延迟初始化）。
//
// 形态参考 DSH 窗口（internal/dsh/dsh_runtime.go 的 createDSHWindow）：面积大、
// 有边框、可缩放、显示在任务栏。与上面四个浮窗刻意不同：
//
//  1. 有边框、可缩放（Frameless=false + MinWidth/MinHeight）。终端是长会话工作台，
//     用户会一边看文档一边敲命令、会拉大窗口看日志，固定尺寸不可接受。
//  2. 失焦不隐藏。浮窗的「失焦即 Hide」套在终端上，会导致点到浏览器复制一条命令
//     窗口就消失，完全没法用。这里只在失焦时保存位置。
//  3. 显示在任务栏（显式 HiddenOnTaskbar:false，不调 applyWindowPlatformOptions）。
//     它是主窗口之外唯一需要 Alt+Tab / 点击任务栏切回的窗口；隐藏会让用户无从下手。
//
// 关闭仍拦截为 Hide（这点与 DSH 的「销毁并下次重建」不同）：SSH 会话常驻 Go 侧
// （internal/sshx 的 Manager），窗口留着比销毁再回放体验更直接——重新打开时终端
// 的滚动缓冲原样还在，不需要等回放。真退出（托盘退出/更新重启）才放行。
func initSshWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
		Title:            "快启坞 - SSH",
		Width:            sshWinWidth,
		Height:           sshWinHeight,
		MinWidth:         640,
		MinHeight:        420,
		Frameless:        false,
		AlwaysOnTop:      false,
		BackgroundColour: application.RGBA{Red: 22, Green: 24, Blue: 29, Alpha: 255},
		Windows:          application.WindowsWindow{HiddenOnTaskbar: false},
		URL:              "/#/ssh",
	}
	applySavedWinPos(app, &opts, "ssh")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		// 只记位置，不隐藏（与浮窗相反，见函数注释）。
		x, y := win.Position()
		saveWinPos("ssh", x, y)
	})
	// 关闭必须用 RegisterHook，不能用 OnWindowEvent。
	//
	// 完整根因分析见 hideOnCloseHook 的注释（四个浮窗是同一个 bug）。
	win.RegisterHook(events.Common.WindowClosing, hideOnCloseHook(func() {
		x, y := win.Position()
		saveWinPos("ssh", x, y)
		win.Hide()
	}))
	return win
}

// initWinmgrWindow 创建窗口管理浮窗（延迟初始化：9 种布局模板，点格子把当前窗口贴过去）
func initWinmgrWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
		Title:            "快启坞 - 窗口管理",
		Width:            winmgrWinWidth,
		Height:           winmgrWinHeight,
		Frameless:        true,
		AlwaysOnTop:      true,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/#/window-manager",
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "winmgr")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	hide := hideFloatingWindow(win, "winmgr", &windowFlags.Winmgr)
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		hide()
	})
	// 同剪贴板窗口：默认取消 Alt+F4 销毁并隐藏，仅真退出时放行。理由见 hideOnCloseHook。
	// 这个窗口尤其需要：它是被 Alt+F4 关得最多的一个，修之前关掉后再唤起会没反应。
	win.RegisterHook(events.Common.WindowClosing, hideOnCloseHook(hide))
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
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "palette")
	win := app.Window.NewWithOptions(opts)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		x, y := win.Position()
		saveWinPos("palette", x, y)
		if foregroundIsOwnedModal() {
			return // 自家模态对话框（文件选择框等）导致失焦，不隐藏
		}
		windowFlags.Palette.Store(false)
		win.Hide()
	})
	// 同剪贴板窗口：默认取消 Alt+F4 销毁并隐藏，仅真退出时放行。理由见 hideOnCloseHook。
	// 失焦路径不能复用 hideFloatingWindow：它要先把位置存下来，再判断是不是自家
	// 模态框导致的失焦（模态框弹出时也要记住位置，只是不隐藏）。
	win.RegisterHook(events.Common.WindowClosing, hideOnCloseHook(hideFloatingWindow(win, "palette", &windowFlags.Palette)))
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
	svc.GetWinmgrWindow = winmgrWindowGetter(app)
	svc.GetSshWindow = sshWindowGetter(app)
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

// getWinmgrWindow 返回窗口管理浮窗（热键回调用，懒创建）
func getWinmgrWindow() *application.WebviewWindow {
	winmgrWinLock.Lock()
	defer winmgrWinLock.Unlock()
	if winmgrWin == nil {
		app := getHotkeyApp()
		if app == nil {
			return nil
		}
		winmgrWin = initWinmgrWindow(app)
	}
	return winmgrWin
}

// winmgrWindowGetter 返回 AppService 使用的窗口管理浮窗 getter
func winmgrWindowGetter(app *application.App) func() *application.WebviewWindow {
	return func() *application.WebviewWindow {
		winmgrWinLock.Lock()
		defer winmgrWinLock.Unlock()
		if winmgrWin == nil {
			if app == nil {
				return nil
			}
			winmgrWin = initWinmgrWindow(app)
		}
		return winmgrWin
	}
}

// sshWindowGetter 返回 AppService 使用的 SSH 终端窗口 getter。
// 与浮窗一致采用懒创建：SSH 只在用户真正打开时才初始化 WebView2，不拖慢启动。
// 入口只有一个（services/sshx.OpenSshWindow），所以这里不另设包级 getSshWindow——
// 避免出现两条「一个发事件、一个不发」的显示路径。
func sshWindowGetter(app *application.App) func() *application.WebviewWindow {
	return func() *application.WebviewWindow {
		sshWinLock.Lock()
		defer sshWinLock.Unlock()
		if sshWin == nil {
			if app == nil {
				return nil
			}
			sshWin = initSshWindow(app)
		}
		return sshWin
	}
}

// sshServiceRef 保存 SSH 门面服务，供全局热键走与前端完全相同的打开路径。
var sshServiceRef *sshxsvc.SshService

// setSshService 在服务装配完成后注入（见 main.go）。
func setSshService(s *sshxsvc.SshService) { sshServiceRef = s }

// showSshWindow 唤起/前置 SSH 终端窗口（全局热键入口）。
//
// 只转调服务层的 OpenSshWindow，不自己写一套 Show+Focus+Emit：显示逻辑一旦分成两份，
// 迟早会出现「热键打开的不刷新清单、侧栏打开的才刷新」这类不一致。
func showSshWindow() {
	if sshServiceRef == nil {
		logger.W("QuickDock: SSH 服务未就绪，热键忽略")
		return
	}
	sshServiceRef.OpenSshWindow()
}

// EnsureConfigDir 确保配置目录存在（用于 WebviewUserDataPath）
func EnsureConfigDir() string {
	dir := platform.DefaultConfigDir()
	os.MkdirAll(dir, 0755)
	return dir
}
