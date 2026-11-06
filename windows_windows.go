//go:build windows

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
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
// 并按启动期判定的材质给浮窗套上 Acrylic。
//
// 这里改成**保留式赋值**（原先是 opts.Windows = WindowsWindow{...} 整字段覆盖）：
// 整字段覆盖会把调用方预设的 BackdropType 等 Windows 属性一并清掉，材质就永远
// 加不上去。只设自己负责的字段，别的属性由别处累加。
func applyWindowPlatformOptions(opts *application.WebviewWindowOptions) {
	opts.Windows.HiddenOnTaskbar = true
	applyWindowBackdrop(opts, true)
}
