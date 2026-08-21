//go:build !windows

package plugin

// killProcessTree 非 Windows 平台空实现：
// Unix 上 Process.Kill(SIGKILL) 即能释放句柄，且无子进程树占用文件的问题。
func killProcessTree(pid int) {}
