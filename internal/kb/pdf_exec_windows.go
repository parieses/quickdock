//go:build windows

package kb

import (
	"os/exec"
	"syscall"
)

// hideCmdWindow 让 exec 起的控制台程序不弹黑框。
// 知识库扫描是后台行为，导入 PDF 时突然闪一个 cmd 窗口会显得很可疑。
func hideCmdWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
