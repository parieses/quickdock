//go:build windows

package services

import (
	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// captionColor 从与内容区同源的中性色算出 DWMWA_CAPTION_COLOR 需要的 COLORREF（0x00BBGGRR）。
// 深色用 #14171f（--color-bg-secondary），浅色用 #f6f7f9（--color-bg-primary），
// 与 style.css 的窗口底色同源，标题栏不再被 Mica 按壁纸染成暖橙。
func captionColor(isDark bool) uint32 {
	r, g, b := byte(0xf6), byte(0xf7), byte(0xf9)
	if isDark {
		r, g, b = 0x14, 0x17, 0x1f
	}
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

// applyWindowTheme 在 Windows 下通过 w32.SetTheme 让原生标题栏跟随 App 主题：
// 内部先 DwmSetWindowAttribute(20, dark) 设沉浸式暗色属性，再 SetMenuTheme 调
// AllowDarkModeForWindow + SetWindowTheme + InvalidateRect，让非客户区（标题栏）真正生效。
//
// 材质档下额外设 DWMWA_CAPTION_COLOR：Mica / Acrylic 会按壁纸给标题栏取色
// （高饱和壁纸下整条变橙黄，与冷色 UI 冲突），显式设成中性色可覆盖。
// 必须在 SetTheme 之后设置——SetTheme 内部会重绘标题栏，顺序反了会被重置。
// DWMWA_CAPTION_COLOR 需 Win11 22H2+，与材质能力门槛一致；未生效时 DWM 静默忽略，
// 不会破坏 SetTheme 的结果。
func applyWindowTheme(hwnd uintptr, isDark bool) {
	w32.SetTheme(hwnd, isDark)
	if platform.EffectiveWindowBackdrop() == platform.BackdropOff {
		return
	}
	c := captionColor(isDark)
	// SetTitleBarColour 是 w32 对 DwmSetWindowAttribute(hwnd, 35, &c, 4) 的封装。
	w32.SetTitleBarColour(hwnd, c)
}
