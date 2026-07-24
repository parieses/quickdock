package services

import (
	"bytes"
	"fmt"
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
	"quickdock/internal/platform"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
)

// ===== Global shared state (accessed by main package via get/set) =====

var (
	// AppRef global App reference (used by SetClipboardText to call app.Clipboard.SetText)
	// 使用 atomic.Pointer 保证并发安全
	AppRef atomic.Pointer[application.App]

	// Clipboard text deduplication
	lastClipboardText   string
	lastClipboardTextMu sync.Mutex
)

// SetClipboardText writes text to the system clipboard via Wails API
func SetClipboardText(text string) {
	if app := AppRef.Load(); app != nil && app.Clipboard.SetText(text) {
		setLastClipboardText(text)
		fmt.Println("QuickDock: clipboard written (length:", len(text), ")")
	} else {
		fmt.Println("QuickDock: clipboard write failed")
	}
}

// ===== OnClipboardChange — called by tray.go's windowProc =====

// OnClipboardChange handles clipboard change events
func (a *AppService) OnClipboardChange() {
	if a.DB == nil {
		fmt.Println("QuickDock: clipboard: database not initialized, skipping")
		return
	}

	hwnd := uintptr(a.HiddenHWND.Load())
	user32 := modUser32
	kernel32 := modKernel32

	if !openClipboardRetry(hwnd) {
		fmt.Println("QuickDock: OpenClipboard failed (another app may be holding it)")
		return
	}
	defer func() {
		closeClipboard := user32.NewProc("CloseClipboard")
		closeClipboard.Call()
	}()

	getClipboardData := user32.NewProc("GetClipboardData")
	globalLock := kernel32.NewProc("GlobalLock")
	globalUnlock := kernel32.NewProc("GlobalUnlock")

	// 1. CF_HDROP
	var filePaths []string
	hdropHandle, _, _ := getClipboardData.Call(15)
	if hdropHandle != 0 {
		ptr, _, _ := globalLock.Call(hdropHandle)
		if ptr != 0 {
			globalSize := kernel32.NewProc("GlobalSize")
			sz, _, _ := globalSize.Call(hdropHandle)
			if sz > 0 && sz < 1*1024*1024 {
				rawData := make([]byte, int(sz))
				copy(rawData, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(sz)))
				filePaths = platform.ParseHDROP(rawData)
			} else {
				fmt.Printf("QuickDock: HDROP size out of range: %d\n", sz)
			}
			globalUnlock.Call(hdropHandle)
		} else {
			fmt.Println("QuickDock: GlobalLock(HDROP) failed")
		}
	}

	// 2. Text
	var text string
	handle, _, _ := getClipboardData.Call(13) // CF_UNICODETEXT
	if handle != 0 {
		ptr, _, _ := globalLock.Call(handle)
		if ptr != 0 {
			text = platform.UTF16PtrToString(ptr, 4096)
			globalUnlock.Call(handle)
		} else {
			fmt.Println("QuickDock: GlobalLock(CF_UNICODETEXT) failed")
		}
	}

	// 3. Image — 探测顺序：PNG(注册格式) → CF_DIBV5(17) → CF_DIB(8)
	//    Win+Shift+S 等截图工具常以 DIBV5(带 alpha，biCompression=6) 或 PNG 存放；
	//    旧逻辑只认 CF_DIB(8)，且 DibToImage 曾拒绝 BI_ALPHABITFIELDS(6)，导致截图静默漏抓。
	var imageData []byte
	imageIsPNG := false
	if pngFmt := getPngClipboardFormat(); pngFmt != 0 {
		if h, _, _ := getClipboardData.Call(uintptr(pngFmt)); h != 0 {
			if b := readGlobalMem(h); len(b) >= 8 &&
				b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
				imageData = b
				imageIsPNG = true
			}
		}
	}
	if imageData == nil {
		for _, imgFmt := range []uintptr{17, 8} { // 17=CF_DIBV5, 8=CF_DIB
			if h, _, _ := getClipboardData.Call(imgFmt); h != 0 {
				if b := readGlobalMem(h); len(b) > 0 {
					imageData = b
					break
				}
			}
		}
	}
	if imageData == nil {
		fmt.Printf("QuickDock: no image format found; available clipboard formats=%v\n", listClipboardFormats())
	} else {
		fmt.Printf("QuickDock: clipboard image detected (PNG=%v, %d bytes) db=%s\n", imageIsPNG, len(imageData), a.DB.Path())
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
			sourceApp := platform.GetActiveWindowTitle()
			setLastClipboardText(joined)
			go func() {
				defer recoverPanic("clipboard processImage (file)")
				if a.DB == nil {
					fmt.Println("QuickDock: clipboard: database closed, skipping image+file")
					return
				}
				processImage(a.DB, imageData, joined, sourceApp, a.emitClipboardEvent, imageIsPNG)
			}()
			return
		}

		setLastClipboardText(joined)
		sourceApp := platform.GetActiveWindowTitle()
		entry, err := a.DB.InsertClipboardFileEntry(joined, sourceApp)
		if err != nil {
			fmt.Printf("QuickDock: file clipboard save failed: %v\n", err)
		} else {
			fmt.Printf("QuickDock >> clipboard captured [%s] (%d files) from [%s]\n", entry.ID[:8], len(filePaths), sourceApp)
			a.emitClipboardEvent()
		}
		return
	}

handleText:
	// 5. Text
	if text != "" && text != getLastClipboardText() && len(strings.TrimSpace(text)) > 0 && len(text) <= 65536 {
		setLastClipboardText(text)
		sourceApp := platform.GetActiveWindowTitle()

		entry, err := a.DB.InsertClipboardEntry(text, sourceApp)
		if err != nil {
			fmt.Printf("QuickDock: clipboard save failed: %v\n", err)
		} else {
			preview := text
			runes := []rune(preview)
			if len(runes) > 80 {
				preview = string(runes[:80]) + "..."
			}
			fmt.Printf("QuickDock >> clipboard captured [%s] from [%s] → %s\n", entry.ID[:8], sourceApp, preview)
			a.emitClipboardEvent()
		}
		return
	}

	// 6. Image-only
	if len(imageData) > 0 {
		sourceApp := platform.GetActiveWindowTitle()
		go func() {
			defer recoverPanic("clipboard processImage (image-only)")
			if a.DB == nil {
				fmt.Println("QuickDock: clipboard: database closed, skipping image")
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
	openClipboard := modUser32.NewProc("OpenClipboard")
	for i := 0; i < 5; i++ {
		if ret, _, _ := openClipboard.Call(hwnd); ret != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// readGlobalMem 锁定并复制全局内存句柄内容（带 50MB 安全上限）。
func readGlobalMem(h uintptr) []byte {
	if h == 0 {
		return nil
	}
	ptr, _, _ := modKernel32.NewProc("GlobalLock").Call(h)
	if ptr == 0 {
		return nil
	}
	defer modKernel32.NewProc("GlobalUnlock").Call(h)
	sz, _, _ := modKernel32.NewProc("GlobalSize").Call(h)
	if sz == 0 || sz > 50*1024*1024 {
		return nil
	}
	b := make([]byte, int(sz))
	copy(b, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(sz)))
	return b
}

// pngClipFmt 缓存 "PNG" 注册剪贴板格式号（部分截图工具直接以此存放图像）。
var pngClipFmt atomic.Uint32

func getPngClipboardFormat() uint32 {
	if v := pngClipFmt.Load(); v != 0 {
		return v
	}
	f, _, _ := modUser32.NewProc("RegisterClipboardFormatW").Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("PNG"))))
	if f != 0 {
		pngClipFmt.Store(uint32(f))
	}
	return uint32(f)
}

// listClipboardFormats 诊断用：枚举当前剪贴板所有可用格式号。
func listClipboardFormats() []int {
	enumFmt := modUser32.NewProc("EnumClipboardFormats")
	var out []int
	fmtCode := uintptr(0)
	for {
		next, _, _ := enumFmt.Call(fmtCode)
		if next == 0 {
			break
		}
		out = append(out, int(next))
		fmtCode = next
	}
	return out
}

// recoverPanic 恢复 goroutine panic 防止整个应用崩溃
func recoverPanic(context string) {
	if r := recover(); r != nil {
		fmt.Printf("QuickDock: [PANIC] %s: %v\n", context, r)
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
			fmt.Printf("QuickDock: DIB to image failed: %v\n", err)
			return
		}
		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, img); err != nil {
			fmt.Printf("QuickDock: PNG encode failed: %v\n", err)
			return
		}
		pngBytes = pngBuf.Bytes()
	}
	hashHex := platform.MD5Hash(pngBytes)

	imageID := uuid.New().String()
	imagePath := filepath.Join(platform.GetImageDir(), imageID+".png")

	entry, err := database.InsertClipboardImageEntry(imageID, imagePath, hashHex, paths, src)
	if err != nil {
		fmt.Printf("QuickDock: image clipboard save failed: %v\n", err)
		return
	}
	// 诊断：确认入库真实生效，并打印 DB 绝对路径（核对与外部查看工具是否同一文件）
	fmt.Printf("QuickDock >> image entry saved: id=%s db=%s\n", entry.ID, database.Path())
	if chk, e := database.GetClipboardEntry(entry.ID); e != nil {
		fmt.Printf("QuickDock: WARN self-check read-back failed: %v\n", e)
	} else {
		fmt.Printf("QuickDock: self-check ok: contentType=%s hasImagePath=%v\n", chk.ContentType, chk.ImagePath != "")
	}
	if entry.CopyCount == 1 {
		if err := os.WriteFile(imagePath, pngBytes, 0644); err != nil {
			fmt.Printf("QuickDock: save image file failed: %v, removing entry %s\n", err, entry.ID[:8])
			// 文件写入失败 → 回滚数据库条目，避免悬挂记录
			database.DeleteClipboardEntry(entry.ID)
			return
		}
	}
	if paths != "" {
		fmt.Printf("QuickDock >> clipboard captured [%s] (image file: %s) hash=%s count=%d\n", entry.ID[:8], paths, hashHex[:8], entry.CopyCount)
	} else {
		fmt.Printf("QuickDock >> clipboard captured [%s] (image) from [%s] hash=%s count=%d\n", entry.ID[:8], src, hashHex[:8], entry.CopyCount)
	}
	if emit != nil {
		emit()
	}
}
