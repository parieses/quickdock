//go:build !windows

package platform

import "github.com/wailsapp/wails/v3/pkg/application"

// ShowFloatingWindow 非 Windows 下没有 WebView2 合成层空帧问题，直接定位 + 显示。
// 保留同名函数是为了让 tray.go 的调用点不必加平台分支。
func ShowFloatingWindow(win *application.WebviewWindow, winWidth, winHeight int) {
	if win == nil {
		return
	}
	SetWindowToCursorScreen(win, winWidth, winHeight)
	win.Show()
	win.Focus()
}
