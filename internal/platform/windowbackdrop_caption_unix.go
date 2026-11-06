//go:build !windows

package platform

// StripGhostCaptionButtons 非 Windows 下是 no-op：幽灵关闭按钮是 Win11 DWM
// 在「无边框 + 系统材质」窗口上的特有行为，其它平台没有对应处理。
func StripGhostCaptionButtons(hwnd uintptr) {}
