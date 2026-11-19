//go:build windows

package platform

// 多显示器定位。
//
// 屏幕几何数据全部来自框架 ScreenManager（app.Screen.GetAll() 的
// PhysicalBounds / PhysicalWorkArea / Bounds），不再手写 MONITORINFO 结构与
// MonitorFromPoint/GetMonitorInfoW 调用；仅保留三个无法跨平台的最小 Win32
// 操作（取光标坐标 / 取窗口物理矩形 / 物理坐标移动窗口），且统一复用框架公开的
// pkg/w32 封装，不再自带 unsafe proc 声明。
//
// 注意：不要用 w32.MonitorFromPoint——该封装把 x/y 当两个独立 uintptr 传参，
// 而 API 期望打包成 POINT（两个 int32 合一个 8 字节），64 位下会传错 dwFlags。
// 本实现改为遍历框架屏幕数据做命中判定，从根上绕开该问题。

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

// cursorPosWarnOnce 让 GetCursorPos 失败只告警一次。
// 该函数在每次显示浮窗时都会被调用，若环境性失败（会话锁定 / 非交互会话）
// 持续存在，逐次打 W 会把日志刷满，反而淹没其它线索。
var cursorPosWarnOnce sync.Once

// cursorScreen 返回鼠标光标所在的屏幕：
// 优先命中 PhysicalBounds，未命中（光标在屏幕间隙等）取中心最近者。
func cursorScreen() *application.Screen {
	app := application.Get()
	if app == nil {
		return nil
	}
	x, y, ok := w32.GetCursorPos()
	if !ok {
		cursorPosWarnOnce.Do(func() {
			logger.W("QuickDock: GetCursorPos 失败（会话可能已锁定或非交互），浮窗沿用默认位置；同类失败不再重复记录")
		})
		return nil
	}

	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return nil
	}

	var best *application.Screen
	var bestDist int64 = 1 << 62
	for _, s := range screens {
		b := s.PhysicalBounds
		if x >= b.X && x < b.X+b.Width && y >= b.Y && y < b.Y+b.Height {
			return s
		}
		dx := b.X + b.Width/2 - x
		dy := b.Y + b.Height/2 - y
		d := int64(dx)*int64(dx) + int64(dy)*int64(dy)
		if d < bestDist {
			bestDist, best = d, s
		}
	}
	return best
}

// windowPhysicalSize 取窗口当前物理尺寸（隐藏窗口也返回最后已知值）。
func windowPhysicalSize(win *application.WebviewWindow) (w, h int, ok bool) {
	nw := win.NativeWindow()
	if nw == nil {
		return 0, 0, false
	}
	rect := w32.GetWindowRect(w32.HWND(uintptr(nw)))
	if rect == nil || rect.Right <= rect.Left || rect.Bottom <= rect.Top {
		return 0, 0, false
	}
	return int(rect.Right - rect.Left), int(rect.Bottom - rect.Top), true
}

// setWindowPosPhysical 用物理像素坐标移动窗口。
// 绕过 Wails SetPosition 的 DIP 转换，避免高 DPI 下二次缩放。
func setWindowPosPhysical(win *application.WebviewWindow, x, y int) {
	nw := win.NativeWindow()
	if nw == nil {
		win.SetPosition(x, y)
		return
	}
	w32.SetWindowPos(w32.HWND(uintptr(nw)), 0, x, y, 0, 0,
		w32.SWP_NOSIZE|w32.SWP_NOZORDER)
}

// resizeWindowPhysical 用物理像素改窗口尺寸（与 setWindowPosPhysical 同源，绕过 DIP 转换）。
func resizeWindowPhysical(win *application.WebviewWindow, w, h int) {
	nw := win.NativeWindow()
	if nw == nil {
		return
	}
	w32.SetWindowPos(w32.HWND(uintptr(nw)), 0, 0, 0, w, h,
		w32.SWP_NOMOVE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
}

// clampAxis 把窗口某一轴收拢进 [origin, origin+extent]。
//
// 窗口比工作区还大时贴原点（标题栏必须能点到），否则先按右下边界回推、再按左上兜底。
// 两端各自夹取、互不覆盖 —— 旧实现是顺序赋值：窗口超宽时「先贴左，再按右边回推」，
// 结果左边缘又被推到屏幕外，等于两头都露。
func clampAxis(origin, extent, size, pos int) int {
	max := origin + extent - size
	if max < origin {
		max = origin
	}
	if pos > max {
		pos = max
	}
	if pos < origin {
		pos = origin
	}
	return pos
}

// SetWindowToCursorScreen 将窗口居中到鼠标光标所在显示器的工作区。
func SetWindowToCursorScreen(win *application.WebviewWindow, winWidth, winHeight int) {
	if win == nil {
		return
	}
	screen := cursorScreen()
	if screen == nil {
		return
	}
	work := screen.PhysicalWorkArea

	if physW, physH, ok := windowPhysicalSize(win); ok {
		// 先收缩再居中：小屏 + 高 DPI（如 1366×768@125%，工作区约 1093×614 物理像素）下，
		// 浮窗按 DIP 定尺寸、乘缩放后可能比工作区还大。不先缩小，怎么夹取都会有一截
		// 落在屏幕外，表现为标题栏拖不到、底部按钮点不着。
		if physW > work.Width || physH > work.Height {
			if physW > work.Width {
				physW = work.Width
			}
			if physH > work.Height {
				physH = work.Height
			}
			resizeWindowPhysical(win, physW, physH)
		}
		x := clampAxis(work.X, work.Width, physW, work.X+(work.Width-physW)/2)
		y := clampAxis(work.Y, work.Height, physH, work.Y+(work.Height-physH)/2)
		setWindowPosPhysical(win, x, y)
		return
	}

	// 兜底：无原生句柄时用框架 DIP 几何数据居中
	// （旧实现此处误把物理坐标当 DIP 传给 SetPosition，高 DPI 下位置偏差）
	b := screen.Bounds
	w, h := winWidth, winHeight
	if w > b.Width {
		w = b.Width
	}
	if h > b.Height {
		h = b.Height
	}
	if w != winWidth || h != winHeight {
		win.SetSize(w, h) // DIP 空间，直接把收缩后的尺寸交回框架
	}
	x := clampAxis(b.X, b.Width, w, b.X+(b.Width-w)/2)
	y := clampAxis(b.Y, b.Height, h, b.Y+(b.Height-h)/2)
	win.SetPosition(x, y)
}
