//go:build windows

package aitools

import (
	"os/exec"
	"strconv"

	"quickdock/internal/sysutil"
)

// setProcessGroup Windows 上没有 POSIX 进程组，杀树靠 taskkill /T，这里不需要额外属性。
func setProcessGroup(_ *exec.Cmd) {}

// killProcessGroup 用 taskkill /T 杀掉整棵进程树。
//
// 只 Kill 直接子进程是不够的：`npm run dev` 自己退出后，它拉起的 node 仍然活着并占着端口，
// 用户下次启动就报「端口被占用」，而他完全不知道是谁占的。
func killProcessGroup(cmd *exec.Cmd) {
	_ = sysutil.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
