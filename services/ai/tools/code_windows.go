//go:build windows

package aitools

import (
	"os/exec"
	"strconv"

	"quickdock/internal/sysutil"
)

// setProcessGroup Windows 上没有 POSIX 进程组，杀树靠 taskkill /T，这里不需要额外属性。
func setProcessGroup(_ *exec.Cmd) {}

// killProcessGroup 用 taskkill /T 杀掉整棵进程树，失败回退到单进程 Kill。
//
// 只 Kill 直接子进程是不够的：`npm run dev` 自己退出后，它拉起的 node 仍然活着并占着端口，
// 用户下次启动就报「端口被占用」，而他完全不知道是谁占的。
//
// 但反过来只信 taskkill 也不行：权限不足、安全软件拦截、进程树里有别的用户的
// 子孙进程时它会失败（实测 Access denied）。失败后若什么都不做，调用方接下来
// 无限等 cmd.Wait() 就永远等不到——「停止」按钮点了没反应。
func killProcessGroup(cmd *exec.Cmd) {
	if err := sysutil.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
		return
	}
	// taskkill 整树失败：至少把直接子进程干掉，让 Wait 能返回。
	_ = cmd.Process.Kill()
}
