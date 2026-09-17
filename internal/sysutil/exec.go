// Package sysutil 封装跨平台子进程创建，Windows 上统一隐藏控制台窗口。
//
// 背景：QuickDock 正式版（wails3 build）以 GUI 子系统链接，自身没有控制台。
// 此时拉起 console 子系统程序（powershell / netstat / tasklist / node / php ...），
// Windows 会为它新建一个控制台并短暂显示黑框。dev 版（wails3 dev）进程自带
// 控制台、子进程直接附着，因此只在打包后的正式版复现。
//
// 隐藏策略（Windows，见 console_windows.go）：正式版启动时由 main 调用
// InitHiddenConsole() 为宿主自建一个不可见控制台，之后所有子进程默认继承它，
// 全系统只留 1 个 conhost.exe。若初始化失败（或 dev 版已有可见控制台），
// 则自动退回 CREATE_NO_WINDOW 兜底——功能一致，只是每个子进程各带一个 conhost。
// **调用方无需关心走哪条路径，一律用 Command/CommandContext/Hide 即可。**
//
// 本包是唯一的隐藏属性来源：业务代码禁止再直接写
// syscall.SysProcAttr{CreationFlags: ...} 或 {HideWindow: true}，
// 否则非 Windows 平台（darwin/linux）因该字段不存在而无法编译。
package sysutil

import (
	"context"
	"os/exec"
)

// Command 等价于 exec.Command，并附加“隐藏控制台窗口”属性。
func Command(name string, arg ...string) *exec.Cmd {
	return Hide(exec.Command(name, arg...))
}

// CommandContext 等价于 exec.CommandContext，并附加“隐藏控制台窗口”属性。
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return Hide(exec.CommandContext(ctx, name, arg...))
}
