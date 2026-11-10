package main

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/lock"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
	sshxsvc "quickdock/services/sshx"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/kvstore"
)

// lockAwareURL 给浮窗 URL 附加「建窗时是否已锁定」。
//
// 浮窗是懒创建 + 复用的：锁定态下首次按热键打开剪贴板，Vue 还没挂载完，
// 会先画一帧窗内的真实内容——剪贴板浮窗闪一帧历史列表，等于锁没生效。
// 把锁状态放进 query（必须放在 hash 之前，否则会变成 hash 的一部分），
// 前端首帧就能直接渲染锁层。窗口复用后 URL 不再更新，后续状态变化由
// lock:changed 事件同步，所以这里只是首帧兜底。
//
// 窗口排版浮层例外：它锁定期间照常可用（只对别的应用贴边/等分，不读 QuickDock
// 数据），前端对 #/window-manager 也不渲染锁层。若给它也带上 locked=1，
// index.html 会保留启动遮罩等 LockStatus 回来，白白多闪一下遮罩。
func lockAwareURL(path string) string {
	if !lock.IsLocked() || strings.HasPrefix(path, "/#/window-manager") {
		return path
	}
	return "/?locked=1" + strings.TrimPrefix(path, "/")
}

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

// ===== 窗口材质（Mica / Acrylic）=====

// floatingWindowBackdropEnabled 控制四个无边框浮窗（剪贴板 / 笔记 / 命令面板 /
// 窗口管理）是否套系统材质（Acrylic）。**开启**，配套措施有三条，缺一不可：
//
//  1. 首次唤起等首帧：浮窗是懒创建的，第一次按热键才建 WebView2 + 加载整套 SPA。
//     窗口是 Translucent（chromium 底色 (0,0,0,0)），若建完立刻 Show，DWM 会先
//     合成一帧只有 Acrylic、没有内容的空窗。newFloatingWindowOnMainThread 里的
//     waitFloatingFirstPaint 把首次 Show 推到首帧画完（超时兜底，见该函数注释）。
//  2. 隐藏系统红 ×：Win11 会给「设了 DWM 材质的 Frameless 窗口」画回原生关闭
//     按钮，stripGhostCaptionButtons 移除 WS_SYSMENU（代价：浮窗不再响应 Alt+F4）。
//  3. 前端 App.vue 按 effective 给浮窗也加 .qd-backdrop，根背景转透明让材质透出；
//     否则只露出 chromium 的实色底，是半吊子样式。
//
// 仍需如实说明的边界：Wails v3 的 hide() 除了 SW_HIDE 还会调 chromium.Hide()，
// show() 先 SW_SHOW 容器、再 chromium.Show()。因此**每次** Hide/Show 复用时
// WebView2 合成层都会断开重连，那一次重连会丢一帧内容。实测这一帧在深色与浅色
// 主题下都是**纯白**（与材质、BackgroundColour 无关：它画在 WebView2 子窗口内部，
// 压在我们客户区之上），所以不能靠调底色或补写材质盖掉。
// 现在的处理在 platform.ShowFloatingWindow：Show 之前先把窗口挪到屏幕外，让重连的
// 那一帧落在用户看不到的地方，90ms 后移回目标位置。详见 windowshow_windows.go。
// 不能改成「不调 chromium.Hide()」——那会重新引入 Wails 特意修过的
// DirectComposition 输入死区（桌面右键点不动）。
const floatingWindowBackdropEnabled = true

// firstPaintWaitTimeout 是「浮窗首次唤起等首帧」的兜底上限。
// WebView2 新建 + SPA 首屏渲染实测 100~300ms；runtime ready 事件到了就立刻放行，
// 超时也必须放行，否则任何加载异常都会表现为「按了热键没反应」（项目踩过这个坑）。
const firstPaintWaitTimeout = 300 * time.Millisecond

// firstPaintSettle 是 runtime ready 之后额外留的一点点余量：
// wails:runtime:ready 只说明 Wails 注入完成，Vue 挂载与首个 paint 还在随后一两帧里。
const firstPaintSettle = 40 * time.Millisecond

// skipFirstPaintWait 供「启动预建」路径使用：预建跑在主线程的 InvokeAsync 回调里，
// 在那里等 WindowRuntimeReady 会把消息循环堵死（该事件的关闭恰恰依赖消息循环派发），
// 只能走满 300ms 超时，表现为启动后主窗口卡一下。预建本来就不 Show，不需要门控。
// 只在预建回调执行期间置位，其余路径（热键首次唤起，跑在 goroutine）正常等待。
var skipFirstPaintWait atomic.Bool

// waitFloatingFirstPaint 只在窗口「懒创建」那一次被调用，等 WebView2 把首帧画出来
// 再让调用方 Show()。Hide/Show 复用不会再走这里，所以后续唤起**没有**额外等待。
//
// 用 Wails 的 WindowRuntimeReady（前端 wails:runtime:ready 触发）而不是自定义前端
// 事件：Go 侧内部事件，不依赖 bindings 是否重新生成，也不会因为浮窗组件还没订阅
// 而漏掉。事件在 NewWithOptions 返回后注册，理论上极快导航可能抢先触发，故必须
// 配 300ms 超时；即使漏事件，最坏也只是首次多等 300ms，不会卡住热键。
func waitFloatingFirstPaint(win *application.WebviewWindow) {
	if win == nil || !floatingWindowBackdropEnabled ||
		platform.EffectiveWindowBackdrop() == platform.BackdropOff ||
		skipFirstPaintWait.Load() {
		return
	}
	ready := make(chan struct{})
	var once sync.Once
	win.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		once.Do(func() { close(ready) })
	})
	select {
	case <-ready:
		time.Sleep(firstPaintSettle)
	case <-time.After(firstPaintWaitTimeout):
	}
}

// applyWindowBackdrop 按本次启动生效的材质给窗口选项套上系统半透明材质。
//
// 生效值由 main() 里的 platform.ResolveWindowBackdrop() 判定并缓存：Win10 /
// 非 Windows / 设置「关」时恒为 off，本函数直接返回，窗口保持原来的
// BackgroundTypeSolid + 实色 —— 这条路径与加材质功能之前完全一致。
//
// 主窗口 / SSH / 四个浮窗 / 插件独立窗口**统一跟随用户选的档位**
// （mica / acrylic / tabbed）；档位映射只有一份，在 platform.ApplyWindowBackdrop。
// floating 参数只用于 floatingWindowBackdropEnabled 这个总开关。
//
// 这里是**保留式赋值** opts.Windows.BackdropType，不整字段覆盖 WindowsWindow：
// 浮窗的 HiddenOnTaskbar 等属性由 applyWindowPlatformOptions 单独设置，
// 整字段覆盖会把它们冲掉（旧的 applyWindowPlatformOptions 正是这么写的）。
func applyWindowBackdrop(opts *application.WebviewWindowOptions, floating bool) {
	if floating && !floatingWindowBackdropEnabled {
		return
	}
	platform.ApplyWindowBackdrop(opts)
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

// hideAllFloatingWindows 收起全部浮窗（锁定触发时由 services 注入回调调用）。
//
// 只处理已经创建出来的窗口：getXxxWindow 是懒创建工厂，为了隐藏去调它会把从没
// 打开过的窗口建出来（还带一次 300ms 的首次绘制等待），完全没必要——没创建的窗口
// 本来就不在屏幕上。
//
// 只 Hide 不销毁：SSH 会话在 Go 侧保持连接、笔记草稿留在前端内存里、命令面板的
// 输入也还在，解锁后按热键重开就是原样。按方案约定也不自动恢复，避免解锁瞬间
// 弹出一堆窗口。
func hideAllFloatingWindows() {
	// 先各自持锁取出指针再统一操作：与 saveAllFloatingPositions 同理由，
	// 直接读全局变量会和窗口创建/销毁 goroutine 的写构成 data race。
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
	sshWinLock.Lock()
	sshW := sshWin
	sshWinLock.Unlock()

	if clipboardW != nil {
		// 与失焦隐藏保持一致：先让前端复位滚动位置，再收起
		if a := getHotkeyApp(); a != nil {
			a.Event.Emit("clipboard:before-hide")
		}
		clipboardW.Hide()
	}
	// 命令面板走服务方法：它要同步 Flags.Palette，否则解锁后热键会判成「面板还开着」
	// 而走隐藏分支，表现为第一次按 Ctrl+K 没反应。
	if paletteW != nil {
		if svc := appSvc.Load(); svc != nil {
			svc.HidePaletteWindow()
		} else {
			paletteW.Hide()
		}
	}
	if noteW != nil {
		noteW.Hide()
	}
	if winmgrW != nil {
		winmgrW.Hide()
	}
	if sshW != nil {
		sshW.Hide()
	}

	windowFlags.Clipboard.Store(false)
	windowFlags.Note.Store(false)
	windowFlags.Winmgr.Store(false)
	logger.I("[lock] 已收起全部浮窗")
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
		URL:              lockAwareURL("/#/clipboard"),
		Zoom:             uiZoom,

		// 关掉 WebView2 原生右键菜单（返回 / 刷新 / 另存为 / 打印 / 更多工具）。
		// dev 下框架强制开启默认菜单供调试，正式包由本开关关闭；详见 main.go。
		DefaultContextMenuDisabled: true,
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "clipboard")
	win := app.Window.NewWithOptions(opts)
	logWindowCreated("floating", opts.Title)
	stripGhostCaptionButtons(win)
	keepBackdropOnShow(win)
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
		URL:              lockAwareURL("/#/note"),
		Zoom:             uiZoom,

		// 关掉 WebView2 原生右键菜单（返回 / 刷新 / 另存为 / 打印 / 更多工具）。
		// dev 下框架强制开启默认菜单供调试，正式包由本开关关闭；详见 main.go。
		DefaultContextMenuDisabled: true,
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "note")
	win := app.Window.NewWithOptions(opts)
	logWindowCreated("floating", opts.Title)
	stripGhostCaptionButtons(win)
	keepBackdropOnShow(win)
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
// 关闭 = 隐藏复用（与四个浮窗一致），不是销毁。
//
// 曾经为省内存改成「关闭即销毁」：SSH 页是常驻重页面，WebView / xterm 实例 /
// 整棵 Vue 子树一直留着确实占内存；会话本身在 Go 侧 Manager、关窗不断连，重建时
// restoreTabs() 按 sessionId 接回终端（滚动缓冲由后端回放补齐），看起来代价可控。
// 但实测每次关掉再点开都要重新建 WebView2 + 重新加载整个 SPA + 重放会话，
// 侧栏得转圈一两秒（App.vue 的 sshOpening 就是为盖住这段空窗期加的），
// 比浮窗 Hide 复用的秒开差得明显。按用户反馈改回 Hide 复用：
//   - 包级引用不清空，GetSshWindow() 直接返回既有窗口 Show + Focus；
//   - Vue 不会重新 mount，滚动缓冲和终端状态原样保留，连回放都省了；
//   - 清单 / 会话状态靠 OpenSshWindow() 每次唤回时发的 quickdock:ssh:shown 刷新。
//
// 代价就是上面那笔常驻内存；进程退出时窗口随进程一起释放。
//
// 真退出（托盘退出 / 更新重启）时 hook 放行销毁，不做 Cancel。
func initSshWindow(app *application.App) *application.WebviewWindow {
	opts := application.WebviewWindowOptions{
		Title:  "快启坞 - SSH",
		Width:  sshWinWidth,
		Height: sshWinHeight,
		// 按 uiZoom 放大：否则 1.15 缩放下把窗口拖到最小时，80 列终端仍会被裁。
		MinWidth:         int(640 * uiZoom),
		MinHeight:        int(420 * uiZoom),
		Frameless:        false,
		AlwaysOnTop:      false,
		BackgroundColour: application.RGBA{Red: 22, Green: 24, Blue: 29, Alpha: 255},
		Windows:          application.WindowsWindow{HiddenOnTaskbar: false},
		URL:              lockAwareURL("/#/ssh"),
		Zoom:             uiZoom,
		// 建窗即隐藏，理由同 applyWindowPlatformOptions：SSH 也用保存的位置，
		// 让 Wails 先按可见创建会在屏幕内闪一帧再被 Hide()。
		Hidden: true,

		// 同上：关掉 WebView2 原生右键菜单（终端有自己的复制/粘贴菜单）。
		// dev 下走框架默认菜单，右键「检查」仍可用。
		DefaultContextMenuDisabled: true,
	}
	// SSH 与插件/DSH 窗口不同：它加载的是本应用主 SPA（App.vue 会跑），所以也要
	// 套同一套材质，否则前端加了 qd-backdrop、窗口却还是实色，两边对不上。
	// 用常驻窗口档位（用户选的那个），不是浮窗的 Acrylic。
	applyWindowBackdrop(&opts, false)
	applySavedWinPos(app, &opts, "ssh")
	win := app.Window.NewWithOptions(opts)
	logWindowCreated("floating", opts.Title)
	win.Hide()
	win.OnWindowEvent(events.Common.WindowLostFocus, func(event *application.WindowEvent) {
		// 只记位置，不隐藏（与浮窗相反，见函数注释）。
		x, y := win.Position()
		saveWinPos("ssh", x, y)
	})
	// 关闭必须用 RegisterHook，不能用 OnWindowEvent：Wails 建窗时自注册的
	// WindowClosing 监听会无条件销毁窗口，普通监听器并发派发时 Cancel() 常常晚一步。
	// 根因分析见 hideOnCloseHook 的注释（四个浮窗是同一个 bug）。
	//
	// 没直接复用 hideOnCloseHook，只因为它不会在真退出时保存位置；语义与浮窗一致：
	// Cancel 关闭 + Hide，**不清空 sshWin 引用**（下次秒开就靠它）。
	win.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		x, y := win.Position()
		saveWinPos("ssh", x, y)
		if trayQuitRequested.Load() {
			return // 真退出：放行销毁，进程即将结束
		}
		event.Cancel()
		win.Hide()
	})
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
		URL:              lockAwareURL("/#/window-manager"),
		Zoom:             uiZoom,

		// 关掉 WebView2 原生右键菜单（返回 / 刷新 / 另存为 / 打印 / 更多工具）。
		// dev 下框架强制开启默认菜单供调试，正式包由本开关关闭；详见 main.go。
		DefaultContextMenuDisabled: true,
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "winmgr")
	win := app.Window.NewWithOptions(opts)
	logWindowCreated("floating", opts.Title)
	stripGhostCaptionButtons(win)
	keepBackdropOnShow(win)
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
		URL:              lockAwareURL("/#/command-palette"),
		Zoom:             uiZoom,

		// 关掉 WebView2 原生右键菜单（返回 / 刷新 / 另存为 / 打印 / 更多工具）。
		// dev 下框架强制开启默认菜单供调试，正式包由本开关关闭；详见 main.go。
		DefaultContextMenuDisabled: true,
	}
	applyWindowPlatformOptions(&opts)
	applySavedWinPos(app, &opts, "palette")
	win := app.Window.NewWithOptions(opts)
	logWindowCreated("floating", opts.Title)
	stripGhostCaptionButtons(win)
	keepBackdropOnShow(win)
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
			paletteWin = newFloatingWindowOnMainThread(initPaletteWindow, app)
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
			clipboardWin = newFloatingWindowOnMainThread(initClipboardWindow, app)
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
			noteWin = newFloatingWindowOnMainThread(initNoteWindow, app)
		}
		return noteWin
	}
}

// newFloatingWindowOnMainThread 保证 WebView2 窗口一定在主线程创建。
//
// 热键回调跑在 Wails 的 goroutine 上，直接在那里 app.Window.NewWithOptions 会踩
// Wails v3 的跨线程建窗问题（死锁 / 次级窗口白屏）。原先靠「启动时同步预建三个
// 浮窗」绕开，代价是每个渲染进程空闲也常驻 30–80MB。改成按需创建后由这里补上
// 线程约束：InvokeSyncWithResult 会把创建投递到主线程并同步等结果；若调用方本来
// 就在主线程，Wails 的 dispatchOnMainThread 会直接执行，不会自己等自己死锁。
//
// 建窗后调 waitFloatingFirstPaint：只有「窗口对象从无到有」这一次会经过这里，
// 让首次 Show 落在 WebView2 首帧之后（材质开启时）。窗口复用期间的 Hide/Show
// 不会再创建窗口，也就不会再有任何等待。SSH 同样懒创建，顺带受益于首帧门控，
// 它首次打开慢本来也有一部分花在 SPA 首屏上。
func newFloatingWindowOnMainThread(
	init func(*application.App) *application.WebviewWindow,
	app *application.App,
) *application.WebviewWindow {
	if app == nil {
		return nil
	}
	win := application.InvokeSyncWithResult(func() *application.WebviewWindow {
		return init(app)
	})
	waitFloatingFirstPaint(win)
	return win
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
		clipboardWin = newFloatingWindowOnMainThread(initClipboardWindow, app)
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
		paletteWin = newFloatingWindowOnMainThread(initPaletteWindow, app)
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
		winmgrWin = newFloatingWindowOnMainThread(initWinmgrWindow, app)
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
			winmgrWin = newFloatingWindowOnMainThread(initWinmgrWindow, app)
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
			sshWin = newFloatingWindowOnMainThread(initSshWindow, app)
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
	// 锁定期间不弹 SSH 窗口：主机列表、凭据、终端输出都是高敏感数据
	if lockBlocksHotkey() {
		return
	}
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
