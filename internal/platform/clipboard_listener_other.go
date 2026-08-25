//go:build !windows

package platform

// 非 Windows 平台的剪贴板变更监听占位实现。
//
// 各平台接入方式：
//   - macOS：轮询 NSPasteboard changeCount（cgo），或 NSPasteboard 唤醒通知
//   - Linux X11：XFixes 选择事件；Wayland：ext-data-control / wlr-data-control 协议
//
// 接入前托盘/热键/主窗口等功能不受影响，仅剪贴板历史不记录。

func StartClipboardListener(onChange func()) {}

func StopClipboardListener() {}

func ClipboardWindowHandle() uintptr { return 0 }
