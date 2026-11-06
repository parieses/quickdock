//go:build windows

package platform

import (
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// backdropRestoreDelay 是「窗口显示/激活 → 重新写回材质」之间的等待。
//
// Wails 处理 WM_ACTIVATE 时**先** emit 事件、**随后**才调 DwmExtendFrameIntoClientArea
// 补圆角与阴影（webview_window_windows.go 的 WM_ACTIVATE 分支）。若在事件回调里同步
// 重写属性，紧接着那次调用会再把它顶掉，等于没写。延后一帧多一点，等消息处理结束。
const backdropRestoreDelay = 40 * time.Millisecond

// ReapplyWindowBackdrop 把启动期选定的系统材质重新写回窗口。
//
// 为什么需要：DWM 的 DWMWA_SYSTEMBACKDROP_TYPE 不是「设一次就永久」的属性。
// 无边框浮窗每次被激活，Wails 都会调一次 DwmExtendFrameIntoClientArea；这次调用之后
// 材质会失效（窗口退回纯色）。表现就是「浮窗失焦隐藏、再按热键唤起，材质没了」。
// 主窗口 / SSH 窗口不是 Frameless，不走那条路径，所以一直正常。
func ReapplyWindowBackdrop(win *application.WebviewWindow) {
	if win == nil {
		return
	}
	bt, ok := windowBackdropType()
	if !ok {
		return
	}
	hwnd := win.NativeWindow()
	if hwnd == nil {
		return
	}
	w32.EnableTranslucency(uintptr(hwnd), uint32(bt))
}

// KeepBackdropOnShow 在窗口每次显示 / 重新获得焦点后把材质写回去。
//
// 只挂在需要它的窗口上（无边框浮窗、插件独立窗口）。窗口 Hide/Show 复用不会重建
// WebView，挂一次即长期有效；未启用材质时整个函数是 no-op。
func KeepBackdropOnShow(win *application.WebviewWindow) {
	if win == nil {
		return
	}
	if _, ok := windowBackdropType(); !ok {
		return
	}
	restore := func(*application.WindowEvent) {
		time.AfterFunc(backdropRestoreDelay, func() { ReapplyWindowBackdrop(win) })
	}
	win.OnWindowEvent(events.Common.WindowShow, restore)
	win.OnWindowEvent(events.Common.WindowFocus, restore)
}
