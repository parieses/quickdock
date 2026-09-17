//go:build darwin || linux

package sysutil

// InitHiddenConsole 非 Windows 平台没有"控制台窗口"概念，恒为无操作，返回 false。
// 保留同名函数只是为了让 main 里的调用点跨平台统一（不引入 build tag 到 main）。
func InitHiddenConsole() bool { return false }
