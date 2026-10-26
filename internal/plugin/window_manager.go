package plugin

import (
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"quickdock/internal/logger"
)

// 隐藏窗口后超过该时长仍未再次打开，则回收 WebView2 渲染进程（释放内存）。
// 短于此时长内重新 Show 走复用路径（零延迟）；超过则真正销毁窗口，
// 下次 Show 自动走「不存在则新建」路径重建，回收同时停插件进程（经 WindowClosing 钩子）。
const pluginWindowRecycleAfter = 10 * time.Minute

// PluginWindowManager 管理每个插件的独立窗口（窗口注册表模式）
// 每个 pluginID 对应一个独立的 WebviewWindow，互不干扰
type PluginWindowManager struct {
	mu            sync.Mutex
	windows       map[string]*application.WebviewWindow // pluginID → 独立窗口
	app           *application.App
	mainWindow    *application.WebviewWindow // 主窗口引用：用于把新建插件窗口对齐到主窗口所在显示器
	mgr           *Manager                   // 插件管理器（用于「关窗即终止」时停止进程 / 按需惰性复活）
	baseWidth     int
	baseHeight    int
	zoom          float64                // UI 等比缩放（与主窗口一致）：窗口尺寸与内容按同系数放大，避免内容被裁
	themeDark     bool                   // 当前 App 主题是否为深色（用于窗口底色，避免浅色下露黑底）
	recycleAfter  time.Duration          // 隐藏后多久未再打开则回收 WebView2
	recycleTimers map[string]*time.Timer // pluginID → 回收计时器
}

// NewPluginWindowManager 创建窗口管理器。
// zoom 是全局 UI 等比缩放系数（与主窗口 main.go 的 uiZoom 一致），由调用方传入以保持单一来源；
// 插件窗口尺寸与内容按同系数放大，否则只放大内容会把固定尺寸的窗口撑爆/裁切。
func NewPluginWindowManager(app *application.App, mgr *Manager, zoom float64) *PluginWindowManager {
	if zoom <= 0 {
		zoom = 1 // 兜底：0/负值会算出 0×0 窗口，一律按不缩放处理
	}
	return &PluginWindowManager{
		windows:       make(map[string]*application.WebviewWindow),
		app:           app,
		mgr:           mgr,
		baseWidth:     800,
		baseHeight:    600,
		zoom:          zoom,
		themeDark:     true, // 默认深色（与窗口初始 BackgroundColour 一致）
		recycleAfter:  pluginWindowRecycleAfter,
		recycleTimers: make(map[string]*time.Timer),
	}
}

// SetMainWindow 注入主窗口引用，供新建插件窗口对齐到主窗口所在显示器。
// 必须由 main.go 在主窗口创建后调用（PluginWindowManager 构造早于主窗口，故用注入而非入参）。
func (m *PluginWindowManager) SetMainWindow(w *application.WebviewWindow) {
	m.mainWindow = w
}

// pluginWindowBackground 返回与主题匹配的底色：深 #17181b / 浅 #f5f6f8。
func pluginWindowBackground(dark bool) application.RGBA {
	if dark {
		return application.RGBA{Red: 23, Green: 24, Blue: 27, Alpha: 255}
	}
	return application.RGBA{Red: 245, Green: 246, Blue: 248, Alpha: 255}
}

// SetDarkMode 记录 App 主题并应用到所有已存在的插件窗口底色。
// 新建窗口在 Show 时也会沿用当前 themeDark，避免浅色下露出黑底。
func (m *PluginWindowManager) SetDarkMode(dark bool) {
	m.mu.Lock()
	m.themeDark = dark
	wins := make([]*application.WebviewWindow, 0, len(m.windows))
	for _, w := range m.windows {
		wins = append(wins, w)
	}
	m.mu.Unlock()
	bg := pluginWindowBackground(dark)
	for _, w := range wins {
		w.SetBackgroundColour(bg)
	}
}

// ensurePluginProcess 复用/新建窗口前确保插件进程存活（幂等：已 running 则 no-op）。
// 失败仅记 W 不阻断显示：窗口是 UI 壳，进程由 EnsureLoaded 拉活失败时用户仍能看到错误页面。
// 注意：调用方不得持有 m.mu——EnsureLoaded→LoadPlugin 会停旧实例/启动进程，不能在窗口锁内执行。
func (m *PluginWindowManager) ensurePluginProcess(pluginID string) {
	if m.mgr == nil {
		return
	}
	if err := m.mgr.EnsureLoaded(pluginID); err != nil {
		logger.W("[plugin-window] 打开插件 %s 前确保进程存活失败: %v", pluginID, err)
	}
}

// windowSize 返回插件窗口宽高（已按 zoom 放大）：优先用插件清单声明的前端尺寸
// （frontend.width/height，即插件布局的「设计画布」），未声明则退回通用基准
// baseWidth/baseHeight。两者都乘 zoom——声明尺寸是设计尺寸，放大后可见内容量不变、
// 文字随全局变大，插件从此按自己声明的尺寸开窗，而非一律挤在通用尺寸里。
func (m *PluginWindowManager) windowSize(pluginID string) (int, int) {
	w, h := m.baseWidth, m.baseHeight
	if m.mgr != nil {
		if inst := m.mgr.GetPlugin(pluginID); inst != nil {
			if fw, fh := inst.Manifest.Frontend.Width, inst.Manifest.Frontend.Height; fw > 0 && fh > 0 {
				w, h = fw, fh
			}
		}
	}
	return int(float64(w) * m.zoom), int(float64(h) * m.zoom)
}

// Show 显示插件窗口。如果窗口不存在则创建新窗口。
// showInTaskbar: 是否在任务栏显示图标（分离模式 = true）
// 返回 (窗口, 是否为新创建)
func (m *PluginWindowManager) Show(pluginID, title string, showInTaskbar bool) (*application.WebviewWindow, bool) {
	// 标记插件被使用：复用路径刷新闲置计时（新建路径的实例在 NewPluginInstance 已置 now），
	// 避免「刚打开的插件」被闲置降级循环误杀。
	if m.mgr != nil {
		m.mgr.MarkActive(pluginID)
	}
	m.mu.Lock()
	if win, ok := m.windows[pluginID]; ok {
		m.cancelRecycleLocked(pluginID)
		m.mu.Unlock()
		// 复用路径同样要确保插件进程存活：进程可能已被外部停止（禁用/手动停/崩溃放弃重启）
		// 而窗口引用仍在注册表——不拉活会显示一个"页面在、进程无"的僵尸窗口。
		m.ensurePluginProcess(pluginID)
		win.Show()
		win.Focus()
		logger.I("[plugin-window] 复用已存在窗口 %s（再次点击）", pluginID)
		return win, false
	}
	m.mu.Unlock()

	// 「关窗即终止」配套：首次打开或关闭后重开时，惰性确保插件进程已启动
	m.ensurePluginProcess(pluginID)

	// 持锁内「二次检查 → 创建 → 登记」，避免并发 Show 同 pluginID 各自建窗：
	// 后者会覆盖注册表、前者变孤儿（WebView2 进程泄漏 + 关窗时误删/误停）。
	m.mu.Lock()
	if win, ok := m.windows[pluginID]; ok {
		m.cancelRecycleLocked(pluginID)
		m.mu.Unlock()
		m.ensurePluginProcess(pluginID)
		win.Show()
		win.Focus()
		logger.I("[plugin-window] 并发命中已存在窗口 %s（跳过重复新建）", pluginID)
		return win, false
	}
	// 创建窗口（不触发其它事件钩子，无重入锁风险）
	// 尺寸：优先插件清单声明的 frontend.width/height，未声明退回通用基准；均按 zoom 放大。
	w, h := m.windowSize(pluginID)
	// 最小尺寸同样按 zoom 放大，但不超过窗口本身——否则声明了很小的插件（如小游戏）
	// 会被 min 顶得比它自己声明得还大。
	minW, minH := int(400*m.zoom), int(300*m.zoom)
	if minW > w {
		minW = w
	}
	if minH > h {
		minH = h
	}
	win := m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - " + title,
		Width:            w,
		Height:           h,
		MinWidth:         minW,
		MinHeight:        minH,
		Frameless:        true,
		BackgroundColour: pluginWindowBackground(m.themeDark),
		Zoom:             m.zoom,
		// 独立插件窗口加载轻量入口 plugin.html（仅挂载 PluginPage，不加载主 SPA），
		// 经 query 传 plugin id；首次打开免去主 bundle 下载，配合「关窗=隐藏复用」秒开。
		URL: "/plugin.html?id=" + pluginID,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: !showInTaskbar,
		},
	})
	// 用户点击关闭按钮 → 真正销毁窗口，并从注册表删除。新模型：关闭窗口只销毁视图，
	// 不杀插件后端（后端常驻后台运行，下次打开窗口直接复用）；状态保持 running。
	win.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		m.mu.Lock()
		delete(m.windows, pluginID)
		if t, ok := m.recycleTimers[pluginID]; ok {
			t.Stop()
			delete(m.recycleTimers, pluginID)
		}
		m.mu.Unlock()
		// 不调用 Cancel()，让窗口正常关闭销毁
		if m.mgr != nil {
			if err := m.mgr.StopPluginOnWindowClose(pluginID); err != nil {
				logger.W("[plugin-window] 窗口关闭 %s 后停止插件进程失败: %v", pluginID, err)
			}
		}
		logger.I("[plugin-window] 窗口关闭 %s，已从注册表移除（后端常驻后台，不杀进程）", pluginID)
	})
	m.windows[pluginID] = win
	m.mu.Unlock()

	// 多显示器：把新建的插件窗口对齐到主窗口当前所在的显示器。
	// 否则 Wails 默认把它放到主屏（Primary）中央，主窗口若在副屏时插件窗口会跑偏。
	// 复用路径（窗口已存在）不动它，保留用户上次拖到的位置。
	if m.mainWindow != nil {
		if ms, err := m.mainWindow.GetScreen(); err == nil && ms != nil {
			win.SetScreen(ms)
		}
	}

	win.Show()
	win.Focus() // 新建路径必须显式聚焦，否则新窗口 z-order 会被压在主窗口/面板之下
	logger.I("[plugin-window] 创建插件窗口 %s title=%s taskbar=%v size=%dx%d zoom=%.2f", pluginID, title, showInTaskbar, w, h, m.zoom)
	return win, true
}

// ShowInPanel 在面板中显示插件窗口（任务栏隐藏，用于浮动面板模式）
func (m *PluginWindowManager) ShowInPanel(pluginID, title string) (*application.WebviewWindow, bool) {
	return m.Show(pluginID, title, false)
}

// ShowAsWindow 以独立窗口显示插件（任务栏可见，有独立图标，仅能通过 X 关闭）
func (m *PluginWindowManager) ShowAsWindow(pluginID, title string) (*application.WebviewWindow, bool) {
	return m.Show(pluginID, title, true)
}

// Hide 仅隐藏指定插件的窗口，保留注册表引用（WebView2 进程不销毁以加速下次打开）。
// 关键：不调用 delete(m.windows, pluginID)。若移除引用，下次 Show 查不到注册表会
// NewWithOptions 新建窗口，旧的隐藏窗口沦为孤儿（WebView2 进程泄漏，且其 WindowClosing
// 日后仍会误删/误停）。保留引用后 Show 走正常复用路径（Show+Focus），无新建、无孤儿。
// 真正销毁只在用户点 X 关闭时由 WindowClosing 钩子完成（删引用 + StopPlugin）。
func (m *PluginWindowManager) Hide(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		win.Hide()
		// 重置回收计时：隐藏超过 recycleAfter 仍未重新打开则销毁窗口、释放 WebView2。
		// 重复 Hide 会重置计时，避免「关一下马上又开」被误回收。
		if t, ok := m.recycleTimers[pluginID]; ok {
			t.Stop()
		}
		m.recycleTimers[pluginID] = time.AfterFunc(m.recycleAfter, func() {
			m.recycle(pluginID)
		})
		logger.I("[plugin-window] 隐藏窗口 %s（保留复用，%s 后回收 WebView2）", pluginID, m.recycleAfter)
	}
}

// Close 立即销毁插件窗口（用户点窗口关闭按钮 = 明确不要了，不做保活复用）。
//
// 与 Hide 的区别：Hide 只隐藏并起回收计时器，窗口的 WebView2 renderer（实测 60~130 MB）
// 仍常驻到超时；Close 立刻从注册表移除并 Close，Close 触发 WindowClosing 钩子完成
// 「停插件进程」（关窗即终止）。下次打开经 EnsureLoaded 惰性复活。
//
// 关掉的插件窗口没有复用价值——用户已经表示不要了——所以不该白占 renderer 到超时。
func (m *PluginWindowManager) Close(pluginID string) {
	m.mu.Lock()
	win, ok := m.windows[pluginID]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.windows, pluginID)
	m.cancelRecycleLocked(pluginID)
	m.mu.Unlock()

	// 锁外销毁：Close 触发 WindowClosing 钩子（删引用 + 停进程），与 recycle 同一退出路径。
	win.Close()
	logger.I("[plugin-window] 关闭并销毁插件窗口 %s（不保活，立即释放 WebView2）", pluginID)
}

// IsWindowVisible 返回指定插件独立窗口当前是否可见（不存在返回 false）。
// 供 host.window.hide/show 在「临时隐藏后恢复」时判断该窗口原本是否可见，
// 仅恢复原本可见的，避免从未开独立窗口的插件凭空弹窗。
func (m *PluginWindowManager) IsWindowVisible(pluginID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	win, ok := m.windows[pluginID]
	if !ok || win == nil {
		return false
	}
	return win.IsVisible()
}

// ShowWindow 复用一个已存在的插件独立窗口（不新建），不存在则 no-op。
// 仅用于「取色等临时隐藏后恢复」——绝不在未开独立窗口时凭空创建窗口。
func (m *PluginWindowManager) ShowWindow(pluginID string) {
	m.mu.Lock()
	win, ok := m.windows[pluginID]
	m.mu.Unlock()
	if ok && win != nil {
		win.Show()
		win.Focus()
	}
}

// cancelRecycleLocked 取消某窗口的回收计时器，调用方须持 m.mu。
func (m *PluginWindowManager) cancelRecycleLocked(pluginID string) {
	if t, ok := m.recycleTimers[pluginID]; ok {
		t.Stop()
		delete(m.recycleTimers, pluginID)
	}
}

// recycle 在窗口隐藏超过 recycleAfter 后回收：销毁 WebView2 渲染进程、从注册表移除引用，
// 使下次 Show 自动走「不存在则新建」路径重建（复用既有逻辑）。
// 销毁经 Close() 触发 WindowClosing 钩子（删引用 + 停插件进程），与用户点 X 同一条退出路径，
// 故此处不再手动停进程。极短竞态（隐藏 30s 期间恰在同名瞬间重建窗口）被接受：
// recycle 仅对旧窗口对象 Close，新窗口不受影响，重建时 ensurePluginProcess 会惰性复活进程。
func (m *PluginWindowManager) recycle(pluginID string) {
	m.mu.Lock()
	win, ok := m.windows[pluginID]
	if !ok {
		m.mu.Unlock()
		return
	}
	// 窗口已被重新打开（用户重新 Show）则放弃回收，并清理本计时器
	if win.IsVisible() {
		delete(m.recycleTimers, pluginID)
		m.mu.Unlock()
		return
	}
	delete(m.windows, pluginID)
	delete(m.recycleTimers, pluginID)
	m.mu.Unlock()

	// 锁外销毁：Close 触发 WindowClosing 钩子（停进程）。
	// 不在锁内调用——StopPlugin 会启进程/扫描，与 window 锁互斥（见 ensurePluginProcess 注释）。
	win.Close()
	logger.I("[plugin-window] 隐藏超时回收窗口 %s（释放 WebView2，下次打开重建）", pluginID)
}

// FocusedWindow 返回当前持有焦点的插件窗口；无则 nil。
// 供宿主原生对话框绑定父窗口使用（无父对话框在 AlwaysOnTop 面板下会跑到所有窗口后面）。
func (m *PluginWindowManager) FocusedWindow() *application.WebviewWindow {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, win := range m.windows {
		if win != nil && win.IsFocused() {
			return win
		}
	}
	return nil
}

// CloseAll 关闭所有插件窗口（应用退出时调用）
func (m *PluginWindowManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.windows)
	for id, win := range m.windows {
		delete(m.windows, id)
		if t, ok := m.recycleTimers[id]; ok {
			t.Stop()
			delete(m.recycleTimers, id)
		}
		win.Hide()
	}
	logger.I("[plugin-window] CloseAll 关闭 %d 个插件窗口", n)
}

// Minimize 最小化指定插件的窗口
func (m *PluginWindowManager) Minimize(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		win.Minimise()
	}
}

// ToggleMaximize 切换指定插件的窗口最大化/还原
func (m *PluginWindowManager) ToggleMaximize(pluginID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if win, ok := m.windows[pluginID]; ok {
		if win.IsMaximised() {
			win.Restore()
		} else {
			win.Maximise()
		}
	}
}

// InjectInitText 向指定插件窗口注入初始文本 + 命中的子命令（从命令面板跨窗口传递）。
//
// 调用方：PluginService.ShowPluginWindow 的复用路径（created=false）。新建路径的
// 窗口经 iframe onLoad 自行消费 pending init（usePluginHost.usePendingInit），不走这里。
//
// 注入时机：有界轮询等 iframe 就绪（200ms × 15 = 3s 上限），而非固定延迟——
// 复用路径通常 iframe 早已在，但窗口刚创建的竞态下 PluginFrame 还在拉前端页。
//
// 为什么要带 theme / locale：这是「窗口已打开、命令面板再次命中同一插件」时
// 唯一的 init 通道。早先这里只下发 text/command，插件收到的 plugin:init
// 里 data.theme 为 undefined。虽然窗口初次 onLoad 时 usePluginHost 已经发过
// 带 theme 的 init，但此后用户可能在主窗口切了主题——此时这条迟到的 init
// 反而成了插件唯一能拿到的「当前状态」信号，缺了 theme 就会用旧值渲染。
// 取值口径与前端 currentThemeName() 一致：读本窗口 <html data-theme>，
// 该属性由 plugin-entry.ts 启动时对齐、并随 quickdock:theme-changed 广播更新。
func (m *PluginWindowManager) InjectInitText(pluginID, text, command string) {
	// 空 init 不注入：复用窗口里插件可能已处于编辑状态，插件页收到 init 会按
	// text/command 重渲染，空注入等于把用户正在弄的半截状态清掉。
	if text == "" && command == "" {
		return
	}
	m.mu.Lock()
	win, ok := m.windows[pluginID]
	m.mu.Unlock()
	if !ok {
		return
	}
	safeText := fmt.Sprintf("%q", text)
	safeCmd := fmt.Sprintf("%q", command)
	// 有界轮询等 iframe 就绪后再发 init。复用路径的窗口通常早已挂载 iframe；
	// 极端竞态下窗口刚创建、PluginFrame 还在异步拉前端页（loading 态无 iframe），
	// 原先的单次 setTimeout(400) 探不到 iframe 就静默丢弃，init 丢失。
	// 每 200ms 探一次、最多 3s（15 次）：插件无前端页时永远等不到，到点放弃；
	// 窗口被关则 JS 上下文随销毁，interval 自然终止，无泄漏。
	win.ExecJS(fmt.Sprintf(
		`(function(){
			var tries = 0;
			var timer = setInterval(function(){
				var ifr = document.querySelector('iframe');
				if (ifr && ifr.contentWindow) {
					clearInterval(timer);
					var de = document.documentElement;
					var theme = de.getAttribute('data-theme') === 'light' ? 'light' : 'dark';
					var locale = de.getAttribute('lang') || 'zh-CN';
					ifr.contentWindow.postMessage({type:'plugin:init', data:{text:%s, command:%s, theme:theme, locale:locale}}, '*');
				} else if (++tries >= 15) {
					clearInterval(timer);
				}
			}, 200);
		})()`, safeText, safeCmd))
}
