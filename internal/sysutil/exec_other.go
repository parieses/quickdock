//go:build !windows

package sysutil

import (
	"os/exec"
	"runtime"
)

// Hide 非 Windows 平台无控制台窗口概念，无操作（保持原有行为）。
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }

// Detach 非 Windows 平台无操作（保持原有行为，不额外 setpgid）。
func Detach(cmd *exec.Cmd) *exec.Cmd { return cmd }

// OpenDetached 非 Windows 平台以系统默认方式打开目标（等价于 xdg-open / open），
// 不涉及作业脱离（Windows 专属问题）。
func OpenDetached(target string, workingDir string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", target)
	} else {
		cmd = exec.Command("xdg-open", target)
	}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	return cmd.Start()
}
