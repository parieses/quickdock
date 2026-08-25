//go:build windows

package dsh

import "syscall"

// hideWindowAttr 返回 CREATE_NO_WINDOW 控制台属性：
// 子进程获得一个隐藏控制台，其派生的 npm/cmd 子进程继承同一隐藏控制台，
// 不会各自弹出可见的 cmd 窗口。
// 注意：0x00000008 是 DETACHED_PROCESS，会让 node 脱离控制台、孙进程反而弹窗，
// 必须用 0x08000000（CREATE_NO_WINDOW）。
func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
