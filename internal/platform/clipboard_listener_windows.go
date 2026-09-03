//go:build windows

package platform

// 剪贴板变更监听（Windows 实现）。
//
// 唯一职责：创建一个隐藏的 Win32 消息窗口，注册 AddClipboardFormatListener，
// 收到 WM_CLIPBOARDUPDATE 时回调 onChange()。
//
// 该窗口此前位于 main 包且同时承载托盘与全局热键；两者已迁移到 Wails v3 框架后，
// 监听器下沉到 platform 包，以回调注入方式与服务层解耦。
//
// ClipboardWindowHandle 暴露隐藏窗口句柄：剪贴板读取 API（OpenClipboard）
// 需要一个窗口句柄。

import (
	goruntime "runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

const wmClipboardUpdate = 0x031D

var (
	listenerUser32      = syscall.NewLazyDLL("user32.dll")
	listenerKernel32    = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassW   = listenerUser32.NewProc("RegisterClassW")
	procCreateWindowExW  = listenerUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = listenerUser32.NewProc("DefWindowProcW")
	procDestroyWindowW   = listenerUser32.NewProc("DestroyWindow")
	procGetMessageW      = listenerUser32.NewProc("GetMessageW")
	procTranslateMessage = listenerUser32.NewProc("TranslateMessage")
	procDispatchMessageW = listenerUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = listenerUser32.NewProc("PostQuitMessage")
	procGetModuleHandleW = listenerKernel32.NewProc("GetModuleHandleW")
)

// listenerHwnd 监听窗口句柄（移除监听 / 剪贴板读取使用）
var listenerHwnd atomic.Uintptr

// StartClipboardListener 在独立 OS 线程上启动消息泵（GetMessage 阻塞，必须独占线程）。
// onChange 在 Win32 消息线程上被调用，重活应自行转 goroutine。
func StartClipboardListener(onChange func()) {
	go runClipboardListener(onChange)
}

// StopClipboardListener 注销剪贴板格式监听并退出消息泵。
//
// 仅 RemoveClipboardFormatListener 不够：消息泵的 GetMessage 循环运行在专属 OS 线程
// （LockOSThread），若不发 WM_DESTROY 使其 PostQuitMessage(0) 退出循环，该线程会永久泄漏。
// DestroyWindow 跨线程安全——它在窗口所属线程派发 WM_DESTROY，由 wndProc 触发退出。
func StopClipboardListener() {
	hwnd := listenerHwnd.Load()
	if hwnd == 0 {
		return
	}
	w32.RemoveClipboardFormatListener(w32.HWND(hwnd))
	procDestroyWindowW.Call(hwnd)
	listenerHwnd.Store(0)
	logger.I("[platform] 剪贴板监听已停止")
}

// ClipboardWindowHandle 返回监听窗口句柄（供 OpenClipboard 等读取 API 使用），未启动时为 0。
func ClipboardWindowHandle() uintptr {
	return listenerHwnd.Load()
}

// clipboardWndProc 仅处理剪贴板变更与销毁消息，其余交 DefWindowProc。
func clipboardWndProc(onChange func()) func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	return func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmClipboardUpdate:
			// 使用 AddClipboardFormatListener（而非传统剪贴板链），
			// 避免链中任一环节断裂导致收不到任何剪贴板变更通知。
			if onChange != nil {
				onChange()
			}
			return 0
		case 0x0002: // WM_DESTROY
			procPostQuitMessage.Call(0)
			return 0
		}
		ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return ret
	}
}

func runClipboardListener(onChange func()) {
	goruntime.LockOSThread()

	// syscall.UTF16PtrFromString：StringToUTF16Ptr 已弃用（SA1019）；常量串无内嵌 NUL，error 必为 nil
	className, _ := syscall.UTF16PtrFromString("QuickDock_Clipboard_Window_v4")

	hinstance, _, _ := procGetModuleHandleW.Call(0)

	wc := struct {
		style         uint32
		lpfnWndProc   uintptr
		cbClsExtra    int32
		cbWndExtra    int32
		hinstance     uintptr
		hIcon         uintptr
		hCursor       uintptr
		hbrBackground uintptr
		lpszMenuName  *uint16
		lpszClassName *uint16
	}{
		lpfnWndProc:   syscall.NewCallback(clipboardWndProc(onChange)),
		hinstance:     hinstance,
		lpszClassName: className,
	}

	procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc)))

	const wsExToolwindow = 0x00000080
	const wsPopup = 0x80000000
	wndTitle, _ := syscall.UTF16PtrFromString("QuickDockClipListener")
	hwnd, _, _ := procCreateWindowExW.Call(
		wsExToolwindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(wndTitle)),
		wsPopup,
		0, 0, 0, 0,
		0, 0, hinstance, 0,
	)

	if hwnd == 0 {
		logger.E("[platform] 创建剪贴板监听窗口失败")
		return
	}
	listenerHwnd.Store(hwnd)

	if w32.AddClipboardFormatListener(w32.HWND(hwnd)) {
		logger.I("[platform] 剪贴板监听已启动 (AddClipboardFormatListener)")
	} else {
		logger.E("[platform] AddClipboardFormatListener 失败")
	}

	var msg struct {
		hwnd    uintptr
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      struct{ x, y int32 }
	}

	for {
		ret, _, _ := procGetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 {
			break
		}

		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	logger.I("[platform] 剪贴板监听消息循环已停止")
}
