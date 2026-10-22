//go:build windows

package services

import (
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"quickdock/internal/logger"
	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/w32"
)

// w32 未导出的两个 API：全局内存大小查询 与 "PNG" 注册剪贴板格式号。
var (
	procGlobalSize               = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalSize")
	procRegisterClipboardFormatW = syscall.NewLazyDLL("user32.dll").NewProc("RegisterClipboardFormatW")
)

// globalSize 返回全局内存块实际大小（字节）。
func globalSize(h uintptr) uintptr {
	sz, _, _ := procGlobalSize.Call(h)
	return sz
}

// clipboardSnapshot 一次剪贴板变更的原始内容快照。
//
// 三个字段都是**已经拷进 Go 堆内存的副本**，因此拿到快照后即可立刻 CloseClipboard。
type clipboardSnapshot struct {
	filePaths  []string
	text       string
	imageData  []byte
	imageIsPNG bool
}

// captureClipboard 取一次剪贴板快照并入库。
//
// 运行在剪贴板工作协程上（见 startClipboardWorker），绝不在 Win32 消息线程上执行。
// 读临界区（Open→内存拷贝→Close）由 platform.WithClipboard 包裹并 LockOSThread：
// 剪贴板变更是一次性的状态变化，这次丢了就再也没有补抓机会（别的程序不会再发
// 一次 WM_CLIPBOARDUPDATE），故打开失败时按退避重试（参数见 platform.ClipboardOpenAttempts）。
func (a *AppService) captureClipboard() {
	if a.DB == nil {
		logClipboardSkip("入口")
		return
	}

	hwnd := platform.ClipboardWindowHandle()
	var snap clipboardSnapshot
	if !platform.WithClipboard(hwnd, func() {
		readClipboardSnapshot(&snap)
	}) {
		logger.W("QuickDock: OpenClipboard 连续 %d 次失败，本次剪贴板变更丢失", platform.ClipboardOpenAttempts)
		return
	}

	a.handleClipboardSnapshot(snap)
}

// readClipboardSnapshot 在剪贴板**已打开**的前提下读取内容快照。
// 调用方必须用 platform.WithClipboard 包裹（本函数不负责 Open/Close）——
// Open/Close 一旦被 Go 调度切到不同 OS 线程，剪贴板会被无限期锁死。
//
// 临界区纪律（这是本文件最重要的一条约束）：函数体内**只允许做内存拷贝**——
// 不解析 DIB、不做 PNG 编码、不查前台窗口标题、不碰数据库。
// 剪贴板是全局独占资源，同一时刻只能被一个进程打开；临界区里多停留的每一毫秒，
// 都会让别的程序的「复制」直接失败。而失败的复制不会产生 WM_CLIPBOARDUPDATE，
// 于是那段内容既进不了系统剪贴板，也进不了这里的历史记录——用户看到的现象就是
// 「复制得越多，后面复制的东西就越进不去剪贴板」。
func readClipboardSnapshot(snap *clipboardSnapshot) {
	// 1. CF_HDROP
	if h := w32.GetClipboardData(15); h != 0 {
		if raw := readGlobalMem(h, 1*1024*1024); len(raw) > 0 {
			snap.filePaths = platform.ParseHDROP(raw)
		} else {
			logger.W("QuickDock: HDROP 读取失败或大小越界")
		}
	}

	// 2. CF_UNICODETEXT
	if h := w32.GetClipboardData(13); h != 0 {
		if ptr := w32.GlobalLock(h); ptr != nil {
			// 基于 GlobalSize 计算实际 UTF-16 单元数，避免硬编码上限截断超长文本
			// （如 base64 编码的图片，长度远超旧上限 4096 字符）。
			// UTF16PtrToString 遇到 \0 即停，故传入真实大小是安全精确的。
			if sz := globalSize(uintptr(h)); sz > 0 {
				maxUnits := int(sz) / 2
				const maxSafeUnits = 1 << 22 // ~4MB UTF-16 安全上限
				if maxUnits > maxSafeUnits {
					maxUnits = maxSafeUnits
				}
				snap.text = platform.UTF16PtrToString(uintptr(unsafe.Pointer(ptr)), maxUnits)
			}
			w32.GlobalUnlock(h)
		} else {
			logger.W("QuickDock: GlobalLock(CF_UNICODETEXT) failed")
		}
	}

	// 3. Image — 探测顺序：PNG(注册格式) → CF_DIBV5(17) → CF_DIB(8)
	//    Win+Shift+S 等截图工具常以 DIBV5(带 alpha，biCompression=6) 或 PNG 存放；
	//    旧逻辑只认 CF_DIB(8)，且 DibToImage 曾拒绝 BI_ALPHABITFIELDS(6)，导致截图静默漏抓。
	if pngFmt := getPngClipboardFormat(); pngFmt != 0 {
		if h := w32.GetClipboardData(uint(pngFmt)); h != 0 {
			if b := readGlobalMem(h, 50*1024*1024); len(b) >= 8 &&
				b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
				snap.imageData = b
				snap.imageIsPNG = true
			}
		}
	}
	if snap.imageData == nil {
		for _, imgFmt := range []uint{17, 8} { // 17=CF_DIBV5, 8=CF_DIB
			if h := w32.GetClipboardData(imgFmt); h != 0 {
				if b := readGlobalMem(h, 50*1024*1024); len(b) > 0 {
					snap.imageData = b
					break
				}
			}
		}
	}
}

// handleClipboardSnapshot 对快照做分类入库。此时剪贴板已经关闭，可以放心做解析、
// 查前台窗口、写数据库。
//
// 分流顺序沿用历史行为：文件优先（资源管理器复制文件时同时带 CF_HDROP 与路径文本），
// 但「文件列表与文本不一致」时说明文本才是主内容，按文本处理。
func (a *AppService) handleClipboardSnapshot(snap clipboardSnapshot) {
	if len(snap.imageData) > 0 && clipboardLogContent {
		// 仅在显式开启内容日志时记录：每次剪贴板变化都可能带图（含截图工具、浏览器复制图片），
		// 默认不记，避免噪音；字节数与 PNG/DIB 形态对「抓错了格式」类问题仍有价值。
		logger.I("QuickDock: clipboard image detected (PNG=%v, %d bytes)", snap.imageIsPNG, len(snap.imageData))
	}

	if len(snap.filePaths) > 0 {
		joined := strings.Join(snap.filePaths, "\n")
		if joined == getLastClipboardText() {
			return
		}
		if snap.text == "" || platform.IsFilePathsAsText(snap.filePaths, snap.text) {
			a.saveClipboardFiles(snap, joined)
			return
		}
	}

	if a.saveClipboardText(snap.text) {
		return
	}

	a.saveClipboardImageOnly(snap)
}

// saveClipboardFiles 落库「复制的文件」（随文件一起复制的图片会落盘并挂在该条目上）。
func (a *AppService) saveClipboardFiles(snap clipboardSnapshot, joined string) {
	setLastClipboardText(joined)
	sourceApp := platform.GetActiveWindowTitle()

	if len(snap.imageData) > 0 {
		go func() {
			defer recoverPanic("clipboard processImage (file)")
			if a.DB == nil {
				logClipboardSkip("图片+文件")
				return
			}
			processImage(a.DB, snap.imageData, joined, sourceApp, a.emitClipboardEvent, snap.imageIsPNG)
		}()
		return
	}

	// 异步入库：DB 繁忙（如快照恢复）时避免阻塞剪贴板工作协程
	go func() {
		defer recoverPanic("clipboard saveFile")
		if a.DB == nil {
			logClipboardSkip("文件")
			return
		}
		entry, err := a.DB.InsertClipboardFileEntry(joined, sourceApp)
		if err != nil {
			logger.W("QuickDock: file clipboard save failed: %v", err)
		} else {
			logger.I("QuickDock >> clipboard captured [%s] (%d files) from [%s]", entry.ID[:8], len(snap.filePaths), sourceApp)
			a.emitClipboardEvent()
		}
	}()
}

// saveClipboardText 把文本入库，返回是否已处理（未处理时调用方继续尝试图片分支）。
func (a *AppService) saveClipboardText(text string) bool {
	// 上限对齐 GetClipboardText 的安全上限（1MB 文本），足以容纳绝大多数
	// base64 编码的图片/文件；再长的纯文本剪贴板内容实践意义极低，且避免无界入库。
	const maxClipboardTextLen = 1 << 20
	if text == "" || text == getLastClipboardText() || len(strings.TrimSpace(text)) == 0 || len(text) > maxClipboardTextLen {
		return false
	}

	setLastClipboardText(text)
	sourceApp := platform.GetActiveWindowTitle()

	// 异步入库：避免 DB 繁忙时阻塞剪贴板工作协程（同图片路径）
	go func() {
		defer recoverPanic("clipboard saveText")
		if a.DB == nil {
			logClipboardSkip("文本")
			return
		}
		entry, err := a.DB.InsertClipboardEntry(text, sourceApp)
		if err != nil {
			logger.W("QuickDock: clipboard save failed: %v", err)
		} else {
			if clipboardLogContent {
				preview := text
				runes := []rune(preview)
				if len(runes) > 80 {
					preview = string(runes[:80]) + "..."
				}
				logger.I("QuickDock >> clipboard captured [%s] from [%s] → %s", entry.ID[:8], sourceApp, preview)
			}
			// 默认不记录文本捕获（复制频繁、无排查价值）；排查时设 QUICKDOCK_LOG_CLIPBOARD=1 重启。
			a.emitClipboardEvent()
		}
	}()
	return true
}

// saveClipboardImageOnly 落库纯图片（无文件路径）。回环防护在 processImage 内做。
func (a *AppService) saveClipboardImageOnly(snap clipboardSnapshot) {
	if len(snap.imageData) == 0 {
		return
	}
	sourceApp := platform.GetActiveWindowTitle()
	go func() {
		defer recoverPanic("clipboard processImage (image-only)")
		if a.DB == nil {
			logClipboardSkip("图片")
			return
		}
		processImage(a.DB, snap.imageData, "", sourceApp, a.emitClipboardEvent, snap.imageIsPNG)
	}()
}

// readGlobalMem 锁定并复制全局内存句柄内容（超过 maxBytes 视为异常，返回 nil）。
func readGlobalMem(h w32.HANDLE, maxBytes int) []byte {
	if h == 0 {
		return nil
	}
	ptr := w32.GlobalLock(h)
	if ptr == nil {
		return nil
	}
	defer w32.GlobalUnlock(h)
	sz := globalSize(uintptr(h))
	if sz == 0 || sz > uintptr(maxBytes) {
		return nil
	}
	b := make([]byte, int(sz))
	copy(b, unsafe.Slice((*byte)(ptr), int(sz)))
	return b
}

// pngClipFmt 缓存 "PNG" 注册剪贴板格式号（部分截图工具直接以此存放图像）。
var pngClipFmt atomic.Uint32

func getPngClipboardFormat() uint32 {
	if v := pngClipFmt.Load(); v != 0 {
		return v
	}
	pngName, _ := syscall.UTF16PtrFromString("PNG")
	f, _, _ := procRegisterClipboardFormatW.Call(
		uintptr(unsafe.Pointer(pngName)))
	if f != 0 {
		pngClipFmt.Store(uint32(f))
	}
	return uint32(f)
}
