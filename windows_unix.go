//go:build !windows

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// foregroundIsOwnedModal 非 Windows 下无 HWND 概念，恒返回 false（不拦截失焦隐藏）。
// mac/Linux 的失焦判断后续可接入 NSWorkspace/XGnome 前台应用检测，但 P0 阶段无需。
func foregroundIsOwnedModal() bool { return false }

// applyWindowPlatformOptions 非 Windows 下暂无需设置的窗口属性。
// mac 的浮窗默认不在 Dock/任务栏显示，行为与原 Windows 意图一致。
func applyWindowPlatformOptions(opts *application.WebviewWindowOptions) {}
