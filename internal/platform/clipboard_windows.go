//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"image/png"
	"os"
	"runtime"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"

	"quickdock/internal/logger"
)

// procGlobalSize w32 未导出 GlobalSize，保留手写声明（仅此处使用）。
var procGlobalSize = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalSize")

// globalSize 返回全局内存块实际大小（字节）。
func globalSize(h uintptr) uintptr {
	sz, _, _ := procGlobalSize.Call(h)
	return sz
}

// SimulatePaste sends Ctrl+V keystroke via keybd_event
func SimulatePaste() {
	user32 := modUser32
	keybd := user32.NewProc("keybd_event")

	const (
		VK_CONTROL        = 0x11
		VK_V              = 0x56
		KEYEVENTF_KEYDOWN = 0x0000
		KEYEVENTF_KEYUP   = 0x0002
	)

	keybd.Call(VK_CONTROL, 0, KEYEVENTF_KEYDOWN, 0)
	keybd.Call(VK_V, 0, KEYEVENTF_KEYDOWN, 0)
	keybd.Call(VK_V, 0, KEYEVENTF_KEYUP, 0)
	time.Sleep(5 * time.Millisecond)
	keybd.Call(VK_CONTROL, 0, KEYEVENTF_KEYUP, 0)
}

// 剪贴板打开重试参数。总等待约 150ms：足够跨过别的程序几十毫秒级的占用，
// 又不至于让一次「复制」的入库延迟到用户可感知。导出供 services 层日志引用，
// 避免两处各写一份魔数。
const (
	ClipboardOpenAttempts  = 6
	ClipboardRetryStepWait = 10 * time.Millisecond
)

// openClipboardWithRetry 打开系统剪贴板，被其他进程短暂持有时重试。
//
// 剪贴板是全系统独占资源：同一时刻只允许一个进程 OpenClipboard。别的程序（含本程序
// 的读取路径）在自己的临界区里停留几十毫秒是常态，所以单次 OpenClipboard 失败
// 并不代表剪贴板坏了——写路径直接放弃，用户看到的就是「点了复制，粘出来的还是旧内容」。
// 退避总时长约 150ms，全部用在写路径（不阻塞 Win32 消息循环）。
//
// 注意：本函数只管「打开」，调用方必须经 WithClipboard 包裹整个临界区，
// 否则 Open 与 Close 可能被 Go 调度切到不同 OS 线程上（见 WithClipboard 注释）。
func openClipboardWithRetry(hwnd uintptr) bool {
	for i := 0; i < ClipboardOpenAttempts; i++ {
		if w32.OpenClipboard(w32.HWND(hwnd)) {
			return true
		}
		if i < ClipboardOpenAttempts-1 {
			time.Sleep(ClipboardRetryStepWait * time.Duration(i+1))
		}
	}
	return false
}

// WithClipboard 打开系统剪贴板 → 执行 fn → 关闭。返回 false 表示剪贴板始终
// 未能打开（fn 不会被执行）。
//
// 【线程绑定是本项目剪贴板代码的第一纪律，也是本函数存在的唯一理由】
// Win32 的 OpenClipboard 把剪贴板锁绑定到「调用线程」，之后必须由同一线程调用
// CloseClipboard 才能释放。Go 运行时会因异步抢占（GC、循环安全点）把 goroutine
// 切到别的 OS 线程——临界区里满是循环与内存拷贝，安全点非常密集。一旦 Open 与
// Close 落在不同线程，CloseClipboard 静默失败，剪贴板被原线程无限期占用：
// 此后所有程序的「复制」全部失败（它们重试几次后放弃，用户侧表现为复制毫无
// 反应），本程序的捕获也会因 OpenClipboard 失败而全部丢失，且只能杀进程恢复。
// wails 自带的 w32.SetClipboardText 内部正是用 LockOSThread 规避了同一问题，
// 本项目自有路径必须保持同一纪律——所有剪贴板临界区一律经本函数进出。
//
// fn 内只允许做内存拷贝：解析 DIB、查前台窗口标题、写数据库等一律放到 fn 外。
// 剪贴板是全局独占资源，临界区里多停留的每一毫秒，都会让别的程序的「复制」
// 直接失败；而失败的复制不会产生 WM_CLIPBOARDUPDATE，那段内容既进不了系统
// 剪贴板、也进不了历史记录——用户看到的就是「复制得越多，越复制不进去」。
func WithClipboard(hwnd uintptr, fn func()) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !openClipboardWithRetry(hwnd) {
		return false
	}
	defer func() {
		if !w32.CloseClipboard() {
			logger.E("[platform] CloseClipboard 失败：剪贴板可能被无限期占用，其他程序的复制将全部失败，需重启进程恢复")
		}
	}()

	fn()
	return true
}

// ClipboardOwnerProbeTimeoutMs 读取前探测 owner 是否响应的上限。
// 正常窗口在 1ms 内就会应一个 WM_NULL；200ms 足够区分「忙」与「挂死」。
const ClipboardOwnerProbeTimeoutMs uint32 = 200

// WithClipboardRead 是 WithClipboard 的「读取版」：打开剪贴板后先确认 owner
// 窗口还能响应，再执行 fn。返回 true 表示 fn 确实执行过。
//
// 【为什么所有读路径都必须走这里】
// GetClipboardData 对延迟渲染格式会同步 SendMessage(WM_RENDERFORMAT) 到 owner，
// 没有超时。owner 一旦卡死，调用线程就永久停在这一行——而它此时正持有剪贴板，
// 于是全系统所有程序的复制都开始返回 ERROR_ACCESS_DENIED，本程序自己的捕获也
// 再也打不开剪贴板。表现就是「用着用着就复制不了了，重启才恢复」。
//
// 本程序的读路径不止捕获协程一条：{clipboard} 变量（db/item.go）与插件
// host.clipboard.read 都走 GetClipboardText，它们同样会以 OpenClipboard(NULL)
// 打开——这种 NULL owner 的持有连 GetOpenClipboardWindow 都查不出来，事后只能
// 重启进程。所以保护必须放在这一层，而不是每个调用点各写一遍。
func WithClipboardRead(hwnd uintptr, fn func()) (executed bool) {
	if !WithClipboard(hwnd, func() {
		if !ClipboardOwnerResponsive(ClipboardOwnerProbeTimeoutMs) {
			// 不响应的 owner ⇒ 任何 GetClipboardData 都可能永久阻塞，直接放弃读取。
			logger.W("[platform] 剪贴板 owner 无响应，跳过本次读取（避免 GetClipboardData 永久阻塞并锁死剪贴板）")
			return
		}
		executed = true
		fn()
	}) {
		return false
	}
	return executed
}

// ClipboardProbe 探测剪贴板当前是否可打开：打开后立刻关闭，不读任何内容。
//
// 只做一次尝试（不像 openClipboardWithRetry 那样退避 150ms）——这是周期性
// 健康检查，不能自己长时间占着剪贴板。返回 false 说明有别的进程正锁着它：
// 此时系统里所有程序的「复制」都会失败，本程序的捕获也拿不到任何通知。
func ClipboardProbe() bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !w32.OpenClipboard(0) {
		return false
	}
	w32.CloseClipboard()
	return true
}

// 剪贴板「打开锁」持有者查询所需的 Win32 入口（w32 包未提供）。
var (
	procGetOpenClipboardWindow = modUser32.NewProc("GetOpenClipboardWindow")
	procGetClassNameW          = modUser32.NewProc("GetClassNameW")
)

// ClipboardHolderInfo 描述当前锁住剪贴板的进程，供探测失败的日志直接指名占用者。
//
// 【能查到什么】
// GetOpenClipboardWindow 返回持有剪贴板打开锁的窗口，再经 GetWindowThreadProcessId
// 得到 PID、processNameByPID 得到进程名——绝大多数程序（浏览器、Office、网盘客户端）
// 都是用自己的窗口打开剪贴板的，这条链路能直接给出进程名与窗口标题。
//
// 【查不到什么】
// 以 OpenClipboard(NULL) 打开的持有者不关联任何窗口，GetOpenClipboardWindow 返回 0，
// 本函数返回空串。Windows 没有「列出谁打开了剪贴板」的反向查询 API，剪贴板也不是内核
// 句柄对象（Process Explorer 之类的句柄搜索同样找不到），这种情况只能从现象侧排查。
//
// 必须在 OpenClipboard 失败之后调用：本进程一旦成功打开，查到的就是自己。
// 另注意与 GetClipboardOwner 区分——后者是「剪贴板内容的所有者」，正常状态也非 0，
// 拿它当占用者会一直误报（实测常见值是浏览器的消息窗口）。
func ClipboardHolderInfo() string {
	hwnd, _, _ := procGetOpenClipboardWindow.Call()
	if hwnd == 0 {
		return ""
	}
	var pid uint32
	modUser32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return ""
	}
	// 持有者是自己：临界区没能正常 Close（跨线程，或卡在 GetClipboardData 里）。
	// 这和「别的程序占着」是两回事——用户该做的是重启 QuickDock，不是去找别的程序。
	if int(pid) == os.Getpid() {
		return fmt.Sprintf("本程序自身（pid=%d，剪贴板临界区未正常结束，重启 QuickDock 即可恢复）", pid)
	}
	name, err := processNameByPID(pid)
	if err != nil || name == "" {
		name = "未知进程名"
	}
	detail := fmt.Sprintf("pid=%d %s", pid, name)
	if cls := windowClassName(hwnd); cls != "" {
		detail += "，窗口类 " + cls
	}
	if title := w32.GetWindowText(w32.HWND(hwnd)); title != "" {
		detail += fmt.Sprintf("，窗口标题「%s」", clipRunes(title, 60))
	}
	return detail
}

// windowClassName 取窗口类名（诊断用，失败返回空串）。
func windowClassName(hwnd uintptr) string {
	var buf [128]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return string(utf16.Decode(buf[:n]))
}

// clipRunes 按 rune 截断，避免超长窗口标题把日志撑爆。
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// allocGlobal 分配 GMEM_MOVEABLE|GMEM_ZEROINIT 全局内存并写入 data。
//
// 返回句柄的内存所有权归调用方：SetClipboardData 成功后所有权移交系统，
// 未移交（含整次写入失败）必须自行 GlobalFree，否则泄漏全局内存。
func allocGlobal(data []byte) (w32.HANDLE, error) {
	handle := w32.GlobalAlloc(0x0042, uint32(len(data)))
	if handle == 0 {
		return 0, fmt.Errorf("GlobalAlloc failed")
	}
	ptr := w32.GlobalLock(handle)
	if ptr == nil {
		// GlobalLock 失败：内存从未被写入，绝不能提交给系统剪贴板，
		// 否则会把未初始化（清零）数据当成真实内容，且需释放句柄避免泄漏。
		w32.GlobalFree(handle)
		return 0, fmt.Errorf("GlobalLock failed")
	}
	copy(unsafe.Slice((*byte)(ptr), len(data)), data)
	w32.GlobalUnlock(handle)
	return handle, nil
}

// clipboardFormat 一个「格式号 + 全局内存句柄」对，供 writeClipboardFormats 使用。
type clipboardFormat struct {
	format uint
	handle w32.HANDLE
}

// writeClipboardFormats 在同一次剪贴板会话里写入多个格式（EmptyClipboard 只做一次）。
//
// 多格式必须一次写完：分成两次会话时，后一次的 EmptyClipboard 会把前一次刚写的
// 格式冲掉。剪贴板历史里「复制的是图片文件」的条目就踩过这条——先写 CF_HDROP
// 再写 CF_DIB，CB 里只剩图片，粘贴到资源管理器毫无反应（用户侧就是「复制不了」）。
func writeClipboardFormats(hwnd uintptr, formats []clipboardFormat) error {
	if len(formats) == 0 {
		return nil
	}
	var setFailed bool
	// 临界区整体锁线程（见 WithClipboard）：Open/Close 跨线程 = 剪贴板永久锁死。
	opened := WithClipboard(hwnd, func() {
		w32.EmptyClipboard()
		for i := range formats {
			if w32.SetClipboardData(formats[i].format, formats[i].handle) == 0 {
				setFailed = true
				continue
			}
			formats[i].handle = 0 // 所有权已移交系统，不可再释放
		}
	})
	// 未被系统接手的句柄自行释放（含 OpenClipboard 始终失败、一个都没写进去的情况）。
	for _, f := range formats {
		if f.handle != 0 {
			w32.GlobalFree(f.handle)
		}
	}
	if !opened {
		return fmt.Errorf("OpenClipboard failed")
	}
	if setFailed {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

// hdropBytes 构造 CF_HDROP 数据：DROPFILES 结构头（20 字节）+ 双 NUL 结尾的宽字符路径表。
func hdropBytes(paths []string) []byte {
	var u16buf []uint16
	for _, p := range paths {
		u16buf = append(u16buf, utf16.Encode([]rune(p))...)
		u16buf = append(u16buf, 0)
	}
	u16buf = append(u16buf, 0)

	const drophdrSize = 20
	data := make([]byte, drophdrSize+len(u16buf)*2)
	binary.LittleEndian.PutUint32(data[0:4], drophdrSize)
	binary.LittleEndian.PutUint32(data[16:20], 1) // fWide = TRUE
	for i, ch := range u16buf {
		binary.LittleEndian.PutUint16(data[drophdrSize+i*2:], ch)
	}
	return data
}

// SetClipboardFiles writes a list of file paths to the system clipboard (CF_HDROP)
func SetClipboardFiles(hwnd uintptr, paths []string) error {
	if hwnd == 0 {
		return fmt.Errorf("window not initialized")
	}
	if len(paths) == 0 {
		return nil
	}
	// 先分配并写好全局内存，最后才动剪贴板：EmptyClipboard 之后任何一步失败，
	// 用户的剪贴板都会变成空的却又没有新内容——「复制了，但粘贴是空的」。
	handle, err := allocGlobal(hdropBytes(paths))
	if err != nil {
		return err
	}
	return writeClipboardFormats(hwnd, []clipboardFormat{{format: 15, handle: handle}})
}

// SetClipboardText 把纯文本写入系统剪贴板（CF_UNICODETEXT），返回是否成功。
//
// 不复用 wails 的 app.Clipboard.SetText：那条路径经 InvokeSyncWithResult 把写入
// 调度到主线程同步执行，而它内部的 waitOpenClipboard 最多自旋 1 秒——剪贴板被
// 别的程序占住时，主线程（热键、托盘、浮窗都在这条线程上）会跟着卡住；它的
// UTF16FromString 失败分支还会在已 EmptyClipboard 之后直接 return，把剪贴板留在
// 打开状态。自有路径与文件/图片写入共用 WithClipboard，是所有剪贴板临界区的唯一入口。
func SetClipboardText(hwnd uintptr, text string) error {
	// 先转换再动剪贴板：文本含内嵌 NUL 时转码会报错，此时必须什么都还没清空，
	// 否则用户的剪贴板会被清成空。
	data, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("文本无法编码为 UTF-16: %w", err)
	}
	raw := make([]byte, len(data)*2)
	for i, u := range data {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	handle, err := allocGlobal(raw)
	if err != nil {
		return err
	}
	return writeClipboardFormats(hwnd, []clipboardFormat{{format: 13, handle: handle}})
}

// SetClipboardImage writes a PNG image to the system clipboard (CF_DIB)
func SetClipboardImage(hwnd uintptr, imagePath string) error {
	if hwnd == 0 {
		return fmt.Errorf("window not initialized")
	}

	f, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("failed to open image file: %w", err)
	}
	defer f.Close()

	src, err := png.Decode(f)
	if err != nil {
		return fmt.Errorf("PNG decode failed: %w", err)
	}

	// DIB 构造与写剪贴板复用内存路径（clipboard_image_windows.go），
	// 避免同一套逻辑在两处各维护一份。
	return writeClipboardDIB(hwnd, encodeDIB(src))
}

// GetClipboardText reads the current system clipboard text (CF_UNICODETEXT).
// Used by snippet variable {clipboard} and plugin host.clipboard.read.
// Returns "" on any failure (e.g. another app holds the clipboard open) — never errors.
//
// 走 WithClipboardRead：这里用的是 OpenClipboard(NULL)，一旦卡在 GetClipboardData
// 上连 GetOpenClipboardWindow 都定位不到持有者，只能重启进程恢复。
func GetClipboardText() string {
	var text string
	WithClipboardRead(0, func() {
		handle := w32.GetClipboardData(13) // CF_UNICODETEXT
		if handle == 0 {
			return
		}
		ptr := w32.GlobalLock(handle)
		if ptr == nil {
			return
		}
		defer w32.GlobalUnlock(handle)

		size := globalSize(uintptr(handle))
		if size == 0 {
			return
		}
		n := int(size) / 2
		if n > 1<<20 {
			n = 1 << 20 // 安全上限 1MB
		}
		buf := unsafe.Slice((*uint16)(ptr), n)
		for i := 0; i < len(buf); i++ {
			if buf[i] == 0 {
				buf = buf[:i]
				break
			}
		}
		text = string(utf16.Decode(buf))
	})
	return text
}

// GetActiveWindowTitle returns the title of the foreground window
func GetActiveWindowTitle() string {
	hwnd := w32.GetForegroundWindow()
	if hwnd == 0 {
		return ""
	}
	return w32.GetWindowText(hwnd)
}

// 剪贴板 owner 响应探测所需的 Win32 入口（w32 包未提供）。
var (
	procGetClipboardOwner  = modUser32.NewProc("GetClipboardOwner")
	procSendMessageTimeout = modUser32.NewProc("SendMessageTimeoutW")
)

const (
	wmNull          = 0x0000
	smtoBlock       = 0x0001
	smtoAbortIfHung = 0x0002
	smtoErrorOnExit = 0x0020
)

// ClipboardOwnerResponsive 判断剪贴板 owner 窗口当前是否还能处理消息。
//
// 【为什么必须存在：GetClipboardData 会同步等一个卡死的进程】
// 剪贴板里的格式可以「延迟渲染」——程序只登记格式，真正数据等别人来取时才现做。
// 此时 GetClipboardData 会 SendMessage(WM_RENDERFORMAT) 到 owner 窗口，这是
// 同步且没有超时的等待。owner 进程一旦卡住（无响应窗口、调试器挂起、正在
// 退出的程序），调用方就永久卡在 GetClipboardData 里。
//
// 对我们来说这是致命的：调用发生在 WithClipboard 的临界区内，于是剪贴板一直被
// 本进程打开着——别的程序此后所有 Ctrl+C 都失败（它们不会因此产生
// WM_CLIPBOARDUPDATE），本程序的捕获协程也永久不再前进。用户看到的两件事
// 「复制没反应」和「历史里没有最新内容」其实是同一个死锁。
//
// 必须在调用 GetClipboardData 之前（剪贴板已打开状态下）用本函数探测 owner：
// 不响应的窗口一律跳过本次读取并立刻 CloseClipboard，宁可丢这一次，也不能锁死。
//
// 返回 true 表示可以安全读取（含无 owner 的情形）。
func ClipboardOwnerResponsive(timeoutMs uint32) bool {
	owner, _, _ := procGetClipboardOwner.Call()
	if owner == 0 {
		// 无 owner：EmptyClipboard 后尚未 SetClipboardData 之类，没有可阻塞的目标。
		return true
	}
	var result uintptr
	ret, _, _ := procSendMessageTimeout.Call(
		owner,
		wmNull, 0, 0,
		smtoBlock|smtoAbortIfHung|smtoErrorOnExit,
		uintptr(timeoutMs),
		uintptr(unsafe.Pointer(&result)),
	)
	return ret != 0
}

// UTF16PtrToString converts a UTF-16 pointer to a Go string
func UTF16PtrToString(ptr uintptr, maxLen int) string {
	if ptr == 0 {
		return ""
	}
	header := struct {
		Data uintptr
		Len  int
		Cap  int
	}{ptr, maxLen, maxLen}
	buf := *(*[]uint16)(unsafe.Pointer(&header))
	for i, ch := range buf {
		if ch == 0 {
			return string(utf16.Decode(buf[:i]))
		}
	}
	return string(utf16.Decode(buf))
}
