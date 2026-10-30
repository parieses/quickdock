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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

const wmClipboardUpdate = 0x031D

var (
	listenerUser32       = syscall.NewLazyDLL("user32.dll")
	listenerKernel32     = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassW   = listenerUser32.NewProc("RegisterClassW")
	procCreateWindowExW  = listenerUser32.NewProc("CreateWindowExW")
	procDefWindowProcW   = listenerUser32.NewProc("DefWindowProcW")
	procDestroyWindowW   = listenerUser32.NewProc("DestroyWindow")
	procGetMessageW      = listenerUser32.NewProc("GetMessageW")
	procTranslateMessage = listenerUser32.NewProc("TranslateMessage")
	procDispatchMessageW = listenerUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = listenerUser32.NewProc("PostQuitMessage")
	procGetModuleHandleW = listenerKernel32.NewProc("GetModuleHandleW")
	procClipSeqNum       = listenerUser32.NewProc("GetClipboardSequenceNumber")
	procIsWindow         = listenerUser32.NewProc("IsWindow")
)

// ClipboardSequenceNumber 返回系统剪贴板序号：每次有程序写入/清空剪贴板都会递增。
//
// 它是「剪贴板内容到底变没变」的唯一可靠判据，且不需要 OpenClipboard——
// 因此可以在监听器失效（WM_CLIPBOARDUPDATE 收不到）时用来兜底补抓。
// 返回 0 表示不可用（非 Windows 平台，或系统拒绝）。
func ClipboardSequenceNumber() uint32 {
	ret, _, _ := procClipSeqNum.Call()
	return uint32(ret)
}

// ClipboardListenerAlive 报告监听消息线程是否仍在运行。
//
// 用于诊断：序号一直在变却始终没有回调，说明消息线程/监听注册已经失效。
func ClipboardListenerAlive() bool {
	if !listenerRunning.Load() {
		return false
	}
	hwnd := listenerHwnd.Load()
	if hwnd == 0 {
		return false
	}
	// 窗口句柄会被系统复用（销毁后可能被别的窗口占用），单看非零不足以证明
	// 我们的消息泵还活着；IsWindow 至少能排除「句柄已失效」这一类。
	ret, _, _ := procIsWindow.Call(hwnd)
	return ret != 0
}

// listenerHwnd 监听窗口句柄（移除监听 / 剪贴板读取使用）
var listenerHwnd atomic.Uintptr

var (
	// listenerReady 在监听窗口创建完成（hwnd 已就绪）后关闭；创建失败也会关闭。
	//
	// Stop 必须等它。早期版本直接读 listenerHwnd，若 Stop 早于 Store(hwnd)
	// 被调用（Start 里还有 RegisterClass + CreateWindow 两步阻塞），会读到 0
	// 而静默放弃——随后窗口照样被建出来并进入消息循环，那条 LockOSThread
	// 的线程从此无人能关，永久泄漏。
	listenerReady   chan struct{}
	listenerReadyMu sync.Mutex

	// listenerRunning 保证 Start 幂等：重复 Start 会叠加出第二个消息线程和
	// 第二个监听窗口，而 listenerHwnd 只存最后一个，前者再也关不掉。
	listenerRunning atomic.Bool
)

// listenerReadyTimeout 等待监听窗口就绪的上限。
// 建窗口是同步 Win32 调用，正常在毫秒级；超时说明线程卡住或压根没起来，
// 此时也没有窗口可销毁，继续等只会拖慢退出。
const listenerReadyTimeout = 3 * time.Second

// StartClipboardListener 在独立 OS 线程上启动消息泵（GetMessage 阻塞，必须独占线程）。
// onChange 在 Win32 消息线程上被调用，重活应自行转 goroutine。
func StartClipboardListener(onChange func()) {
	if listenerRunning.Swap(true) {
		logger.W("[platform] 剪贴板监听已在运行，忽略重复启动")
		return
	}

	listenerReadyMu.Lock()
	listenerReady = make(chan struct{})
	ready := listenerReady
	listenerReadyMu.Unlock()

	go runClipboardListener(onChange, ready)
}

// StopClipboardListener 注销剪贴板格式监听并退出消息泵。
//
// 仅 RemoveClipboardFormatListener 不够：消息泵的 GetMessage 循环运行在专属 OS 线程
// （LockOSThread），若不发 WM_DESTROY 使其 PostQuitMessage(0) 退出循环，该线程会永久泄漏。
// DestroyWindow 跨线程安全——它在窗口所属线程派发 WM_DESTROY，由 wndProc 触发退出。
func StopClipboardListener() {
	if !listenerRunning.Load() {
		return
	}

	// 先等窗口就绪，避免「窗口还没建好 → 读到 hwnd==0 → 放弃」导致的线程泄漏。
	listenerReadyMu.Lock()
	ready := listenerReady
	listenerReadyMu.Unlock()
	if ready != nil {
		select {
		case <-ready:
		case <-time.After(listenerReadyTimeout):
			logger.W("[platform] 等待剪贴板监听窗口就绪超时（%v），放弃停止", listenerReadyTimeout)
		}
	}

	hwnd := listenerHwnd.Load()
	if hwnd == 0 {
		// 窗口创建失败：线程自己已退出，这里只需复位运行标记。
		listenerRunning.Store(false)
		return
	}
	w32.RemoveClipboardFormatListener(w32.HWND(hwnd))
	procDestroyWindowW.Call(hwnd)
	listenerHwnd.Store(0)
	listenerRunning.Store(false)
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

func runClipboardListener(onChange func(), ready chan struct{}) {
	goruntime.LockOSThread()

	// 消息泵真正退出时才复位运行标记，使 Stop→Start 的重启得以成立。
	defer listenerRunning.Store(false)
	// ready 必须在任何退出路径上关闭，否则 Stop 会白等一个超时才继续。
	closeReady := sync.OnceFunc(func() { close(ready) })
	defer closeReady()

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
		return // ready 由 defer 关闭，Stop 不会白等
	}
	listenerHwnd.Store(hwnd)
	// 就绪信号必须在 Store 之后立刻发出，Stop 靠它判断能否安全销毁。
	closeReady()

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
