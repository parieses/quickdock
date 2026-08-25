//go:build !windows

package dsh

import "syscall"

// hideWindowAttr 非 Windows 平台无控制台隐藏需求。
func hideWindowAttr() *syscall.SysProcAttr { return nil }
