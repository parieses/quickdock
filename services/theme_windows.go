//go:build windows

package services

import (
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// applyWindowTheme 在 Windows 下通过 w32.SetTheme 让原生标题栏跟随 App 主题：
// 内部先 DwmSetWindowAttribute(20, dark) 设沉浸式暗色属性，再 SetMenuTheme 调
// AllowDarkModeForWindow + SetWindowTheme + InvalidateRect，让非客户区（标题栏）真正生效。
func applyWindowTheme(hwnd uintptr, isDark bool) {
	w32.SetTheme(hwnd, isDark)
}
