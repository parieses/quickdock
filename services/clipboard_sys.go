package services

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
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

// ===== Global shared state (accessed by main package via get/set) =====

var (
	// AppRef global App reference (used by SetClipboardText to call app.Clipboard.SetText)
	// 使用 atomic.Pointer 保证并发安全
	AppRef atomic.Pointer[application.App]

	// Clipboard text deduplication
	lastClipboardText   string
	lastClipboardTextMu sync.Mutex

	// Clipboard image deduplication (防回环)：记录本程序刚写回剪贴板的图片哈希，
	// 使后续捕获能识别"从历史复制的图片"并跳过，避免 CopyCount 失真与重复 DIB→PNG 编码。
	lastClipboardImageHash   string
	lastClipboardImageHashMu sync.Mutex
)

// clipboardLogContent 控制是否把剪贴板「内容」（文本预览/文件路径/图片 self-check）
// 写入日志。默认关闭——剪贴板复制频繁且可能含敏感信息（密码/Token），写日志既膨胀
// 又泄露隐私。排查时设环境变量 QUICKDOCK_LOG_CLIPBOARD=1 再启动即可开启。
var clipboardLogContent = os.Getenv("QUICKDOCK_LOG_CLIPBOARD") == "1"

// SetClipboardText writes text to the system clipboard via Wails API
func SetClipboardText(text string) {
	if app := AppRef.Load(); app != nil && app.Clipboard.SetText(text) {
		setLastClipboardText(text)
		logger.I("QuickDock: clipboard written (length: %d)", len(text))
	} else {
		logger.W("QuickDock: clipboard write failed")
	}
}

// ===== OnClipboardChange — called by tray.go's windowProc =====

// OnClipboardChange handles clipboard change events
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

// ===== Clipboard helpers =====

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

// recoverPanic 恢复 goroutine panic 防止整个应用崩溃
func recoverPanic(context string) {
	if r := recover(); r != nil {
		logger.E("QuickDock: [PANIC] %s: %v", context, r)
	}
}

func (a *AppService) emitClipboardEvent() {
	if a.app != nil {
		a.app.Event.Emit("clipboard:updated")
	}
}

// ===== Internal helpers =====

func getLastClipboardText() string {
	lastClipboardTextMu.Lock()
	defer lastClipboardTextMu.Unlock()
	return lastClipboardText
}

func setLastClipboardText(s string) {
	lastClipboardTextMu.Lock()
	defer lastClipboardTextMu.Unlock()
	lastClipboardText = s
}

func getLastClipboardImageHash() string {
	lastClipboardImageHashMu.Lock()
	defer lastClipboardImageHashMu.Unlock()
	return lastClipboardImageHash
}

func setLastClipboardImageHash(h string) {
	lastClipboardImageHashMu.Lock()
	defer lastClipboardImageHashMu.Unlock()
	lastClipboardImageHash = h
}

// imageDataHash 计算剪贴板图片的去重哈希，口径与 processImage 完全一致：
// PNG 原样取 MD5，DIB 解码后重编码 PNG 取 MD5。用于在捕获入口同步判断
// "是否为本程序刚写回的图片"（防回环）。返回 (hash, ok)，ok=false 表示无法计算。
func imageDataHash(imageData []byte, isPNG bool) (string, bool) {
	if isPNG {
		return platform.MD5Hash(imageData), true
	}
	img, err := platform.DibToImage(imageData)
	if err != nil {
		return "", false
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", false
	}
	return platform.MD5Hash(buf.Bytes()), true
}

// ===== Internal processing functions (run in goroutines) =====

// processImage 处理剪贴板图片数据：DIB→PNG（或 PNG 原样）→去重→写入磁盘→入库
// paths 参数：非空时表示图片附带文件路径，空字符串时表示纯图片
// isPNG：剪贴板原始数据已是 PNG，直接落盘，免去 DIB 解码再编码的损失与开销
func processImage(database *db.Database, imageData []byte, paths, src string, emit func(), isPNG bool) {
	var pngBytes []byte
	if isPNG {
		pngBytes = imageData
	} else {
		img, err := platform.DibToImage(imageData)
		if err != nil {
			logger.W("QuickDock: DIB to image failed: %v", err)
			return
		}
		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, img); err != nil {
			logger.W("QuickDock: PNG encode failed: %v", err)
			return
		}
		pngBytes = pngBuf.Bytes()
	}
	hashHex := platform.MD5Hash(pngBytes)

	imageID := uuid.New().String()
	imagePath := filepath.Join(platform.GetImageDir(), imageID+".png")

	entry, err := database.InsertClipboardImageEntry(imageID, imagePath, hashHex, paths, src)
	if err != nil {
		logger.W("QuickDock: image clipboard save failed: %v", err)
		return
	}
	// 诊断：确认入库真实生效。self-check 读回仅排查时开启（QUICKDOCK_LOG_CLIPBOARD=1），
	// 默认只记一条极简入库确认，避免每次图片复制刷屏。
	logger.I("QuickDock >> image entry saved: id=%s", entry.ID)
	if clipboardLogContent {
		if chk, e := database.GetClipboardEntry(entry.ID); e != nil {
			logger.W("QuickDock: WARN self-check read-back failed: %v", e)
		} else {
			logger.I("QuickDock: self-check ok: contentType=%s hasImagePath=%v", chk.ContentType, chk.ImagePath != "")
		}
	}
	if entry.CopyCount == 1 {
		if err := os.WriteFile(imagePath, pngBytes, 0644); err != nil {
			logger.W("QuickDock: save image file failed: %v, removing entry %s", err, entry.ID[:8])
			// 文件写入失败 → 回滚数据库条目，避免悬挂记录
			database.DeleteClipboardEntry(entry.ID)
			return
		}
	}
	if clipboardLogContent {
		if paths != "" {
			logger.I("QuickDock >> clipboard captured [%s] (image file: %s) hash=%s count=%d", entry.ID[:8], paths, hashHex[:8], entry.CopyCount)
		} else {
			logger.I("QuickDock >> clipboard captured [%s] (image) from [%s] hash=%s count=%d", entry.ID[:8], src, hashHex[:8], entry.CopyCount)
		}
	} else {
		if paths != "" {
			logger.I("QuickDock >> clipboard captured image [%s] (%d files) from [%s]", entry.ID[:8], strings.Count(paths, "\n")+1, src)
		} else {
			logger.I("QuickDock >> clipboard captured image [%s] from [%s]", entry.ID[:8], src)
		}
	}
	if emit != nil {
		emit()
	}
}
