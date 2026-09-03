//go:build windows

package sysutil

import (
	"os/exec"
	"syscall"
)

const (
	// createNoWindow = CREATE_NO_WINDOW：不创建控制台，从根上杜绝黑框。
	// 优于 syscall.SysProcAttr{HideWindow:true}（只设 STARTF_USESHOWWINDOW/SW_HIDE）：
	// 后者仍会创建控制台，只是不显示，残留的 console 句柄会干扰管道读取。
	createNoWindow = 0x08000000
	// detachedProcess = DETACHED_PROCESS：子进程脱离父进程组，
	// 父进程退出/被杀时子进程继续存活（用于“通过 QuickDock 打开第三方软件”）。
	detachedProcess = 0x00000008
)

// Hide 附加“隐藏控制台窗口”属性。
// 用 |= 合并而非整体覆盖：调用方可能已在 SysProcAttr 上手写了 CmdLine 等字段
// （如 explorer /select，" 必须手写完整命令行否则含空格路径会被 argv 规则拆散），
// 整体覆盖会静默丢掉这些字段。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	attr(cmd).CreationFlags |= createNoWindow
	return cmd
}

// Detach 让子进程脱离父进程组，父进程退出时子进程不会被连带杀掉。
func Detach(cmd *exec.Cmd) *exec.Cmd {
	attr(cmd).CreationFlags |= detachedProcess
	return cmd
}

func attr(cmd *exec.Cmd) *syscall.SysProcAttr {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	return cmd.SysProcAttr
}
