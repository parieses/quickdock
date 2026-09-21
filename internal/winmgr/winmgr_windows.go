//go:build windows

package winmgr

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

// Rect 物理像素矩形。
// 绕过高 DPI 二次缩放：所有坐标都用框架 Screen 的 PhysicalWorkArea（物理像素），
// 与 internal/platform/monitor.go 的约定一致。
type Rect struct {
	X, Y, Width, Height int
}

// ForegroundWindow 返回当前前台（激活）窗口句柄。
// 窗口管理热键作用于「用户正在使用的窗口」，因此统一从这里取目标。
func ForegroundWindow() (uintptr, error) {
	h := w32.GetForegroundWindow()
	if h == 0 {
		return 0, fmt.Errorf("no foreground window")
	}
	return uintptr(h), nil
}

// ForegroundInfo 返回「用户想操作的那个窗口」的句柄 + 标题 + 物理矩形。
//
// 不是 GetForegroundWindow 的裸封装：当前台是 QuickDock 自己的窗口时（排版浮层的
// 创建动作会抢一次前台；浮层已经打开时前台也一直是它），会沿 Z 序向下找到第一个
// 「非本进程 + 可见 + 有标题」的顶层窗口。否则 Ctrl+Alt+W 永远只能捕获到自己。
//
// 排版浮层在 Show() 之前调用它捕获目标：浮层一旦显示并获得焦点，
// GetForegroundWindow 就指向 QuickDock 自己了。
func ForegroundInfo() (WindowInfo, error) {
	h := w32.GetForegroundWindow()
	if h == 0 {
		return WindowInfo{}, fmt.Errorf("no foreground window")
	}
	if !IsSelfWindow(uintptr(h)) && w32.IsWindowVisible(h) {
		return windowInfoOf(h), nil
	}
	// 前台是自身窗口：沿 Z 序向下找最近的第三方窗口（限制步数，防异常链表死循环）。
	for n, steps := w32.GetWindow(h, w32.GW_HWNDNEXT), 0; n != 0 && steps < 512; n, steps = w32.GetWindow(n, w32.GW_HWNDNEXT), steps+1 {
		if IsSelfWindow(uintptr(n)) || !w32.IsWindowVisible(n) {
			continue
		}
		// 回退路径要求有标题：避免把 shell 的隐藏辅助窗口当成排版目标。
		if w32.GetWindowText(n) != "" {
			return windowInfoOf(n), nil
		}
	}
	return WindowInfo{}, fmt.Errorf("no capturable foreground window")
}

// windowInfoOf 组装窗口元信息（句柄 + 标题 + 物理矩形）。
func windowInfoOf(h w32.HWND) WindowInfo {
	return WindowInfo{HWND: uintptr(h), Title: w32.GetWindowText(h), Rect: currentRect(uintptr(h))}
}

// WindowAlive 判断窗口是否仍然存在：排版目标可能在浮层打开后被用户关掉。
func WindowAlive(hwnd uintptr) bool {
	return hwnd != 0 && w32.IsWindow(w32.HWND(hwnd))
}

// IsSelfWindow 判断 hwnd 是否属于本进程（QuickDock 的主窗口 / 浮窗 / 插件窗口）。
// 用于把自身窗口从候选列表里排除，避免「把 QuickDock 自己排走」。
func IsSelfWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	_, pid := w32.GetWindowThreadProcessId(w32.HWND(hwnd))
	return pid != 0 && uint32(pid) == uint32(syscall.Getpid())
}

// ToggleAlwaysOnTop 切换窗口置顶状态，返回切换后的置顶状态（true=已置顶）。
func ToggleAlwaysOnTop(hwnd uintptr) (bool, error) {
	if hwnd == 0 {
		return false, fmt.Errorf("invalid window handle")
	}
	ex := w32.GetWindowLong(w32.HWND(hwnd), w32.GWL_EXSTYLE)
	if ex&w32.WS_EX_TOPMOST != 0 {
		if !w32.SetWindowPos(w32.HWND(hwnd), w32.HWND_NOTOPMOST, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE) {
			return false, fmt.Errorf("SetWindowPos(NOTOPMOST) failed")
		}
		return false, nil
	}
	if !w32.SetWindowPos(w32.HWND(hwnd), w32.HWND_TOPMOST, 0, 0, 0, 0, w32.SWP_NOMOVE|w32.SWP_NOSIZE) {
		return false, fmt.Errorf("SetWindowPos(TOPMOST) failed")
	}
	return true, nil
}

// ApplyLayout 把窗口按 layout 贴到目标显示器工作区。
//
// layout 取值：
//
//	left/right/top/bottom/tl/tr/bl/br/center —— 半屏/四分/居中
//	maximize/restore/minimize               —— 窗口状态
//	monitor-prev/monitor-next               —— 跨显示器移动
//
// ratio 仅对 left/right 生效（左占比例 0~1，如 0.6 表示左占 60%）。
// monitorIndex: -1=窗口当前所在屏；0/1/...=指定屏；monitor-prev/next 时此参数忽略（按当前屏环形偏移）。
func ApplyLayout(hwnd uintptr, layout string, ratio float64, monitorIndex int) error {
	if hwnd == 0 {
		return fmt.Errorf("invalid window handle")
	}

	// 窗口状态类操作无屏幕依赖，直接执行。
	switch layout {
	case "maximize":
		w32.ShowWindow(w32.HWND(hwnd), w32.SW_MAXIMIZE)
		return nil
	case "minimize":
		w32.ShowWindow(w32.HWND(hwnd), w32.SW_MINIMIZE)
		return nil
	case "restore":
		w32.ShowWindow(w32.HWND(hwnd), w32.SW_RESTORE)
		return nil
	}

	work, idx, err := targetWorkArea(hwnd, layout, monitorIndex)
	if err != nil {
		return err
	}

	r := layoutRect(layout, work, ratio, hwnd)

	// 与批量排版共用 placeWindow：一并处理「最大化/最小化未彻底复位」与「抢焦点」两个坑。
	if !placeWindow(hwnd, r) {
		return fmt.Errorf("SetWindowPos failed for layout %s", layout)
	}
	logger.I("QuickDock: 窗口布局 [%s] 应用到屏#%d %+v", layout, idx, r)
	return nil
}

// layoutRect 根据布局名计算目标物理像素矩形。
func layoutRect(layout string, work Rect, ratio float64, hwnd uintptr) Rect {
	switch layout {
	case "left":
		w := int(float64(work.Width) * clampRatio(ratio))
		return Rect{work.X, work.Y, w, work.Height}
	case "right":
		w := int(float64(work.Width) * (1 - clampRatio(ratio)))
		return Rect{work.X + work.Width - w, work.Y, w, work.Height}
	case "top":
		h := work.Height / 2
		return Rect{work.X, work.Y, work.Width, h}
	case "bottom":
		h := work.Height / 2
		return Rect{work.X, work.Y + work.Height - h, work.Width, h}
	case "tl":
		return Rect{work.X, work.Y, work.Width / 2, work.Height / 2}
	case "tr":
		return Rect{work.X + work.Width/2, work.Y, work.Width / 2, work.Height / 2}
	case "bl":
		return Rect{work.X, work.Y + work.Height/2, work.Width / 2, work.Height / 2}
	case "br":
		return Rect{work.X + work.Width/2, work.Y + work.Height/2, work.Width / 2, work.Height / 2}
	case "center":
		cw := int(float64(work.Width) * 0.7)
		ch := int(float64(work.Height) * 0.7)
		return Rect{work.X + (work.Width-cw)/2, work.Y + (work.Height-ch)/2, cw, ch}
	case "monitor-prev", "monitor-next":
		// 跨屏：保持当前尺寸，落到目标屏工作区内（过大则收敛）。
		cur := currentRect(hwnd)
		w, h := cur.Width, cur.Height
		if w <= 0 || h <= 0 {
			w = int(float64(work.Width) * 0.7)
			h = int(float64(work.Height) * 0.7)
		}
		if w > work.Width {
			w = work.Width
		}
		if h > work.Height {
			h = work.Height
		}
		return Rect{work.X + (work.Width-w)/2, work.Y + (work.Height-h)/2, w, h}
	default:
		return Rect{work.X, work.Y, work.Width, work.Height}
	}
}

// targetWorkArea 计算目标屏的物理工作区矩形。
func targetWorkArea(hwnd uintptr, layout string, monitorIndex int) (Rect, int, error) {
	screens := allScreens()
	if len(screens) == 0 {
		return Rect{}, -1, fmt.Errorf("no screens available")
	}
	if monitorIndex >= 0 && monitorIndex < len(screens) {
		b := screens[monitorIndex].PhysicalWorkArea
		return Rect{b.X, b.Y, b.Width, b.Height}, monitorIndex, nil
	}
	curIdx := screenIndexOf(hwnd, screens)
	if layout == "monitor-prev" || layout == "monitor-next" {
		delta := 1
		if layout == "monitor-prev" {
			delta = -1
		}
		curIdx = (curIdx + delta) % len(screens)
		if curIdx < 0 {
			curIdx += len(screens)
		}
	}
	b := screens[curIdx].PhysicalWorkArea
	return Rect{b.X, b.Y, b.Width, b.Height}, curIdx, nil
}

// allScreens 返回全部屏幕（含物理像素几何）。
func allScreens() []*application.Screen {
	app := application.Get()
	if app == nil {
		return nil
	}
	return app.Screen.GetAll()
}

// screenIndexOf 按窗口中心坐标判定其所在屏索引；未命中取中心最近者。
func screenIndexOf(hwnd uintptr, screens []*application.Screen) int {
	rect := w32.GetWindowRect(w32.HWND(hwnd))
	var cx, cy int
	if rect != nil {
		cx = int((rect.Left + rect.Right) / 2)
		cy = int((rect.Top + rect.Bottom) / 2)
	}
	best := 0
	bestDist := int64(1) << 62
	for i, s := range screens {
		b := s.PhysicalWorkArea
		if cx >= b.X && cx < b.X+b.Width && cy >= b.Y && cy < b.Y+b.Height {
			return i
		}
		dx := b.X + b.Width/2 - cx
		dy := b.Y + b.Height/2 - cy
		d := int64(dx)*int64(dx) + int64(dy)*int64(dy)
		if d < bestDist {
			bestDist, best = d, i
		}
	}
	return best
}

// currentRect 取窗口当前物理尺寸。
func currentRect(hwnd uintptr) Rect {
	rect := w32.GetWindowRect(w32.HWND(hwnd))
	if rect == nil {
		return Rect{}
	}
	return Rect{int(rect.Left), int(rect.Top), int(rect.Right - rect.Left), int(rect.Bottom - rect.Top)}
}

// clampRatio 约束分屏比例在 (0, 0.95] 区间，非法值回退 0.5。
func clampRatio(r float64) float64 {
	if r <= 0 {
		return 0.5
	}
	if r > 0.95 {
		return 0.95
	}
	return r
}

// WindowInfo 第三方窗口元信息（枚举结果）。坐标统一物理像素，绕过高 DPI 二次缩放。
type WindowInfo struct {
	HWND  uintptr
	Title string
	Rect  Rect
}

// enumWindowsCallback 持有回调指针，防止枚举期间被 GC 回收（syscall.NewCallback 的经典坑）。
var enumWindowsCallback uintptr

// EnumWindows 枚举所有「可见、非工具窗口、非 cloaked、有标题」的第三方顶层窗口，
// 排除自身进程（QuickDock 的全部窗口），返回物理像素几何。供窗口管理浮层列出候选。
func EnumWindows() ([]WindowInfo, error) {
	infos := make([]WindowInfo, 0, 32)

	enumWindowsCallback = syscall.NewCallback(func(hwnd w32.HWND, lparam uintptr) uintptr {
		// 排除自身进程（QuickDock 主窗口/浮窗/插件窗口都是同一 PID）。
		if IsSelfWindow(uintptr(hwnd)) {
			return 1
		}
		// 可见性：不可见窗口不排。
		if !w32.IsWindowVisible(hwnd) {
			return 1
		}
		style := int(w32.GetWindowLong(hwnd, w32.GWL_STYLE))
		if style&int(w32.WS_VISIBLE) == 0 {
			return 1
		}
		exStyle := int(w32.GetWindowLong(hwnd, w32.GWL_EXSTYLE))
		// 工具窗口（任务栏隐藏的辅助窗口）排除。
		if exStyle&int(w32.WS_EX_TOOLWINDOW) != 0 {
			return 1
		}
		// DWM cloaked：任务视图/虚拟桌面里隐藏的窗口排除。
		var cloaked int32
		if w32.DwmGetWindowAttribute(hwnd, w32.DWMWA_CLOAKED, unsafe.Pointer(&cloaked), 4) == 0 && cloaked != 0 {
			return 1
		}
		title := w32.GetWindowText(hwnd)
		if title == "" {
			return 1
		}
		r := currentRect(uintptr(hwnd))
		infos = append(infos, WindowInfo{HWND: uintptr(hwnd), Title: title, Rect: r})
		return 1 // 继续枚举
	})
	w32.EnumWindows(enumWindowsCallback, 0)
	return infos, nil
}

// TilingCell 排版模板里的单个格子：归一化矩形（相对目标屏工作区，0~1）。
// 纯几何描述——哪个窗口进哪一格由 ApplyTiling 决定，前端不需要知道任何窗口句柄。
type TilingCell struct {
	X, Y, W, H float64
}

// ApplyTiling 把一个布局模板整块应用到目标屏。
//
// targetCell 是用户在浮层里点中的格子下标：本次捕获的目标窗口（targetHWND）进这一格；
// 其余格子按 Z 序（最近用过在前）用目标屏上的其他可见窗口依次填充，窗口不够时空着。
// 例：桌面上 3 个窗口、点四象限模板的第 4 格 → 目标窗口进第 4 格，另 2 个填第 1、2 格，第 3 格为空。
//
// useCursorScreen: true=按光标所在屏换算几何（用户正注视的显示器）；false=按目标窗口当前所在屏。
// 返回本次排版的结果（成功窗口数 + 参与窗口标题 + 同屏候选数）。
func ApplyTiling(cells []TilingCell, targetCell int, targetHWND uintptr, useCursorScreen bool) (TilingResult, error) {
	if len(cells) == 0 {
		return TilingResult{}, fmt.Errorf("no cells to tile")
	}
	work, idx, err := tilingWorkArea(targetHWND, useCursorScreen)
	if err != nil {
		return TilingResult{}, err
	}
	if targetCell < 0 || targetCell >= len(cells) {
		targetCell = 0
	}

	others := fillCandidates(work, targetHWND)
	res := TilingResult{Peers: len(others)}

	for i, c := range cells {
		var hwnd uintptr
		switch {
		case i == targetCell && targetHWND != 0:
			hwnd = targetHWND
		case len(others) > 0:
			hwnd, others = others[0], others[1:]
		default:
			continue // 同屏可排窗口不够，这一格留空
		}
		r := cellRect(c, work)
		if !placeWindow(hwnd, r) {
			// 窗口可能在浮层打开后被关掉：只记日志，不计入成功数。
			logger.W("QuickDock: 排版窗口 %d 失败（目标 %+v）", hwnd, r)
			continue
		}
		res.Applied++
		res.Titles = append(res.Titles, w32.GetWindowText(w32.HWND(hwnd)))
	}
	logger.I("QuickDock: 排版 %d/%d 格到屏#%d（目标格=%d，同屏候选=%d）", res.Applied, len(cells), idx, targetCell, res.Peers)
	return res, nil
}

// TilingResult 一次排版的执行结果（ApiResult.data，前端据此回显「排了哪几个窗口」）。
type TilingResult struct {
	Applied int      // 实际成功贴屏的窗口数
	Peers   int      // 同屏可参与自动填充的候选窗口总数（不含目标窗口）
	Titles  []string // 参与排版的窗口标题，按落位顺序
}

// TilingInfo 描述「这次排版作用于哪块屏、还有几个窗口会被一起排」，供浮层预览。
type TilingInfo struct {
	ScreenW int // 目标屏工作区物理宽度
	ScreenH int // 目标屏工作区物理高度
	Peers   int // 同屏可参与填充的候选窗口数（不含目标窗口）
}

// PlanTiling 返回本次排版的目标屏几何与候选窗口数。
//
// 前端按 ScreenW/ScreenH 的比例画模板缩略图，让「缩略图比例 = 实际落位比例」；
// Peers 让用户提前知道这块屏还有几个窗口会被一起排——格子空着时能明白是窗口不够，
// 而不是「排版没按模板生效」。
func PlanTiling(targetHWND uintptr, useCursorScreen bool) (TilingInfo, error) {
	work, _, err := tilingWorkArea(targetHWND, useCursorScreen)
	if err != nil {
		return TilingInfo{}, err
	}
	return TilingInfo{ScreenW: work.Width, ScreenH: work.Height, Peers: len(fillCandidates(work, targetHWND))}, nil
}

// tilingWorkArea 解析排版目标屏的工作区（物理像素）与屏索引。
// useCursorScreen=true 取光标所在屏（浮层总在光标屏弹出，所见即所得）；否则取目标窗口所在屏。
func tilingWorkArea(targetHWND uintptr, useCursorScreen bool) (Rect, int, error) {
	screens := allScreens()
	if len(screens) == 0 {
		return Rect{}, -1, fmt.Errorf("no screens available")
	}
	idx := 0
	if useCursorScreen {
		idx = screenIndexAtCursor(screens)
	} else if targetHWND != 0 {
		idx = screenIndexOf(targetHWND, screens)
	}
	b := screens[idx].PhysicalWorkArea
	return Rect{b.X, b.Y, b.Width, b.Height}, idx, nil
}

// cellRect 把归一化格子换算成工作区内的物理像素矩形。
//
// 用边界取整（x0/x1 各自取整后相减）而不是对 x 与 w 分别取整：屏幕宽高不能被格数整除时
// （如 1366/3），后者会在格子交界处留下 1px 缝或 1px 重叠，看起来「没贴严」。
func cellRect(c TilingCell, work Rect) Rect {
	x0 := work.X + roundInt(c.X*float64(work.Width))
	y0 := work.Y + roundInt(c.Y*float64(work.Height))
	x1 := work.X + roundInt((c.X+c.W)*float64(work.Width))
	y1 := work.Y + roundInt((c.Y+c.H)*float64(work.Height))
	return Rect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

// roundInt 四舍五入到 int（格子坐标恒为非负，无需处理负数取整差异）。
func roundInt(v float64) int {
	return int(math.Round(v))
}

// placeWindow 把一个窗口落到目标矩形，返回是否成功。
//
// 两个关键点，缺任一个都会表现成「排版没按模板」：
//  1. 先彻底清除最大化/最小化状态。仅 ShowWindow(SW_RESTORE) 对自定义 frame（Chrome / VS Code）
//     或被 Aero Snap 吸附的窗口不够——WS_MAXIMIZE 仍生效，SetWindowPos 的尺寸被忽略。
//  2. SetWindowPos 必须带 SWP_NOACTIVATE：否则被排窗口会抢到前台，排版浮层随即失焦自动隐藏，
//     用户看不到整屏整理的过程（以为没生效）。SWP_NOOWNERZORDER 防止 owner 窗口被连动改 Z 序。
func placeWindow(hwnd uintptr, r Rect) bool {
	h := w32.HWND(hwnd)
	unmaximize(h)
	flags := uint(w32.SWP_NOACTIVATE | w32.SWP_NOOWNERZORDER)
	if w32.SetWindowPos(h, w32.HWND_TOP, r.X, r.Y, r.Width, r.Height, flags) {
		return true
	}
	// 少数窗口首次设位会失败（扩展帧 / 延迟创建的 frame 尚未就绪）：复位后再试一次。
	unmaximize(h)
	return w32.SetWindowPos(h, w32.HWND_TOP, r.X, r.Y, r.Width, r.Height, flags)
}

// unmaximize 把窗口恢复到「普通（非最大化、非最小化）」状态，让后续 SetWindowPos 的尺寸生效。
func unmaximize(h w32.HWND) {
	if !w32.IsZoomed(h) && !isMinimized(uintptr(h)) {
		return
	}
	w32.ShowWindow(h, w32.SW_RESTORE)
	if !w32.IsZoomed(h) {
		return
	}
	// SW_RESTORE 没能清掉 WS_MAXIMIZE（部分窗口确实如此）：用 SetWindowPlacement 显式改 ShowCmd。
	var wp w32.WINDOWPLACEMENT
	wp.Length = uint32(unsafe.Sizeof(wp))
	if w32.GetWindowPlacement(h, &wp) {
		wp.ShowCmd = w32.SW_SHOWNORMAL
		w32.SetWindowPlacement(h, &wp)
	}
}

// fillCandidates 返回可以拿去填充「其余格子」的窗口句柄，按 Z 序（最近用过在前）。
//
// 只取中心落在 work 工作区内的窗口（即与布局同一块屏），并排除目标窗口本身与最小化窗口
// （最小化窗口被 SetWindowPos 拉起来会突然弹到用户面前，不该被自动填进来）。
// EnumWindows 本身已按 Z 序自上而下枚举，且已排除本进程窗口、无标题窗口、工具窗口。
func fillCandidates(work Rect, exclude uintptr) []uintptr {
	infos, err := EnumWindows()
	if err != nil {
		return nil
	}
	out := make([]uintptr, 0, len(infos))
	for _, w := range infos {
		if w.HWND == exclude || isMinimized(w.HWND) {
			continue
		}
		cx := w.Rect.X + w.Rect.Width/2
		cy := w.Rect.Y + w.Rect.Height/2
		if cx >= work.X && cx < work.X+work.Width && cy >= work.Y && cy < work.Y+work.Height {
			out = append(out, w.HWND)
		}
	}
	return out
}

// procIsIconic user32!IsIconic：w32 包未封装，用于识别最小化窗口。
var procIsIconic = syscall.NewLazyDLL("user32.dll").NewProc("IsIconic")

// isMinimized 判断窗口是否处于最小化状态。
func isMinimized(hwnd uintptr) bool {
	r, _, _ := procIsIconic.Call(hwnd)
	return r != 0
}

// screenIndexAtCursor 返回光标当前所在屏索引；光标不在任意屏内时取中心最近者。
// 用于「跟随鼠标屏」模式，使批量网格铺到用户正注视的显示器。
func screenIndexAtCursor(screens []*application.Screen) int {
	x, y, ok := w32.GetCursorPos()
	if !ok {
		return 0
	}
	for i, s := range screens {
		b := s.PhysicalWorkArea
		if x >= b.X && x < b.X+b.Width && y >= b.Y && y < b.Y+b.Height {
			return i
		}
	}
	best := 0
	bestDist := int64(1) << 62
	for i, s := range screens {
		b := s.PhysicalWorkArea
		dx := b.X + b.Width/2 - x
		dy := b.Y + b.Height/2 - y
		d := int64(dx)*int64(dx) + int64(dy)*int64(dy)
		if d < bestDist {
			bestDist, best = d, i
		}
	}
	return best
}
