//go:build windows

package sysutil

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
)

const (
	// createNoWindow = CREATE_NO_WINDOW：不创建控制台，从根上杜绝黑框。
	// 优于 syscall.SysProcAttr{HideWindow:true}（只设 STARTF_USESHOWWINDOW/SW_HIDE）：
	// 后者仍会创建控制台，只是不显示，残留的 console 句柄会干扰管道读取。
	createNoWindow = 0x08000000
	// detachedProcess = DETACHED_PROCESS：子进程不继承父进程控制台（避免黑框与
	// 控制台 Ctrl 信号按进程组传播）。注意：它并不能让子进程脱离“作业(Job Object)”，
	// 父进程处于某作业时，作业关闭仍会连带杀掉子进程——必须与下面两个标志配合。
	detachedProcess = 0x00000008
	// createNewProcessGroup = CREATE_NEW_PROCESS_GROUP：让子进程成为新进程组的组长，
	// 父进程退出/被结束时，子进程不再随父进程组一起被终止。
	createNewProcessGroup = 0x00000200
	// createBreakawayFromJob = CREATE_BREAKAWAY_FROM_JOB：让子进程脱离父进程所属的作业
	// (Job Object)。当 QuickDock 自身运行在某个作业里（如 dev 运行时/某些启动器创建的作业），
	// 作业关闭会杀掉其中所有进程；此标志使第三方软件逃逸作业、独立存活。
	// 若父进程不在作业中 → 该标志被忽略，无副作用；
	// 若父进程在“不允许脱离”的作业中 → CreateProcess 会返回“拒绝访问”，此时由
	// StartDetached 自动去掉该标志重试（仅失去作业脱离能力，仍能正常启动）。
	createBreakawayFromJob = 0x01000000
)

// Hide 附加“隐藏控制台窗口”属性。
// 用 |= 合并而非整体覆盖：调用方可能已在 SysProcAttr 上手写了 CmdLine 等字段
// （如 explorer /select，" 必须手写完整命令行否则含空格路径会被 argv 规则拆散），
// 整体覆盖会静默丢掉这些字段。
func Hide(cmd *exec.Cmd) *exec.Cmd {
	attr(cmd).CreationFlags |= createNoWindow
	return cmd
}

// Detach 让子进程脱离父进程组与作业：父进程退出时子进程不会被连带杀掉。
// 组合 DETACHED_PROCESS + CREATE_NEW_PROCESS_GROUP + CREATE_BREAKAWAY_FROM_JOB。
func Detach(cmd *exec.Cmd) *exec.Cmd {
	a := attr(cmd)
	a.CreationFlags |= detachedProcess | createNewProcessGroup | createBreakawayFromJob
	return cmd
}

// StartDetached 以“隐藏 + 脱离父进程组/作业”的方式启动外部程序，并异步回收进程句柄
// （exec.Command.Start 之后若不 Wait，Windows 上内核句柄不会被释放，长期累积泄漏）。
// 用于“通过 QuickDock 打开第三方软件”，确保主程序退出后软件继续存活。
//
// 若父进程处于不允许脱离的作业（Job）中，CreateProcess 会因权限不足失败，
// 此时自动去掉 BREAKAWAY 标志用全新 Cmd 重试，保证仍能启动（仅失去作业脱离能力）。
func StartDetached(cmd *exec.Cmd) error {
	Hide(cmd)
	Detach(cmd)

	if err := cmd.Start(); err == nil {
		reap(cmd)
		return nil
	} else if !isAccessDenied(err) {
		return err
	}

	// 父进程处于不允许脱离的作业：去掉 BREAKAWAY 标志，用克隆的 Cmd 重试。
	clone := *cmd
	if sp := clone.SysProcAttr; sp != nil {
		sp2 := *sp
		sp2.CreationFlags &^= createBreakawayFromJob
		clone.SysProcAttr = &sp2
	}
	if err2 := clone.Start(); err2 != nil {
		return err2
	}
	reap(&clone)
	return nil
}

func reap(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}

// OpenDetached 以“系统默认方式”打开一个目标（应用 exe / 文件 / 目录 / 网页链接），
// 并让被打开的进程独立于 QuickDock 存活：主程序退出后它继续运行。
//
// 实现要点：统一经 explorer.exe 中转。
//   - explorer 是系统常驻进程，运行在 QuickDock 所属“作业(Job Object)”之外；
//   - 由它拉起的真正进程（应用/浏览器/文件夹/关联程序）属于 explorer 的上下文，
//     不对 QuickDock 的作业负责，因此主进程被作业关闭/重启时不会被连带杀掉。
//   - 相比直接用 exec.Command + CREATE_BREAKAWAY_FROM_JOB：当 QuickDock 自身处于
//     “不允许脱离”的作业时，breakaway 会失败且子进程仍被困在作业内；explorer 中转
//     从根上绕开了这个限制，覆盖 dev 运行时的作业场景。
//   - 用 Hide 避免 explorer 弹出多余控制台。
// 注意：终端命令（命令型项目）不要走这里——它需要在终端里执行整条命令，
// 应使用 startDetached；本函数仅用于“系统默认打开”。
func OpenDetached(target string, workingDir string) error {
	c := exec.Command("explorer.exe", target)
	if workingDir != "" {
		c.Dir = workingDir
	}
	Hide(c)
	if err := c.Start(); err != nil {
		return err
	}
	reap(c)
	return nil
}

// isAccessDenied 判断错误是否为 Windows “拒绝访问”(ERROR_ACCESS_DENIED, errno 5)，
// 即 CREATE_BREAKAWAY_FROM_JOB 在禁止脱离的作业中被拒绝。
func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == 5
	}
	return strings.Contains(err.Error(), "Access is denied")
}

func attr(cmd *exec.Cmd) *syscall.SysProcAttr {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	return cmd.SysProcAttr
}
