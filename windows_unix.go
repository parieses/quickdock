//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// foregroundIsOwnedModal 非 Windows 下无 HWND 概念，恒返回 false（不拦截失焦隐藏）。
// mac/Linux 的失焦判断后续可接入 NSWorkspace/XGnome 前台应用检测，但 P0 阶段无需。
func foregroundIsOwnedModal() bool { return false }

// applyWindowPlatformOptions 非 Windows 下暂无需设置的窗口属性。
// mac 的浮窗默认不在 Dock/任务栏显示，行为与原 Windows 意图一致。
// 仍然走一次 applyWindowBackdrop：非 Windows 恒判定 off，是 no-op，
// 但保证平台分支的语义一致，日后 mac 支持材质时不用再改调用点。
func applyWindowPlatformOptions(opts *application.WebviewWindowOptions) {
	applyWindowBackdrop(opts, true)
}
