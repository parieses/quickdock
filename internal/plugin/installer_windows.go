//go:build windows

package plugin

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

// killProcessesLockingDir 强杀所有可执行文件位于 dir 内的进程。
// 清理 manager 未跟踪的孤儿插件进程（主程序异常退出残留、手动调试拉起的实例）：
// 这些 PID 不在 m.plugins 里，按 PID 兜底杀不到，导致 Windows 上目录被锁无法备份 rename。
// 按路径匹配而非进程名，避免误杀共享同名 exe（如 _shared/system-tools.exe）的其他插件。
func killProcessesLockingDir(dir string) {
	// 单引号包裹路径防空格/特殊字符；插件路径不含单引号（pluginID 已过白名单校验），
	// 这里仍做替换防御性处理
	safe := strings.ReplaceAll(strings.TrimSuffix(dir, "\\"), "'", "")
	ps := fmt.Sprintf(
		"Get-Process | Where-Object { $_.Path -like '%s*' } | Stop-Process -Force",
		safe,
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	_ = cmd.Run()
}
