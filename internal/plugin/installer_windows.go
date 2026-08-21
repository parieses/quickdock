//go:build windows

package plugin

import (
	"os/exec"
	"strconv"
	"syscall"
)

// killProcessTree 强杀指定 PID 的整棵进程树（taskkill /F /T）。
// 用于安装/更新时兜底清理 stopPlugin 的 Process.Kill 之后仍残留的子进程，
// 释放它们持有的文件句柄（解决 Windows 上 Kill 主进程后子进程继续占用 exe 文件的问题）。
// CREATE_NO_WINDOW 避免弹出黑色控制台窗口。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = cmd.Run()
}
