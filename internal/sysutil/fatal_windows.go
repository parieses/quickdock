//go:build windows && production

package sysutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ShowFatalError 用原生消息框呈现无法继续的错误（正式版 / Windows）。
//
// 正式版是 `-H windowsgui` 构建，没有控制台：进程静默退出时用户看到的是
// 「双击了但什么都没发生」——这是桌面应用最糟的失败形态，既没有错误也没有
// 任何可追查的线索。日志虽然落了盘，但用户得先知道去哪儿看。
//
// 只在 production 构建里弹：dev 版有真实控制台，stderr 已经能看到（见
// internal/logger/tee_dev.go），而 dev 会因热重载反复重启，弹窗会不停地挡在
// 屏幕上。
//
// 调用点必须是「已经没救了」的路径——它会阻塞到用户点确定，之后进程退出。
func ShowFatalError(title, msg string) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := windows.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	const (
		mbOK        = 0x00000000
		mbIconError = 0x00000010
		mbTopmost   = 0x00040000 // 压在其他窗口之上，避免被遮住误以为没反应
	)
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	// 返回值为 0 表示失败（如桌面会话不可用），此时只能放弃——
	// 日志里已有完整错误，不再递归报错。
	_, _, _ = proc.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbOK|mbIconError|mbTopmost)
}
