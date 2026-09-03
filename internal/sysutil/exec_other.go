//go:build !windows

package sysutil

import "os/exec"

// Hide 非 Windows 平台无控制台窗口概念，无操作（保持原有行为）。
func Hide(cmd *exec.Cmd) *exec.Cmd { return cmd }

// Detach 非 Windows 平台无操作（保持原有行为，不额外 setpgid）。
func Detach(cmd *exec.Cmd) *exec.Cmd { return cmd }
