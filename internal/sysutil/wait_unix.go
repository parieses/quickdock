//go:build darwin || linux

package sysutil

import (
	"syscall"
	"time"
)

// WaitProcessExit 等待 pid 对应的进程退出，最多等 timeout。
// 返回 true 表示已退出，false 表示超时后仍在运行。
//
// 用于「重启应用」：新进程必须先确认旧进程真正退出，否则单实例检测会
// 把新进程当成重复启动并退出。
//
// Unix 没有等句柄的原语，用 kill(pid, 0) 轮询存在性——信号 0 不发送信号，
// 只做权限与存在性检查。50ms 间隔对重启场景足够（本机进程退出是毫秒级）。
func WaitProcessExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return true // ESRCH：进程已不存在
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
