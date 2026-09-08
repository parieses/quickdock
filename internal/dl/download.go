// Package dl 提供通用的 HTTP 并行分块下载能力，供 services 与 services/plugin 共用，
// 避免在 services 与子包之间产生循环依赖。
package dl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
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
func DownloadParallelTo(client *http.Client, url string, f *os.File, total int64, progress func(downloaded int64)) error {
	chunk := (total + DownloadConcurrency - 1) / DownloadConcurrency
	g, ctx := errgroup.WithContext(context.Background())
	for i := 0; i < DownloadConcurrency; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if end >= total {
			end = total - 1
		}
		if start > end {
			continue // 尾部空段
		}
		g.Go(func() error {
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(attempt) * 500 * time.Millisecond): // 退避 0/500ms/1s
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return err
				}
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
				resp, err := client.Do(req)
				if err != nil {
					lastErr = err
					continue
				}
				if resp.StatusCode != http.StatusPartialContent {
					io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
					resp.Body.Close()
					lastErr = fmt.Errorf("Range 请求返回 HTTP %d", resp.StatusCode)
					continue
				}
				buf := make([]byte, 64*1024)
				offset := start
				var werr error
				for {
					n, rerr := resp.Body.Read(buf)
					if n > 0 {
						if _, e := f.WriteAt(buf[:n], offset); e != nil {
							werr = e
							break
						}
						offset += int64(n)
						if progress != nil {
							progress(int64(n))
						}
					}
					if rerr != nil {
						if rerr != io.EOF {
							werr = rerr
						}
						break
					}
				}
				resp.Body.Close()
				if werr == nil && offset == end+1 {
					return nil
				}
				if werr == nil {
					werr = fmt.Errorf("分段 %d-%d 数据不完整", start, end)
				}
				lastErr = werr
			}
			return lastErr
		})
	}
	return g.Wait()
}
