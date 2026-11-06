//go:build !windows

package platform

// SupportsWindowBackdrop 非 Windows 平台没有 Mica / Acrylic，恒 false。
// 保留同名函数是为了让启动期与 services 层不必按平台写条件编译。
func SupportsWindowBackdrop() bool { return false }
