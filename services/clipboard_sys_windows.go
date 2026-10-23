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

	// clipboardOpenFailStreak 连续「读不到剪贴板」的次数。剪贴板被其他进程锁死时
	// 每次变更都会失败，累计到这里用于把静默失败升级成一条可诊断的错误日志。
	clipboardOpenFailStreak atomic.Int64
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
	// 序号必须在**读取之前**取。读取期间别的程序写不进剪贴板（被我们占着），
	// 但万一发生，用读取前的旧序号标记已处理，兜底轮询还能发现并补抓一次。
	seqBefore := platform.ClipboardSequenceNumber()
	var snap clipboardSnapshot
	// WithClipboardRead：打开后先确认 owner 窗口能响应再读。owner 卡死时
	// GetClipboardData 会无超时阻塞，而那一刻剪贴板正被本进程占着——全系统
	// 复制随后全部失败，只能重启恢复（详见该函数的注释）。
	if !platform.WithClipboardRead(hwnd, func() {
		readClipboardSnapshot(&snap)
	}) {
		// 连续打不开通常不是本程序的问题：剪贴板是全局独占资源，被别的进程
		// （尤其是以管理员权限运行、又没有及时 CloseClipboard 的进程）锁住时，
		// 所有程序都会返回 ERROR_ACCESS_DENIED。累计到一定次数后升级为 E 级，
		// 让用户/排查者能立刻分辨「是外部占用」还是「本程序坏了」。
		if n := clipboardOpenFailStreak.Add(1); n == 10 || n%100 == 0 {
			logger.E("QuickDock: 剪贴板已连续 %d 次无法打开：被其他进程长期占用或锁死。"+
				"此时系统里所有程序的「复制」都会失败，剪贴板历史也会停止更新（重启占用者即可恢复）", n)
		}
		return
	}
	clipboardOpenFailStreak.Store(0)
	if seqBefore != 0 {
		a.clipboardSeq.Store(seqBefore)
		clipboardMissStreak.Store(0)
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
//
// 【读取顺序按阻塞风险排，不是随手排的】
// GetClipboardData 对**延迟渲染**格式会同步 SendMessage(WM_RENDERFORMAT) 到 owner
// 窗口，而且没有超时。纯文本几乎总是「直接给内存」，文件/图片则常被延迟渲染。
// 所以这里先读文本，并且只在确实需要时才去碰文件/图片格式：
//   - 普通复制文字 → 只碰 CF_UNICODETEXT 一项，完全不接触可能阻塞的格式；
//   - 文本为空（纯图片/纯文件）或文本形如路径（资源管理器同时给了路径文本）才继续。
//
// 继续之前还要用 platform.ClipboardOwnerResponsive 探一次 owner 是否响应，
// 不响应就整块跳过——宁可丢这一次，也不能把捕获协程和整个系统剪贴板一起锁死。
func readClipboardSnapshot(snap *clipboardSnapshot) {
	// 1. CF_UNICODETEXT
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

	// 文本已经有内容、又不像路径 → 就是一次普通文本复制，到此为止。
	//
	// 文本为空（纯截图/纯图片）与文本像路径（资源管理器复制文件时同时给
	// CF_HDROP + 路径文本，"复制的图片文件"还会额外带 CF_DIB 预览）这两种情况
	// 才继续读文件/图片格式——后者不能因为"文本非空"就跳过，否则复制图片文件
	// 会退化成一条纯文件记录，历史里的缩略图就没了。
	needBinary := snap.text == "" || looksLikeFilePaths(snap.text)
	if !needBinary {
		return
	}

	// owner 响应探测不在本函数里做：它由 platform.WithClipboardRead 在进入临界区
	// 时统一执行，覆盖本文件、{clipboard} 变量与插件读取三条路径。

	// 2. CF_HDROP
	if h := w32.GetClipboardData(15); h != 0 {
		if raw := readGlobalMem(h, 1*1024*1024); len(raw) > 0 {
			snap.filePaths = platform.ParseHDROP(raw)
		} else {
			logger.W("QuickDock: HDROP 读取失败或大小越界")
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

// looksLikeFilePaths 判断文本形态是否**可能**是本地路径列表。
//
// 唯一用途是决定「要不要为它多读一次 CF_HDROP」，判错只会多读或少读一个格式，
// 不会影响最终结果（真正的判定在 platform.IsFilePathsAsText）。所以这里只做
// 廉价的形态判断：每行都像盘符路径（C:\ / C:/）或 UNC 路径（\\server\share），
// 且行数/行长都在合理范围。
func looksLikeFilePaths(text string) bool {
	if len(text) > 32*1024 {
		return false
	}
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 100 {
		return false
	}
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if len(l) == 0 || len(l) > 500 {
			return false
		}
		isDrive := len(l) >= 3 && l[1] == ':' && (l[2] == '\\' || l[2] == '/') &&
			((l[0] >= 'A' && l[0] <= 'Z') || (l[0] >= 'a' && l[0] <= 'z'))
		if !isDrive && !strings.HasPrefix(l, `\\`) {
			return false
		}
	}
	return true
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
		// 本程序刚写回剪贴板的文件路径（从历史复制文件）：只挡这一次。
		if consumeSelfWriteText(joined) {
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
	// 一次复制可能触发两条通知（文本与 CF_HDROP 先后填好），短窗口内只记一次。
	if !markInsertedText(joined) {
		return
	}
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
	if text == "" || len(strings.TrimSpace(text)) == 0 || len(text) > maxClipboardTextLen {
		return false
	}

	// 本程序刚写进剪贴板的文本 → 跳过，但算「已处理」（否则会继续落到图片分支）。
	if consumeSelfWriteText(text) {
		return true
	}

	// 同一次复制可能产生两条通知（程序先 SetClipboardData、再 Empty+Set），
	// 短窗口内只入库一次；窗口之外的相同内容仍当一次新的复制（会递增并置顶）。
	if !markInsertedText(text) {
		return true
	}

	sourceApp := platform.GetActiveWindowTitle()
	setLastClipboardText(text)

	// 异步入库：避免 DB 繁忙时阻塞剪贴板工作协程（同图片路径）
	go func() {
		defer recoverPanic("clipboard saveText")
		if a.DB == nil {
			logClipboardSkip("文本")
			return
		}
		entry, err := a.DB.InsertClipboardEntry(text, sourceApp)
		if err != nil {
			// 入库失败不再「占位」：旧实现在这里之前就已把内容记为已处理，
			// 于是同一条内容之后再也进不来（DB 短暂不可用会变成永久丢内容）。
			// 现在的去重只剩 markInsertedText 的短窗口，失败后用户再复制一次即可补上。
			logger.W("QuickDock: clipboard save failed: %v", err)
			return
		}
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
