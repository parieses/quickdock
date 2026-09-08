//go:build windows

package plugin

import (
	"fmt"
	"strconv"
	"strings"

	"quickdock/internal/sysutil"
)

// killProcessTree 强杀指定 PID 的整棵进程树（taskkill /F /T）。
// 用于安装/更新时兜底清理 stopPlugin 的 Process.Kill 之后仍残留的子进程，
// 释放它们持有的文件句柄（解决 Windows 上 Kill 主进程后子进程继续占用 exe 文件的问题）。
// CREATE_NO_WINDOW 避免弹出黑色控制台窗口。
// taskkill 失败（被忽略/AV 介入）时补一发 PowerShell Stop-Process，不再静默吞错。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := sysutil.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		// 兜底：PowerShell Stop-Process（含子进程树需 taskkill，这里先做主进程+尝试全部同名）
		ps := fmt.Sprintf("Get-Process -Id %d -ErrorAction SilentlyContinue | Stop-Process -Force", pid)
		cmd2 := sysutil.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		_ = cmd2.Run()
	}
}

// killProcessesLockingDir 强杀所有可执行文件位于 dir 内的进程。
// 清理 manager 未跟踪的孤儿插件进程（主程序异常退出残留、手动调试拉起的实例）：
// 这些 PID 不在 m.plugins 里，按 PID 兜底杀不到，导致 Windows 上目录被锁无法备份 rename。
// 按路径匹配而非进程名，避免误杀共享同名 exe（如多个插件各自的 system-tools.exe）的其他插件。
func killProcessesLockingDir(dir string) {
	// 单引号包裹路径防空格/特殊字符；插件路径不含单引号（pluginID 已过白名单校验），
	// 这里仍做替换防御性处理
	safe := strings.ReplaceAll(strings.TrimSuffix(dir, "\\"), "'", "")
	ps := fmt.Sprintf(
		"Get-Process | Where-Object { $_.Path -like '%s*' } | Stop-Process -Force",
		safe,
	)
	cmd := sysutil.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	_ = cmd.Run()
}
