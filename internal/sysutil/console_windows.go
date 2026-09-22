//go:build windows

package sysutil

import (
	"sync/atomic"
	"syscall"
	"time"
)

// 背景：QuickDock 发布版以 GUI 子系统链接（-H windowsgui），宿主自身没有控制台。
// Windows 此前靠 CREATE_NO_WINDOW 让每个控制台子进程"隐藏"，但该标志的语义是
// "分配一个新控制台但不显示"——于是每个子进程都配一个 conhost.exe（实测 6.5MB）。
// 47 插件全启用时 29 个后代进程白吃约 190MB（见 2026-09-17 实测）。
//
// 正解：宿主自己 AllocConsole() 建一个隐藏控制台，子进程**不带任何标志**默认继承它，
// 全系统只留 1 个 conhost。实测对照（pythonw 模拟 GUI 宿主，2026-09-17）：
//   - 宿主 AllocConsole + SW_HIDE 后，无标志启动 console 子进程 → 新增 conhost 0 个
//   - CREATE_NO_WINDOW 启动同样进程 → 新增 conhost 1 个
//   - DETACHED_PROCESS 启动 → 新增 0 个，但子孙进程会各自弹窗（本包禁用该路径）
//
// 额外收益：所有没走 sysutil 的裸 exec.Command（internal/db、internal/platform、
// internal/dsh helpers、internal/env/rabbitmq 等）也都会继承这个隐藏控制台，
// 不再各自弹黑框。

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	user32               = syscall.NewLazyDLL("user32.dll")
	procAllocConsole     = kernel32.NewProc("AllocConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procIsWindowVisible  = user32.NewProc("IsWindowVisible")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
)

const (
	swHide = 0
	// SetWindowPos 标志：强隐藏窗口，且不移动/缩放/改 Z 序/不抢激活
	swpHideWindow = 0x0080
	swpNoMove     = 0x0002
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// inheritHiddenConsole 为真时，子进程直接继承宿主的隐藏控制台，
// Hide() 不再叠加 CREATE_NO_WINDOW（叠加反而会新开 conhost）。
var inheritHiddenConsole atomic.Bool

// InitHiddenConsole 尝试为宿主进程建立一个不可见的控制台，返回是否已就绪。
//
// 三种情形：
//   - 宿主已有**可见**控制台（wails3 dev / 从终端启动）：返回 false，
//     保持既有 CREATE_NO_WINDOW 行为不变，避免 dev 下把插件日志混进终端。
//   - 宿主已有**不可见**控制台：直接复用，返回 true。
//   - 宿主无控制台（发布版 GUI 子系统）：AllocConsole 后立即 ShowWindow(SW_HIDE)。
//
// 返回 false 时 Hide() 会自动退回 CREATE_NO_WINDOW 兜底，功能不受影响。
func InitHiddenConsole() bool {
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible != 0 {
			return false
		}
		inheritHiddenConsole.Store(true)
		return true
	}

	if ret, _, _ := procAllocConsole.Call(); ret == 0 {
		return false
	}
	// AllocConsole 让独立进程 conhost 为宿主创建一个控制台窗口，默认**可见**。
	// 旧逻辑只在 GetConsoleWindow 立即返回非 0 句柄时才隐藏；但 conhost 建窗口
	// 与 GetConsoleWindow 取句柄是跨进程异步的，首次调用常返回 0（窗口尚未就绪），
	// 于是隐藏被整个跳过，窗口以可见态被 conhost 画出 → 启动闪一下黑框。
	// 改为轮询：绝大多数情况下首次 GetConsoleWindow 即命中、同步 SW_HIDE 赶在
	// conhost 首帧绘制前藏好，不会闪；偶发 0 时短暂重试（上限 100ms）直到藏住。
	// 再叠一层 SetWindowPos(SWP_HIDEWINDOW)，防止 conhost 首绘时把窗口重新拉出来。
	hidden := false
	for i := 0; i < 100; i++ {
		if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
			procShowWindow.Call(hwnd, swHide)
			procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
				swpHideWindow|swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate)
			hidden = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	inheritHiddenConsole.Store(true)
	return hidden
}

// consoleInherited 供同包 Hide() 判断是否需要 CREATE_NO_WINDOW。
func consoleInherited() bool { return inheritHiddenConsole.Load() }
