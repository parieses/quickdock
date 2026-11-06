//go:build windows

package platform

import "github.com/wailsapp/wails/v3/pkg/w32"

// SupportsWindowBackdrop 报告系统是否支持 Mica / Acrylic 系统材质。
//
// 判据直接沿用 Wails 的 w32.SupportsBackdropTypes()（build ≥ 22621，即 Win11 22H2），
// 而不是自己再写一个版本判断：框架的 setBackdropType 在更低版本上会回退到 Win10 的
// ACCENT_ENABLE_BLURBEHIND 旧模糊，且回退分支根本不看 backdropType —— 选 Mica 和
// 选 Acrylic 出来是同一种效果，都不是 Win11 材质。用框架同一把尺子，才能避免在
// Win10 上给用户一个「能选但没用」的设置项。
func SupportsWindowBackdrop() bool {
	return w32.SupportsBackdropTypes()
}
