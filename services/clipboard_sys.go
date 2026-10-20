package services

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// startClipboardWorker 启动剪贴板捕获协程（ServiceStartup 调用，随 ClipboardCtx 退出）。
//
// 变更信号（a.clipboardWake）承载的是「状态」而不是「事件」：无论期间来了多少次变更，
// 最终只需要按最新的剪贴板内容抓一次，所以 OnClipboardChange 用丢弃式非阻塞发送做合并。
//
// 为什么要单独一个协程：WM_CLIPBOARDUPDATE 回调跑在 Win32 消息线程上，那条线程同时
// 负责全局热键、托盘与浮窗消息。一旦在里面做 DIB 解码/PNG 编码/查库，热键和托盘
// 就跟着卡；更糟的是它会把剪贴板一直敞着（读取时 OpenClipboard 到函数返回才关闭），
// 别家程序的复制在这期间全部失败。
func (a *AppService) startClipboardWorker(ctx context.Context) {
	a.clipboardWake = make(chan struct{}, 1)
	go func() {
		defer logger.RecoverPanic("clipboard worker")
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.clipboardWake:
				a.captureClipboard()
			}
		}
	}()
}

// OnClipboardChange 由 Win32 消息线程调用（见 tray.go 的回调注入）。
// 这里只置一个信号就返回，绝不阻塞消息线程——真正的读取/解析/入库在剪贴板工作协程上。
func (a *AppService) OnClipboardChange() {
	if a.clipboardWake == nil {
		return
	}
	select {
	case a.clipboardWake <- struct{}{}:
	default: // 已有待处理信号，合并成一次（内容以最终状态为准）
	}
}

// SetClipboardText writes text to the system clipboard via Wails API
func SetClipboardText(text string) {
	app := AppRef.Load()
	if app == nil {
		logger.W("QuickDock: 剪贴板写入失败：应用实例未就绪")
		return
	}
	// 剪贴板是全系统独占资源，被别的程序短暂占用时 SetText 会直接返回 false。
	// 写失败且不重试，用户看到的就是「点了复制，粘出来还是旧内容」，故这里退避重试。
	const attempts = 4
	for i := 0; i < attempts; i++ {
		if app.Clipboard.SetText(text) {
			// 成功路径不记日志：从历史复制是高频操作，记日志只会把日志刷满。
			setLastClipboardText(text)
			return
		}
		if i < attempts-1 {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
		}
	}
	logger.W("QuickDock: 剪贴板写入失败：SetText 连续 %d 次返回 false（可能被其他程序占用），长度=%d", attempts, len(text))
}

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

// logClipboardSkip 剪贴板捕获因数据库未就绪/已关闭而跳过时的统一记录。
// 原实现散落 5 条文案各异但条件相同的 W 日志（"database not initialized" /
// "database closed"），合并后既减少重复，也保证每条都点明丢失的是哪类内容。
func logClipboardSkip(kind string) {
	logger.W("QuickDock: 剪贴板捕获[%s]跳过：数据库未就绪或已关闭", kind)
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

	// 回环防护：本次捕获的图片正是本程序刚写回剪贴板的那张（从历史复制图片），直接跳过，
	// 避免 CopyCount 失真。
	//
	// 比对放在这里而不是捕获入口：入口处为了比对要先解 DIB 再编一次 PNG，和下面这段
	// 编码完全重复（4K 截图约 200ms/次），而这里本来就要算哈希，等于零额外开销。
	if hashHex == getLastClipboardImageHash() {
		// 只挡一次：用户下次再主动复制同一张图，仍应正常记录。
		setLastClipboardImageHash("")
		return
	}

	imageID := uuid.New().String()
	imagePath := filepath.Join(platform.GetImageDir(), imageID+".png")

	entry, err := database.InsertClipboardImageEntry(imageID, imagePath, hashHex, paths, src)
	if err != nil {
		logger.W("QuickDock: image clipboard save failed: %v", err)
		return
	}
	// 原「image entry saved: id=」与下方 captured 日志信息重复，已删除；
	// 入库是否真实生效由 QUICKDOCK_LOG_CLIPBOARD=1 的 self-check 读回验证。
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
