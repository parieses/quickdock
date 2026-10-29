// Package dl 提供通用的 HTTP 并行分块下载能力，供 services 与 services/plugin 共用，
// 避免在 services 与子包之间产生循环依赖。
package dl

import (
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// DlProgressWriter / ProgressTracker：下载进度节流上报（plugin:download-progress 事件）。
// 至少间隔 minInterval 且（total 已知时）百分比变化才触发，避免事件风暴。
// total<=0（chunked 传输无 Content-Length）时 percent 恒 0，前端回退显示已下载 MB。
type ProgressTracker struct {
	mu       sync.Mutex
	total    int64
	done     int64
	lastPct  int
	lastEmit time.Time
	emit     func(downloaded, total int64, percent int)
}

// NewProgressTracker 构造下载进度追踪器。total 为总字节数（<=0 表示未知，百分比恒 0）；
// emit 在节流规则满足时被调用（已下载字节、总字节、百分比）。
func NewProgressTracker(total int64, emit func(downloaded, total int64, percent int)) *ProgressTracker {
	return &ProgressTracker{total: total, lastPct: -1, emit: emit}
}

const ProgressMinInterval = 150 * time.Millisecond

// add 并发安全地累计 n 字节并按节流规则决定是否发事件
func (t *ProgressTracker) Add(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done += n
	pct := 0
	if t.total > 0 {
		pct = int(t.done * 100 / t.total)
		if pct > 100 {
			pct = 100
		}
	}
	if time.Since(t.lastEmit) < ProgressMinInterval {
		return
	}
	if t.total > 0 && pct == t.lastPct {
		return
	}
	t.lastPct = pct
	t.lastEmit = time.Now()
	if t.emit != nil {
		t.emit(t.done, t.total, pct)
	}
}

// Seed 预设已下载字节数。断点续传时把进度基线抬到断点处，否则续传的下载
// 会从 0% 重新爬（实际已下了一半，用户看到的是「进度倒退」）。
func (t *ProgressTracker) Seed(done int64) {
	if done <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.done = done
}

// DlProgressWriter 单流下载用：包装目标 writer 并把字节数计入 tracker
type DlProgressWriter struct {
	dst io.Writer
	tr  *ProgressTracker
}

// NewProgressWriter 构造单流下载进度包装 writer（dst 与 tracker 均必填）。
func NewProgressWriter(dst io.Writer, tr *ProgressTracker) *DlProgressWriter {
	return &DlProgressWriter{dst: dst, tr: tr}
}

func (w *DlProgressWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.tr.Add(int64(n))
	}
	return n, err
}

// 并发分块下载参数：仅对支持 Accept-Ranges 且体积超阈值的包启用，
// 小包/不支持 Range 的源自动回退单流（避免小包并发握手开销与兼容性问题）
const (
	DownloadConcurrency = 4
	ParallelMinSize     = 2 << 20 // 2MB
)

// DownloadParallelTo 把 url 按 Range 切 DownloadConcurrency 段并行下载，
// 各段 WriteAt 写入已按 total 预分配的文件的对应偏移（不同偏移无写竞争）；
// 每段最多重试 3 次，任一段最终失败即整体失败并 cancel 其余段。
// progress 每收到一个数据块回调一次（n 为本次写入字节数），由调用方决定
// 是否节流与如何上报——插件市场走 ProgressTracker.add（节流 + 事件），
// 设置页"检查更新"走同样逻辑转发 Wails onProgress。共用同一实现避免两处分叉。
//
// 不带续传状态：进程/调用结束后进度即丢。需要跨调用续传（自动更新）用
// DownloadResumable + ResumeOptions.StatePath。
func DownloadParallelTo(client *http.Client, url string, f *os.File, total int64, progress func(downloaded int64)) error {
	return DownloadResumable(ResumeOptions{
		Client:   client,
		URL:      url,
		File:     f,
		Total:    total,
		Parts:    DownloadConcurrency,
		Progress: progress,
	})
}
