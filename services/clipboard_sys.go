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

	"quickdock/internal/cliprule"
)

// ===== Global shared state (accessed by main package via get/set) =====

var (
	// AppRef global App reference (used by SetClipboardText to call app.Clipboard.SetText)
	// 使用 atomic.Pointer 保证并发安全
	AppRef atomic.Pointer[application.App]

	// lastClipboardText 最近一次**入库**的剪贴板文本（本程序回写的内容不算）。
	// 命令面板的 {clipboard} 变量读它，因此这里只记录真实复制来的内容。
	lastClipboardText   string
	lastClipboardTextMu sync.Mutex

	// Clipboard image deduplication (防回环)：记录本程序刚写回剪贴板的图片哈希，
	// 使后续捕获能识别"从历史复制的图片"并跳过，避免 CopyCount 失真与重复 DIB→PNG 编码。
	lastClipboardImageHash   string
	lastClipboardImageHashMu sync.Mutex

	// selfWriteText 本程序刚写进剪贴板的文本——**一次性**回环令牌（配合 selfWriteTextAt 过期）。
	//
	// 与「最近入库文本」必须分开：早前用同一个变量同时承担回环防护和去重，副作用是
	// 用户连续两次复制同一段内容时，第二次会被永久吞掉（连置顶都不做）。
	// 令牌只挡一次、且有时效，之后同样内容再复制仍会正常入库并置顶。
	selfWriteText   string
	selfWriteTextAt int64
	selfWriteTextMu sync.Mutex

	// lastInsertedText / At 抑制「同一次剪贴板变化产生两条 WM_CLIPBOARDUPDATE」时的重复入库。
	// 只在一个很短的窗口内生效，不是持久去重——超过窗口的相同内容仍算一次新的复制。
	lastInsertedText   string
	lastInsertedAt     int64
	lastInsertedTextMu sync.Mutex
)

// clipboardLogContent 控制是否把剪贴板「内容」（文本预览/文件路径/图片 self-check）
// 写入日志。默认关闭——剪贴板复制频繁且可能含敏感信息（密码/Token），写日志既膨胀
// 又泄露隐私。排查时设环境变量 QUICKDOCK_LOG_CLIPBOARD=1 再启动即可开启。
var clipboardLogContent = os.Getenv("QUICKDOCK_LOG_CLIPBOARD") == "1"

// 剪贴板链路的看护参数。
const (
	// clipboardFallbackInterval 序号兜底轮询间隔。
	//
	// WM_CLIPBOARDUPDATE 一旦丢了就再也不会有第二次（内容变化是不可重放的状态），
	// 而下面几种失效都不报错、也不会自愈：监听窗口被系统从列表里摘掉、消息线程
	// 意外退出、某次捕获卡在 GetClipboardData 上。GetClipboardSequenceNumber
	// 不需要 OpenClipboard 就能读到，用它做兜底是唯一「无论监听是否活着都能补抓」的办法。
	clipboardFallbackInterval = 2 * time.Second

	// clipboardCaptureStuckAfter 单次捕获超过该时长即视为卡住并告警。
	// 正常读取是纯内存拷贝（最坏几十 MB 图片），远低于此值。
	clipboardCaptureStuckAfter = 5 * time.Second

	// clipboardStuckWarnInterval / clipboardMissWarnInterval 同类告警的最小间隔，避免刷日志。
	clipboardStuckWarnInterval = 30 * time.Second
	clipboardMissWarnInterval  = 60 * time.Second

	// selfWriteTTL 回环令牌的有效期：超过它说明本程序写入时的那条通知没抓到，
	// 令牌自行失效，之后相同内容再复制仍会正常入库。
	selfWriteTTL = 3 * time.Second

	// duplicateWindow 同一内容在这么短的时间内只入库一次
	// （一次复制可能触发两条通知：程序先 Set 再 Empty+Set）。
	duplicateWindow = 1500 * time.Millisecond

	// clipboardProbeInterval / clipboardProbeWarnAfter 剪贴板可用性探测的周期与
	// 升级为 E 级日志的连续失败次数（见 startClipboardWorker 的第二条看护协程）。
	clipboardProbeInterval  = 60 * time.Second
	clipboardProbeWarnAfter = 2
)

var (
	clipboardStuckWarnAt atomic.Int64
	clipboardMissWarnAt  atomic.Int64
	clipboardMissStreak  atomic.Int64
)

// clipboardHolderDesc 返回「当前锁住剪贴板的进程」的可读描述，查不到时说清原因。
//
// 只在 OpenClipboard 已经失败之后调用：持有锁时查询，报的是本进程自己。
func clipboardHolderDesc() string {
	if h := platform.ClipboardHolderInfo(); h != "" {
		return h
	}
	return "无法定位（持有者未关联窗口，或以 NULL 句柄打开）"
}

// startClipboardWorker 启动剪贴板捕获协程（ServiceStartup 调用，随 ClipboardCtx 退出）。
//
// 变更信号（a.clipboardWake）承载的是「状态」而不是「事件」：无论期间来了多少次变更，
// 最终只需要按最新的剪贴板内容抓一次，所以 OnClipboardChange 用丢弃式非阻塞发送做合并。
//
// 为什么要单独一个协程：WM_CLIPBOARDUPDATE 回调跑在 Win32 消息线程上，那条线程同时
// 负责全局热键、托盘与浮窗消息。一旦在里面做 DIB 解码/PNG 编码/查库，热键和托盘
// 就跟着卡；更糟的是它会把剪贴板一直敞着（读取时 OpenClipboard 到函数返回才关闭），
// 别家程序的复制在这期间全部失败。
//
// 这里同时起两条循环：事件驱动 + 序号兜底（见 clipboardFallbackInterval 的说明）。
func (a *AppService) startClipboardWorker(ctx context.Context) {
	wake := make(chan struct{}, 1)
	a.clipboardWake = wake

	// 把「启动那一刻的序号」记为已处理：否则兜底轮询会把启动前就存在的旧内容
	// 当成一次漏抓补进来。必须在起协程之前同步设置，避免和第一次事件捕获抢。
	a.clipboardSeq.Store(platform.ClipboardSequenceNumber())

	// 事件驱动：正常路径，延迟最低。
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				a.runClipboardCapture("事件")
			}
		}
	}()

	// 兜底轮询：监听失效时把内容补回来，并把「失效」这件事记到日志里。
	go func() {
		ticker := time.NewTicker(clipboardFallbackInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.pollClipboardSequence()
			}
		}
	}()

	// 可用性探测：剪贴板被别的进程锁死时，本程序既收不到通知、也没有任何入口
	// 能感知（用户复制失败发生在别的程序里）。定期探一次，把「复制不了」这件
	// 事写进日志，避免又变成一次无据可查的「用着用着就不行了」。
	go func() {
		ticker := time.NewTicker(clipboardProbeInterval)
		defer ticker.Stop()
		fails := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if platform.ClipboardProbe() {
					if fails >= clipboardProbeWarnAfter {
						logger.I("QuickDock: 剪贴板已恢复可用")
					}
					fails = 0
					continue
				}
				fails++
				if fails == clipboardProbeWarnAfter || fails%10 == 0 {
					// 点名占用者。必须在探测**失败之后**才查——本进程一旦成功打开
					// 剪贴板，GetOpenClipboardWindow 报的就是本进程自己。
					logger.E("QuickDock: 剪贴板探测失败 %d 次（每次间隔 %v），占用者：%s。"+
						"此时系统内所有程序都无法复制，本程序的剪贴板历史也不会更新——重启占用者或注销即可恢复",
						fails, clipboardProbeInterval, clipboardHolderDesc())
				}
			}
		}
	}()
}

// pollClipboardSequence 比对系统剪贴板序号，补抓「序号变了但没收到通知」的内容。
func (a *AppService) pollClipboardSequence() {
	seq := platform.ClipboardSequenceNumber()
	if seq == 0 || seq == a.clipboardSeq.Load() {
		return
	}
	n := clipboardMissStreak.Add(1)
	now := time.Now().UnixMilli()
	if last := clipboardMissWarnAt.Load(); now-last > clipboardMissWarnInterval.Milliseconds() {
		clipboardMissWarnAt.Store(now)
		logger.W("QuickDock: 剪贴板序号已变化但未收到变更通知（累计 %d 次，监听存活=%v），改由轮询补抓",
			n, platform.ClipboardListenerAlive())
	}
	a.runClipboardCapture("轮询兜底")
}

// runClipboardCapture 执行一次捕获，负责重入保护、panic 隔离与卡死告警。
//
// 三者缺一不可：
//   - panic 隔离：捕获里任何一次越界/空指针都会杀掉这条常驻协程，而协程死了不会
//     自愈——之后连复制都抓不到，表现就是「软件开着越久越复制不进来」。
//     整个循环的 defer recover 只在协程退出时生效，等于没有，必须每轮都兜。
//   - 重入保护：事件与兜底轮询会同时触发。并发 OpenClipboard 只会互相抢失败，
//     还会让「另一条线程占着剪贴板」的时间翻倍。
//   - 卡死告警：GetClipboardData 在 owner 卡死时会无超时阻塞（见 platform.
//     ClipboardOwnerResponsive 的说明）。一旦发生，这条协程不会再前进；把持续
//     时长写进日志，用户下次反馈时能直接定位，而不是只能靠猜。
func (a *AppService) runClipboardCapture(trigger string) {
	if !a.clipboardBusy.CompareAndSwap(false, true) {
		since := a.clipboardBusyAt.Load()
		if since > 0 {
			stuck := time.Since(time.UnixMilli(since))
			if stuck > clipboardCaptureStuckAfter {
				now := time.Now().UnixMilli()
				if last := clipboardStuckWarnAt.Load(); now-last > clipboardStuckWarnInterval.Milliseconds() {
					clipboardStuckWarnAt.Store(now)
					logger.E("QuickDock: 剪贴板捕获已持续 %v 未返回（本次触发=%s）：读取极可能卡在延迟渲染格式上，"+
						"剪贴板也被本进程占住，其他程序的复制会同时失败", stuck.Truncate(time.Second), trigger)
				}
			}
		}
		return
	}
	a.clipboardBusyAt.Store(time.Now().UnixMilli())

	defer func() {
		if r := recover(); r != nil {
			logger.E("QuickDock: [PANIC] 剪贴板捕获(%s): %v", trigger, r)
		}
		a.clipboardBusyAt.Store(0)
		a.clipboardBusy.Store(false)
	}()

	a.captureClipboard()
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

// SetClipboardText 把文本写入系统剪贴板，返回是否成功。
//
// 走 platform 自有路径而不是 app.Clipboard.SetText：后者把写入同步派发到主线程，
// 内部还会自旋等待最多 1 秒，剪贴板被别的程序占住时会把整个 UI（热键/托盘/浮窗
// 共用那条线程）一起卡住。重试也不必在这层再套一层——WithClipboard 内部已有
// 6 次、约 150ms 的打开重试（platform.ClipboardOpenAttempts）。
//
// 返回值必须如实反映结果：旧实现无返回值，界面「点了复制、粘出来还是旧内容」
// 完全没有信号，是「无法复制」里最难排查的一类。
func SetClipboardText(text string) bool {
	if err := platform.SetClipboardText(platform.ClipboardWindowHandle(), text); err != nil {
		logger.W("QuickDock: 剪贴板写入失败: %v（文本长度=%d）", err, len(text))
		return false
	}
	// 成功路径不记日志：从历史复制是高频操作，记日志只会把日志刷满。
	// 登记回环令牌：这次写入同样会触发 WM_CLIPBOARDUPDATE（系统通知所有监听者，
	// 包括我们自己），没有令牌就会被当成一次新复制入库，CopyCount 与置顶都会失真。
	setSelfWriteText(text)
	// {clipboard} 变量读的是「最近一次复制到的内容」，本程序回写的也算——
	// 用户从历史里挑一条再粘贴，那条就是对当前剪贴板的真实描述。
	setLastClipboardText(text)
	return true
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

// ===== 回环令牌 / 重复抑制 =====

// setSelfWriteText 登记「本程序刚写进剪贴板的文本」，供下一次捕获识别并跳过。
func setSelfWriteText(s string) {
	selfWriteTextMu.Lock()
	selfWriteText = s
	selfWriteTextAt = time.Now().UnixMilli()
	selfWriteTextMu.Unlock()
}

// consumeSelfWriteText 判断本次捕获到的内容是否正是本程序刚写进去的。
//
// 令牌只消费一次：命中后立即清空，因此用户之后主动复制同一段内容仍会正常入库。
// 过期的令牌（写入时那条通知没抓到）也一并清掉并返回 false——宁可多记一条，
// 也不能长期吞掉用户的复制。
func consumeSelfWriteText(s string) bool {
	if s == "" {
		return false
	}
	selfWriteTextMu.Lock()
	defer selfWriteTextMu.Unlock()
	if selfWriteText == "" || selfWriteText != s {
		return false
	}
	fresh := time.Since(time.UnixMilli(selfWriteTextAt)) <= selfWriteTTL
	selfWriteText = ""
	selfWriteTextAt = 0
	return fresh
}

// markInsertedText 记录本次入库内容，返回 false 表示它是 duplicateWindow 内的重复通知。
func markInsertedText(s string) bool {
	now := time.Now().UnixMilli()
	lastInsertedTextMu.Lock()
	defer lastInsertedTextMu.Unlock()
	if s == lastInsertedText && now-lastInsertedAt < duplicateWindow.Milliseconds() {
		return false
	}
	lastInsertedText = s
	lastInsertedAt = now
	return true
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

// ClipboardRulesEnabledKey 剪贴板自动规则总开关的 DB 键名（唯一定义处）。
// 默认关闭：自动改写用户刚复制的内容属于侵入行为，必须由用户显式打开。
const ClipboardRulesEnabledKey = "clipboard_rules_enabled"

// applyClipboardRules 在文本入库前跑一次自动规则。
//
// 性能考虑：总开关关闭时只读一行设置就返回，不做任何规则查询 —— 剪贴板捕获是高频
// 路径，不能让它每次都去扫规则表。开关打开且规则为空时同样直接返回。
//
// 返回：(可能被转换后的文本, 是否需要回写系统剪贴板)
func (a *AppService) applyClipboardRules(text, sourceApp string) (string, bool) {
	if a.DB == nil {
		return text, false
	}
	raw, err := a.DB.GetSetting(ClipboardRulesEnabledKey)
	if err != nil || raw != "1" {
		return text, false
	}
	rules, err := a.DB.ListClipboardRules()
	if err != nil || len(rules) == 0 {
		return text, false
	}
	out, changed, writeBack := cliprule.Apply(rules, text, sourceApp)
	if !changed {
		return text, false
	}
	return out, writeBack
}
