//go:build !windows

package env

// 非 Windows 平台暂不做系统级环境变量注册（QuickDock 当前仅面向 Windows）。
// 这里保留空实现，保证包在其它平台仍可编译。
func sysRegisterPath(dir string) error   { return nil }
func sysUnregisterPath(dir string) error { return nil }
func sysReadPath() (string, bool)        { return "", false }
