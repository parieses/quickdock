//go:build windows

package services

import (
	"strings"
	"sync/atomic"
	"syscall"
	"time"
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
func (a *AppService) OnClipboardChange() {
	if a.DB == nil {
		logger.W("QuickDock: clipboard: database not initialized, skipping")
		return
	}

	hwnd := platform.ClipboardWindowHandle()

	if !openClipboardRetry(hwnd) {
		logger.W("QuickDock: OpenClipboard failed (another app may be holding it)")
		return
	}
	defer w32.CloseClipboard()

	// 1. CF_HDROP
	var filePaths []string
	hdropHandle := w32.GetClipboardData(15)
	if hdropHandle != 0 {
		ptr := w32.GlobalLock(hdropHandle)
		if ptr != nil {
			sz := globalSize(uintptr(hdropHandle))
			if sz > 0 && sz < 1*1024*1024 {
				rawData := make([]byte, int(sz))
				copy(rawData, unsafe.Slice((*byte)(ptr), int(sz)))
				filePaths = platform.ParseHDROP(rawData)
			} else {
				logger.W("QuickDock: HDROP size out of range: %d", sz)
			}
			w32.GlobalUnlock(hdropHandle)
		} else {
			logger.W("QuickDock: GlobalLock(HDROP) failed")
		}
	}

	// 2. Text
	var text string
	handle := w32.GetClipboardData(13) // CF_UNICODETEXT
	if handle != 0 {
		ptr := w32.GlobalLock(handle)
		if ptr != nil {
			// 基于 GlobalSize 计算实际 UTF-16 单元数，避免硬编码上限截断超长文本
			// （如 base64 编码的图片，长度远超旧上限 4096 字符）。
			// UTF16PtrToString 遇到 \0 即停，故传入真实大小是安全精确的。
			if sz := globalSize(uintptr(handle)); sz > 0 {
				maxUnits := int(sz) / 2
				const maxSafeUnits = 1 << 22 // ~4MB UTF-16 安全上限
				if maxUnits > maxSafeUnits {
					maxUnits = maxSafeUnits
				}
				text = platform.UTF16PtrToString(uintptr(unsafe.Pointer(ptr)), maxUnits)
			}
			w32.GlobalUnlock(handle)
		} else {
			logger.W("QuickDock: GlobalLock(CF_UNICODETEXT) failed")
		}
	}

	// 3. Image — 探测顺序：PNG(注册格式) → CF_DIBV5(17) → CF_DIB(8)
	//    Win+Shift+S 等截图工具常以 DIBV5(带 alpha，biCompression=6) 或 PNG 存放；
	//    旧逻辑只认 CF_DIB(8)，且 DibToImage 曾拒绝 BI_ALPHABITFIELDS(6)，导致截图静默漏抓。
	var imageData []byte
	imageIsPNG := false
	if pngFmt := getPngClipboardFormat(); pngFmt != 0 {
		if h := w32.GetClipboardData(uint(pngFmt)); h != 0 {
			if b := readGlobalMem(h); len(b) >= 8 &&
				b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
				imageData = b
				imageIsPNG = true
			}
		}
	}
	if imageData == nil {
		for _, imgFmt := range []uint{17, 8} { // 17=CF_DIBV5, 8=CF_DIB
			if h := w32.GetClipboardData(imgFmt); h != 0 {
				if b := readGlobalMem(h); len(b) > 0 {
					imageData = b
					break
				}
			}
		}
	}
	if imageData != nil {
		logger.I("QuickDock: clipboard image detected (PNG=%v, %d bytes) db=%s", imageIsPNG, len(imageData), a.DB.Path())
	}

	// 图片回环防护：若本次捕获的图片正是本程序刚写回剪贴板的那张（从历史复制图片），
	// 其去重哈希与上次写入一致，后续分支直接跳过，避免 CopyCount 失真与重复 DIB→PNG 编码。
	// 与 processImage 中的去重口径完全一致（PNG 原样取 MD5，DIB 解码后重编码 PNG 取 MD5）。
	imageLoop := false
	if len(imageData) > 0 {
		if h, ok := imageDataHash(imageData, imageIsPNG); ok && h != "" && h == getLastClipboardImageHash() {
			imageLoop = true
		}
	}

	// 4. Handle files/images
	if len(filePaths) > 0 {
		joined := strings.Join(filePaths, "\n")
		if joined == getLastClipboardText() {
			return
		}

		if text != "" && !platform.IsFilePathsAsText(filePaths, text) {
			goto handleText
		}

		if len(imageData) > 0 {
			if imageLoop {
				return
			}
			sourceApp := platform.GetActiveWindowTitle()
			setLastClipboardText(joined)
			go func() {
				defer recoverPanic("clipboard processImage (file)")
				if a.DB == nil {
					logger.W("QuickDock: clipboard: database closed, skipping image+file")
					return
				}
				processImage(a.DB, imageData, joined, sourceApp, a.emitClipboardEvent, imageIsPNG)
			}()
			return
		}

		setLastClipboardText(joined)
		sourceApp := platform.GetActiveWindowTitle()
		// 异步入库：DB 繁忙（如快照恢复）时避免阻塞 Win32 消息循环线程，影响全局热键/托盘
		go func() {
			defer recoverPanic("clipboard saveFile")
			if a.DB == nil {
				logger.W("QuickDock: clipboard: database closed, skipping file")
				return
			}
			entry, err := a.DB.InsertClipboardFileEntry(joined, sourceApp)
			if err != nil {
				logger.W("QuickDock: file clipboard save failed: %v", err)
			} else {
				logger.I("QuickDock >> clipboard captured [%s] (%d files) from [%s]", entry.ID[:8], len(filePaths), sourceApp)
				a.emitClipboardEvent()
			}
		}()
		return
	}

handleText:
	// 5. Text
	// 上限对齐 GetClipboardText 的安全上限（1MB 文本），足以容纳绝大多数
	// base64 编码的图片/文件；再长的纯文本剪贴板内容实践意义极低，且避免无界入库。
	const maxClipboardTextLen = 1 << 20
	if text != "" && text != getLastClipboardText() && len(strings.TrimSpace(text)) > 0 && len(text) <= maxClipboardTextLen {
		setLastClipboardText(text)
		sourceApp := platform.GetActiveWindowTitle()

		// 异步入库：避免 DB 繁忙时阻塞 Win32 消息循环线程（同图片路径）
		go func() {
			defer recoverPanic("clipboard saveText")
			if a.DB == nil {
				logger.W("QuickDock: clipboard: database closed, skipping text")
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
		return
	}

	// 6. Image-only
	if len(imageData) > 0 {
		if imageLoop {
			return
		}
		sourceApp := platform.GetActiveWindowTitle()
		go func() {
			defer recoverPanic("clipboard processImage (image-only)")
			if a.DB == nil {
				logger.W("QuickDock: clipboard: database closed, skipping image")
				return
			}
			processImage(a.DB, imageData, "", sourceApp, a.emitClipboardEvent, imageIsPNG)
		}()
	}
}
// openClipboardRetry 打开剪贴板，被其他进程短暂持有时重试若干次。
// 剪贴板监控里最容易被忽略的一类“静默丢数据”就是 OpenClipboard 偶发失败。
func openClipboardRetry(hwnd uintptr) bool {
	for i := 0; i < 5; i++ {
		if w32.OpenClipboard(w32.HWND(hwnd)) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// readGlobalMem 锁定并复制全局内存句柄内容（带 50MB 安全上限）。
func readGlobalMem(h w32.HANDLE) []byte {
	if h == 0 {
		return nil
	}
	ptr := w32.GlobalLock(h)
	if ptr == nil {
		return nil
	}
	defer w32.GlobalUnlock(h)
	sz := globalSize(uintptr(h))
	if sz == 0 || sz > 50*1024*1024 {
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
