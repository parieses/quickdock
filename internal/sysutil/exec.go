// Package sysutil 封装跨平台子进程创建，Windows 上统一隐藏控制台窗口。
//
// 背景：QuickDock 正式版（wails3 build）以 GUI 子系统链接，自身没有控制台。
// 此时拉起 console 子系统程序（powershell / netstat / tasklist / node / php ...），
// Windows 会为它新建一个控制台并短暂显示黑框。dev 版（wails3 dev）进程自带
// 控制台、子进程直接附着，因此只在打包后的正式版复现。
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
