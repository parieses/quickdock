//go:build !windows

package aitools

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让子进程成为新进程组的组长，超时时可以整组杀掉。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 向进程组发 SIGKILL（负 pid 表示整组）。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}
