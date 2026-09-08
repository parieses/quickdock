//go:build darwin || linux

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

// StartDetached 非 Windows 平台无“作业/控制台脱离”概念，等价于普通启动并异步回收
// 进程句柄（避免僵尸/句柄泄漏），保持与 Windows 相同的调用方语义。
func StartDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
