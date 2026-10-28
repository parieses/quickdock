//go:build darwin || linux

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

// ClipboardSequenceNumber 非 Windows 平台无对应概念，固定返回 0
// （调用方约定 0 表示「不支持兜底轮询」，不会据此触发补抓）。
func ClipboardSequenceNumber() uint32 { return 0 }

// ClipboardListenerAlive 非 Windows 平台没有消息窗口监听。
func ClipboardListenerAlive() bool { return false }

// ClipboardOwnerResponsive 非 Windows 平台没有「延迟渲染同步等待」问题。
func ClipboardOwnerResponsive(timeoutMs uint32) bool { return true }

// ClipboardProbe 非 Windows 平台没有可探测的独占剪贴板，恒为可用。
func ClipboardProbe() bool { return true }

// ClipboardHolderInfo 非 Windows 平台没有「打开锁」，也就不存在可指名的持有者。
func ClipboardHolderInfo() string { return "" }
