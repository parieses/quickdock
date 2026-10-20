//go:build !windows

package services

// captureClipboard 非 Windows 下的占位实现（由 startClipboardWorker 调用）。
// mac/Linux 的剪贴板监控由 internal/platform 的监听桩负责（当前为空），
// 待 P2 接入 NSPasteboard changeCount 轮询 / X11 选择监视后再实现真实入库逻辑。
// 保留签名以保证共享代码（clipboard_sys.go 的工作协程）在跨平台编译下不报 undefined。
func (a *AppService) captureClipboard() {}
