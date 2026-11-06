//go:build windows

package platform

import "github.com/wailsapp/wails/v3/pkg/w32"

// StripGhostCaptionButtons 移除无边框窗口右上角被 DWM 画出来的系统关闭按钮（红 ×）。
//
// 根因：Wails 的 Frameless 窗口其实保留着完整的 WS_OVERLAPPEDWINDOW 样式
// （含 WS_CAPTION / WS_SYSMENU），只是靠 WM_NCCALCSIZE 把标题栏裁掉；正常情况下
// DWM 不再绘制标题按钮。但窗口一旦设置系统材质（DWMWA_SYSTEMBACKDROP_TYPE），
// Windows 11 会按「带标题栏的窗口」对待，把原生关闭按钮直接画在内容之上。
//
// 处理：按 Raymond Chen 的结论（"The only way to get rid of the Close button is
// not to have a System menu at all"），移除 WS_SYSMENU。代价是 Alt+F4 的 SC_CLOSE
// 通道失效；浮窗本就失焦即隐藏、Esc / 热键均可收起，插件窗口有关闭按钮，影响可接受。
//
// hwnd 传 0 或当前未套材质时是 no-op，调用方不必自己判断。
func StripGhostCaptionButtons(hwnd uintptr) {
	if hwnd == 0 || EffectiveWindowBackdrop() == BackdropOff {
		return
	}
	style := w32.GetWindowLongPtr(hwnd, w32.GWL_STYLE)
	w32.SetWindowLongPtr(hwnd, w32.GWL_STYLE, style&^uintptr(w32.WS_SYSMENU))
	// 触发 WM_NCCALCSIZE / 框架重算，让按钮立刻消失（窗口可能已显示）。
	w32.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		uint(w32.SWP_NOMOVE|w32.SWP_NOSIZE|w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_FRAMECHANGED))
}
