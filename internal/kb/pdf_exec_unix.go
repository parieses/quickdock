//go:build !windows

package kb

import "os/exec"

// hideCmdWindow 非 Windows 平台无需处理（没有控制台窗口的概念）。
func hideCmdWindow(c *exec.Cmd) {}
