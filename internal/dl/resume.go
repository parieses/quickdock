package dl

// 断点续传：把「每块下到哪」落盘，下次（同源、同包）从断点继续。
//
// 为什么需要：安装包动辄几十 MB，GitHub 直连常被限速到几十 KB/s，单次尝试
// 3 分钟超时是常态。没有续传时，超时一次就得从 0 重来，网络越差越下不完——
// 这不是「慢」，是「永远更新不了」。
//
// 一致性：状态文件只在数据 Sync 之后写，保证「记了 N 字节，文件里就真有 N 字节」；
// 换源（直连 ↔ 镜像）或包大小变化一律作废重下，避免把两个来源的字节拼在一起
// 导致签名校验失败——那种失败看起来像「服务器被篡改」，实际上是自己拼错了。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	// resumeRetries 单个分块在一次下载中最多尝试的次数
	resumeRetries = 3
	// resumeSaveInterval 续传进度落盘节流：频繁写盘会拖慢下载，太稀疏又白丢进度
	resumeSaveInterval = 500 * time.Millisecond
	// resumeReadBuf 分块读取缓冲区
	resumeReadBuf = 64 * 1024
)

// ErrRangeUnsupported 源忽略了 Range 请求（返回 200 全量）。分块并发下载依赖
// Range 定位写入偏移，遇到它只能整体失败、由调用方退回单流。
var ErrRangeUnsupported = errors.New("源不支持 Range，无法分块/续传")

// ResumeState 已落盘的续传进度。
type ResumeState struct {
	Source string  `json:"source"` // 来源 URL：换源即作废
	Total  int64   `json:"total"`  // 包体总字节数
	Chunk  int64   `json:"chunk"`  // 每块大小（total/parts 向上取整）
	Parts  []int64 `json:"parts"`  // 每块已写入字节数
}

// Download 已完成的字节数（各块之和）。
func (s ResumeState) Download() int64 {
	var sum int64
	for _, v := range s.Parts {
		sum += v
	}
	return sum
}

// ReadResume 读取续传状态文件；不存在、损坏或字段不可用时返回 false。
func ReadResume(path string) (ResumeState, bool) {
	if path == "" {
		return ResumeState{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ResumeState{}, false
	}
	var st ResumeState
	if err := json.Unmarshal(raw, &st); err != nil {
		return ResumeState{}, false
	}
	if st.Total <= 0 || len(st.Parts) == 0 {
		return ResumeState{}, false
	}
	return st, true
}

// Resumer 分块下载的进度记录器，并发安全。
type Resumer struct {
	mu       sync.Mutex
	path     string
	source   string
	total    int64
	chunk    int64
	parts    []int64
	file     *os.File
	lastSave time.Time
}

// newResumer 载入（或新建）续传状态。源/总长/分块方案任一不匹配都从头开始：
// 半成品的价值远小于拼错包体的代价。
func newResumer(path, source string, total, chunk int64, n int, f *os.File) *Resumer {
	r := &Resumer{
		path:     path,
		source:   source,
		total:    total,
		chunk:    chunk,
		parts:    make([]int64, n),
		file:     f,
		lastSave: time.Now(),
	}
	if path == "" {
		return r
	}
	st, ok := ReadResume(path)
	if !ok || st.Source != source || st.Total != total || st.Chunk != chunk || len(st.Parts) != n {
		return r
	}
	copy(r.parts, st.Parts)
	return r
}

// Downloaded 返回已写入的总字节数。
func (r *Resumer) Downloaded() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sum int64
	for _, v := range r.parts {
		sum += v
	}
	return sum
}

// Part 返回第 i 块已写入的字节数。
func (r *Resumer) Part(i int) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.parts) {
		return 0
	}
	return r.parts[i]
}

// Advance 记录第 i 块新增 n 字节，并按节流间隔触发落盘。
func (r *Resumer) Advance(i int, n int64) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	if i >= 0 && i < len(r.parts) {
		r.parts[i] += n
	}
	due := r.path != "" && time.Since(r.lastSave) >= resumeSaveInterval
	r.mu.Unlock()
	if due {
		r.Checkpoint()
	}
}

// Checkpoint 立即落盘（先 Sync 数据文件再写状态，保证状态不会超前于数据）。
func (r *Resumer) Checkpoint() {
	if r.path == "" {
		return
	}
	r.mu.Lock()
	if r.file != nil {
		_ = r.file.Sync()
	}
	st := ResumeState{
		Source: r.source,
		Total:  r.total,
		Chunk:  r.chunk,
		Parts:  append([]int64(nil), r.parts...),
	}
	r.lastSave = time.Now()
	r.mu.Unlock()

	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	// 先写临时文件再 rename：状态文件被写坏会让下次续传读到半截 JSON 而整包重下
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		_ = os.Remove(tmp)
	}
}

// Reset 清空进度（源忽略 Range、只能从头重来时用），保留状态文件与总长。
func (r *Resumer) Reset() {
	r.mu.Lock()
	for i := range r.parts {
		r.parts[i] = 0
	}
	r.mu.Unlock()
	r.Checkpoint()
}

// Discard 丢弃进度并删除状态文件：包体本身变了（源文件被重发、镜像缓存过期），
// 留着旧 Total 会让下一次调用继续按错误的方案切块，永远修不好。
func (r *Resumer) Discard() {
	r.mu.Lock()
	for i := range r.parts {
		r.parts[i] = 0
	}
	r.mu.Unlock()
	r.Done()
}

// Done 删除状态文件（整包下载完成，缓存文件由调用方处理）。
func (r *Resumer) Done() {
	if r.path == "" {
		return
	}
	_ = os.Remove(r.path)
	_ = os.Remove(r.path + ".tmp")
}

// ResumeOptions DownloadResumable 的参数。
type ResumeOptions struct {
	// Ctx 整体取消/超时，会透传到每个分块请求
	Ctx context.Context
	// Client HTTP 客户端（必填）
	Client *http.Client
	// URL 下载地址，同时作为续传状态的来源标识——换地址即作废半成品
	URL string
	// File 目标文件（必填，可写），内部会先按 Total 预分配长度
	File *os.File
	// Total 包体总字节数（必填，>0）
	Total int64
	// Parts 并发分块数，1 表示单流；>1 需要源支持 Range
	Parts int
	// StatePath 续传状态文件路径；空表示不落盘（只在本次调用的重试之间有效）
	StatePath string
	// OnResume 开始下载前回调已存在的字节数（调用方据此抬高进度基线）
	OnResume func(downloaded int64)
	// Progress 每收到一块数据回调本次新增字节数
	Progress func(n int64)
}

// DownloadResumable 把 URL 下载到 opt.File，支持中断后续传。
//
// 与 DownloadParallelTo 的区别：进度可落盘（StatePath）跨调用/跨进程续传，
// 且每块内部失败也从该块的断点继续，而不是整块重下——3 分钟超时恰好卡在
// 大块中间时，重下整块等于永远下不完。
func DownloadResumable(opt ResumeOptions) error {
	if opt.Client == nil {
		return errors.New("缺少 HTTP 客户端")
	}
	if opt.File == nil {
		return errors.New("缺少目标文件")
	}
	if opt.Total <= 0 {
		return errors.New("包体大小未知，无法分块下载")
	}
	if opt.Parts < 1 {
		opt.Parts = 1
	}
	ctx := opt.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// 预分配总长：各块直接 WriteAt 到自己的偏移，最后文件长度天然等于包体大小
	if err := opt.File.Truncate(opt.Total); err != nil {
		return err
	}

	chunk := (opt.Total + int64(opt.Parts) - 1) / int64(opt.Parts)
	rs := newResumer(opt.StatePath, opt.URL, opt.Total, chunk, opt.Parts, opt.File)
	if opt.OnResume != nil {
		opt.OnResume(rs.Downloaded())
	}

	g, gctx := errgroup.WithContext(ctx)
	for i := 0; i < opt.Parts; i++ {
		start := int64(i) * chunk
		if start >= opt.Total {
			break // 尾部空段（包体小于块数时）
		}
		end := start + chunk - 1
		if end >= opt.Total {
			end = opt.Total - 1
		}
		// Go 1.22+ 的循环变量是每次迭代独立的，闭包直接捕获即可
		g.Go(func() error {
			return downloadPart(gctx, opt, rs, i, start, end)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	rs.Done()
	return nil
}

// downloadPart 下完 [start, end] 这一段，可从中断处继续。
func downloadPart(ctx context.Context, opt ResumeOptions, rs *Resumer, idx int, start, end int64) error {
	need := end - start + 1
	done := rs.Part(idx)
	if done > need {
		done = need
	}
	var lastErr error

	for attempt := 0; attempt < resumeRetries; attempt++ {
		if done >= need {
			rs.Checkpoint()
			return nil
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt-1) * 500 * time.Millisecond): // 退避 0/500ms/1s
			}
		}

		// 续传点：本块起点 + 已写入字节
		from := start + done
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, end))
		// 必须禁用压缩：中间层若对 Range 响应做 gzip，偏移就和包体对不上了
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("User-Agent", "QuickDock-Updater/1.0")

		resp, err := opt.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusOK {
			// 源忽略了 Range，返回整份内容
			if opt.Parts > 1 {
				resp.Body.Close()
				return ErrRangeUnsupported
			}
			if done > 0 {
				// 单流续传被拒：只能丢掉已下部分从头来（此响应就是完整包，直接复用）
				rs.Reset()
				done = 0
			}
			from = start
		} else if resp.StatusCode != http.StatusPartialContent {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
			continue
		} else if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok && total != opt.Total {
			// 源上的文件换了（版本被重发/镜像缓存过期）：偏移不再可信，
			// 连状态一起丢掉，下次调用重新探测总长
			resp.Body.Close()
			rs.Discard()
			return fmt.Errorf("源文件大小变化（%d ≠ %d），已丢弃断点，请重试", total, opt.Total)
		}

		writeAt := from
		buf := make([]byte, resumeReadBuf)
		var readErr error
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				// 服务器可能多给（忽略 Range 时的整份内容），超出本块的部分丢弃
				remain := need - done
				if remain <= 0 {
					break
				}
				if int64(n) > remain {
					n = int(remain)
				}
				if n > 0 {
					if _, werr := opt.File.WriteAt(buf[:n], writeAt); werr != nil {
						resp.Body.Close()
						return werr
					}
					writeAt += int64(n)
					done += int64(n)
					rs.Advance(idx, int64(n))
					if opt.Progress != nil {
						opt.Progress(int64(n))
					}
				}
			}
			if rerr != nil {
				if rerr != io.EOF {
					readErr = rerr
				}
				break
			}
		}
		resp.Body.Close()

		if done >= need {
			rs.Checkpoint()
			return nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = fmt.Errorf("分段 %d-%d 数据不完整（%d/%d）", start, end, done, need)
		}
		// 已写入的部分由 Advance 落盘，下次尝试从这里继续
		rs.Checkpoint()
	}

	// 失败也要落盘：下次（含换镜像重试、应用重启后重下）能从断点继续
	rs.Checkpoint()
	if lastErr == nil {
		lastErr = fmt.Errorf("分段 %d-%d 下载失败", start, end)
	}
	return lastErr
}

// parseContentRangeTotal 解析 "bytes 100-199/12345" 的总长。
func parseContentRangeTotal(v string) (int64, bool) {
	slash := -1
	for i := len(v) - 1; i >= 0; i-- {
		if v[i] == '/' {
			slash = i
			break
		}
	}
	if slash < 0 || slash == len(v)-1 {
		return 0, false
	}
	var total int64
	if _, err := fmt.Sscanf(v[slash+1:], "%d", &total); err != nil || total <= 0 {
		return 0, false
	}
	return total, true
}
