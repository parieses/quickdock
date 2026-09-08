//go:build !windows

package services

// applyWindowTheme 非 Windows（mac/Linux）下无原生 DWM 沉浸式暗色机制，
// 标题栏跟随由 Wails 原生窗口与 SetBackgroundColour 处理，此处为空实现。
func applyWindowTheme(hwnd uintptr, isDark bool) {}
