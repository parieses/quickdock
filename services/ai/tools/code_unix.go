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

// killProcessGroup 向进程组发 SIGKILL（负 pid 表示整组），失败回退到单进程 Kill。
//
// 与 Windows 侧同理：整组 kill 可能因为权限或进程已脱离进程组而失败，
// 失败后必须有兜底，否则调用方无限等 cmd.Wait()（也就永远不返回）。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}
