//go:build !(windows && production)

package sysutil

import (
	"fmt"
	"os"
)

// ShowFatalError 在无原生弹窗的构建上退化为 stderr 输出。
//
// 覆盖三种情况：dev 构建（有控制台，看终端即可）、非 Windows 平台、
// 以及 unix 的正式版（macOS .app / Linux 桌面版同样没有终端，
// 但那边有各自的崩溃报告渠道，这里不额外引入 GUI 依赖）。
//
// 与 Windows 正式版保持同一语义：调用即意味着「流程无法继续」，
// 调用方随后退出。
func ShowFatalError(title, msg string) {
	fmt.Fprintf(os.Stderr, "QuickDock: %s: %s\n", title, msg)
}
