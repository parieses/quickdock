//go:build windows

package sysutil

import (
	"time"

	"golang.org/x/sys/windows"
)

// WaitProcessExit 等待 pid 对应的进程退出，最多等 timeout。
// 返回 true 表示已退出，false 表示超时后仍在运行。
//
// 用于「重启应用」：新进程必须先确认旧进程真正退出，否则 Wails 的单实例互斥
// （application.New 里的 CreateMutex）会把新进程当成重复启动直接 os.Exit(0)。
//
// 用 SYNCHRONIZE 权限开句柄后 WaitForSingleObject：进程退出时句柄被信号化，
// 比轮询进程列表便宜，也没有「枚举间隙」的窗口期。
// 拿不到句柄（进程已不存在）一律视为已退出——重启场景下宁可继续启动。
func WaitProcessExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)

	if timeout < 0 {
		timeout = 0
	}
	event, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	if err != nil {
		return false
	}
	return event == windows.WAIT_OBJECT_0
}
