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
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

// cursorScreen 返回鼠标光标所在的屏幕：
// 优先命中 PhysicalBounds，未命中（光标在屏幕间隙等）取中心最近者。
func cursorScreen() *application.Screen {
	app := application.Get()
	if app == nil {
		return nil
	}
	x, y, ok := w32.GetCursorPos()
	if !ok {
		logger.W("QuickDock: GetCursorPos failed, keeping default position")
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
		x := work.X + (work.Width-physW)/2
		y := work.Y + (work.Height-physH)/2
		// 收敛到工作区边界内
		if x < work.X {
			x = work.X
		}
		if y < work.Y {
			y = work.Y
		}
		if x+physW > work.X+work.Width {
			x = work.X + work.Width - physW
		}
		if y+physH > work.Y+work.Height {
			y = work.Y + work.Height - physH
		}
		setWindowPosPhysical(win, x, y)
		return
	}

	// 兜底：无原生句柄时用框架 DIP 几何数据居中
	// （旧实现此处误把物理坐标当 DIP 传给 SetPosition，高 DPI 下位置偏差）
	b := screen.Bounds
	x := b.X + (b.Width-winWidth)/2
	y := b.Y + (b.Height-winHeight)/2
	win.SetPosition(x, y)
}
