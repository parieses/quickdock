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
	"unsafe"

	"quickdock/internal/db"
	"quickdock/internal/platform"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// ===== Global shared state (accessed by main package via get/set) =====

var (
	// AppRef global App reference (used by SetClipboardText to call app.Clipboard.SetText)
	// 使用 atomic.Pointer 保证并发安全
	AppRef atomic.Pointer[application.App]

	// Clipboard text deduplication
	lastClipboardText   string
	lastClipboardTextMu sync.Mutex

	// ClipboardEmitter fires clipboard change events (set by tray.go)
	ClipboardEmitter func()
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
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")

	openClipboard := user32.NewProc("OpenClipboard")
	if ret, _, _ := openClipboard.Call(hwnd); ret == 0 {
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

	// 3. Image (CF_DIB)
	var imageData []byte
	imageHandle, _, _ := getClipboardData.Call(8) // CF_DIB
	if imageHandle != 0 {
		ptr, _, _ := globalLock.Call(imageHandle)
		if ptr != 0 {
			globalSize := kernel32.NewProc("GlobalSize")
			sz, _, _ := globalSize.Call(imageHandle)
			if sz > 0 && sz < 50*1024*1024 {
				imageData = make([]byte, int(sz))
				copy(imageData, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(sz)))
			} else {
				fmt.Printf("QuickDock: DIB size out of range: %d\n", sz)
			}
			globalUnlock.Call(imageHandle)
		} else {
			fmt.Println("QuickDock: GlobalLock(CF_DIB) failed")
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
			sourceApp := platform.GetActiveWindowTitle()
			setLastClipboardText(joined)
			go func() {
				if a.DB == nil {
					fmt.Println("QuickDock: clipboard: database closed, skipping image+file")
					return
				}
				processImage(a.DB, imageData, joined, sourceApp, a.emitClipboardEvent)
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
			if a.DB == nil {
				fmt.Println("QuickDock: clipboard: database closed, skipping image")
				return
			}
			processImage(a.DB, imageData, "", sourceApp, a.emitClipboardEvent)
		}()
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

// processImage 处理剪贴板图片数据：DIB→PNG→去重→写入磁盘→入库
// paths 参数：非空时表示图片附带文件路径，空字符串时表示纯图片
func processImage(database *db.Database, imageData []byte, paths, src string, emit func()) {
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
	pngBytes := pngBuf.Bytes()
	hashHex := platform.MD5Hash(pngBytes)

	imageID := uuid.New().String()
	imagePath := filepath.Join(platform.GetImageDir(), imageID+".png")

	entry, err := database.InsertClipboardImageEntry(imageID, imagePath, hashHex, paths, src)
	if err != nil {
		fmt.Printf("QuickDock: image clipboard save failed: %v\n", err)
		return
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
