//go:build windows

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/platform"
)

// foregroundIsOwnedModal 判断当前前台窗口是否为被本应用窗口拥有的模态对话框
// （例如命令面板内插件 <input type="file"> 弹出的系统文件选择框）。
// 命令面板等窗口在失焦时会隐藏自身；若失焦是自家模态框打开所致，则不应隐藏，
// 否则会出现"在插件里选文件时页面被关掉"的问题。
//
// 判定方式：取前台窗口的拥有者（GW_OWNER=4）。其值为非 0 说明这是一个被其它窗口
// 拥有的弹出式窗口（如文件选择框），通常正属于本应用——此时不应隐藏面板。
func foregroundIsOwnedModal() bool {
	fg := w32.GetForegroundWindow()
	if fg == 0 {
		return false
	}
	return w32.GetWindow(fg, 4) != 0
}

// applyWindowPlatformOptions 设置 Windows 专属窗口属性：
// 浮窗（剪贴板/笔记/命令面板）从任务栏隐藏，只保留托盘与全局热键入口；
// 并按启动期判定的材质给浮窗套上 Acrylic（见 windows.go 的
// floatingWindowBackdropEnabled，关闭时 applyWindowBackdrop 直接返回）。
//
// 这里保留**保留式赋值**（原先是 opts.Windows = WindowsWindow{...} 整字段覆盖）：
// 整字段覆盖会把调用方预设的 Windows 属性一并清掉；只设自己负责的字段，
// 别的属性由别处累加。
func applyWindowPlatformOptions(opts *application.WebviewWindowOptions) {
	// 建窗即隐藏：浮窗的初始位置来自上次保存的坐标（通常就在屏幕内），
	// 若让 Wails 先按默认可见创建、再靠 init 里的 Hide() 收起来，创建那一瞬
	// 会在屏幕内闪一帧空窗口。Hidden 只影响首次创建；Show() 会自行把它置回 false。
	opts.Hidden = true
	opts.Windows.HiddenOnTaskbar = true
	applyWindowBackdrop(opts, true)
}

// stripGhostCaptionButtons 移除无边框浮窗右上角被 DWM 画出来的系统关闭按钮（红 ×）。
// 具体原理与代价见 platform.StripGhostCaptionButtons；这里只负责把 Wails 的窗口
// 句柄取出来转交，并尊重 floatingWindowBackdropEnabled 总开关。
func stripGhostCaptionButtons(win *application.WebviewWindow) {
	if win == nil || !floatingWindowBackdropEnabled {
		return
	}
	if hwnd := win.NativeWindow(); hwnd != nil {
		platform.StripGhostCaptionButtons(uintptr(hwnd))
	}
}

// keepBackdropOnShow 让系统材质在每次显示 / 重新激活后自动写回去。
//
// 为什么需要：DWM 材质不是设一次就永久的属性。无边框浮窗每次被激活，Wails 都会在
// WM_ACTIVATE 里调 DwmExtendFrameIntoClientArea 补圆角与阴影，这次调用之后材质失效 ——
// 表现就是「浮窗失焦隐藏、再唤起时材质没了」。主窗口 / SSH 不是 Frameless，不走那条
// 路径，所以一直正常（这也是用户反馈「只有浮窗会掉」的原因）。
//
// 材质未启用（off / 系统不支持）时不挂，行为与加材质功能之前完全一致。
func keepBackdropOnShow(win *application.WebviewWindow) {
	if win == nil || !floatingWindowBackdropEnabled {
		return
	}
	platform.KeepBackdropOnShow(win)
}
