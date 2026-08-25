//go:build !windows

package plugin

// 非 Windows 平台空实现：macOS/Linux 上进程退出即释放文件句柄，
// 不存在 Windows 的「运行中 exe 被锁导致删除失败」问题。
// Windows 实现在 installer_windows.go。

func killProcessTree(pid int) {}

func killProcessesLockingDir(dir string) {}