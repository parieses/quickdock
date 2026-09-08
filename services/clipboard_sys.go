package services

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"quickdock/internal/db"
	"quickdock/internal/logger"
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

// ===== Clipboard helpers =====


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

// SetLastClipboardImageHash 导出包装（供 services/clipboard 门面写回历史图片时做回环防护）
func SetLastClipboardImageHash(h string) {
	setLastClipboardImageHash(h)
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

