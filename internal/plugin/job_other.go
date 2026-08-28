//go:build !windows

package plugin

// assignProcessToJob 在非 Windows 平台为空实现：Job Object 是 Windows 机制，
// 其它平台插件子进程由各自 supervisor 逻辑管理。
func assignProcessToJob(_ uintptr) {}
