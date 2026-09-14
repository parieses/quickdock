//go:build windows

package plugin

import (
	"fmt"
	"strconv"
	"strings"

	"quickdock/internal/sysutil"

	"golang.org/x/sys/windows"
)

// killProcessTree 强杀指定 PID 的整棵进程树（taskkill /F /T）。
// 用于安装/更新时兜底清理 stopPlugin 的 Process.Kill 之后仍残留的子进程，
// 释放它们持有的文件句柄（解决 Windows 上 Kill 主进程后子进程继续占用 exe 文件的问题）。
// CREATE_NO_WINDOW 避免弹出黑色控制台窗口。
// taskkill 失败时先确认进程是否真的还在：taskkill 对「进程已不存在」同样返回非零，
// 不加区分就一律走 PowerShell 兜底，会白白多起一个 powershell.exe（冷启约 1.3s）。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	cmd := sysutil.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		// 进程已退出（或从未存在）：没有可杀的对象，直接收工。
		if !processAlive(pid) {
			return
		}
		// 进程仍在但 taskkill 没干掉（被忽略/AV 介入），补一发 PowerShell Stop-Process。
		ps := fmt.Sprintf("Get-Process -Id %d -ErrorAction SilentlyContinue | Stop-Process -Force", pid)
		cmd2 := sysutil.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		_ = cmd2.Run()
	}
}

// processAlive 探测 PID 对应进程是否仍存活。
// 以 SYNCHRONIZE 权限打开句柄后零超时等待：句柄已 signaled（返回 WAIT_OBJECT_0，即 0）
// 表示进程已退出；WAIT_TIMEOUT 表示仍在运行。OpenProcess 失败同样视为不存在。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, 0)
	return err == nil && event != 0
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
