package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProxyTransport 返回代理感知的 http transport（环境变量 + 系统代理，Windows 下含 WinINET 注册表）。
func ProxyTransport() http.RoundTripper {
	return proxyTransport()
}

// downloadMaxAttempts 单个下载地址的最大重试次数（指数退避）。
// MySQL/MariaDB 等大包网络抖动即整体失败，重试可显著提升成功率。
const downloadMaxAttempts = 4

// httpFatalError 表示「重试无益」的 HTTP 响应：4xx（除 408 超时 / 429 限流）多为
// 地址不存在（404）、源已下线（410）等确定性错误，重试只会白白拉长失败时间
// （实测 404 也退避重试 4 次约 3.5s，且每个候选源都重来一遍）。
// 上层据此跳过该地址的重试循环，直接尝试下一个候选源。
type httpFatalError struct {
	Code int // HTTP 状态码；0 表示非状态类确定性失败（如源站返回 HTML 错误页）
	URL  string
	Msg  string // Code==0 时的自定义描述
}

func (e *httpFatalError) Error() string {
	if e.Code > 0 {
		return fmt.Sprintf("下载失败 HTTP %d: %s", e.Code, e.URL)
	}
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("下载失败: %s", e.URL)
}

// isFatalStatus 判断 HTTP 状态码是否属于「重试无益」。
// 408（请求超时）与 429（限流）虽是 4xx，但属临时性故障，保留重试。
func isFatalStatus(code int) bool {
	return code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// Download 依次尝试 urls 下载到 dst；任意一个地址成功即返回。带进度回调与代理穿透。
// 下载先写临时文件，完整成功后才落到 dst，避免中途失败污染目标。
// 每个地址最多重试 downloadMaxAttempts 次（指数退避）；命中 httpFatalError（4xx）时
// 不重试直接换下一个地址。全部失败再尝试下一个候选地址。
func Download(ctx context.Context, dst string, urls []string, onProgress func(written, total int64)) error {
	var lastErr error
	for _, u := range urls {
		var attemptErr error
		for attempt := 0; attempt < downloadMaxAttempts; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
			if attemptErr = downloadOne(ctx, u, dst, onProgress); attemptErr == nil {
				return nil
			}
			lastErr = attemptErr
			var fe *httpFatalError
			if errors.As(attemptErr, &fe) {
				break // 4xx 确定性失败，放弃该地址的重试
			}
		}
		// 该地址多次重试仍失败，继续尝试下一个候选地址
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用下载地址")
	}
	return lastErr
}

// newDownloadClient 构造下载用 client，并在跟随重定向时按需改写目标地址。
//
// 为什么要在重定向这一步改写：部分下载源给的是「重定向 API」而不是静态文件——
// JDK 的 api.adoptium.net/v3/binary/latest/... 会 307 到 github.com 的 Release 资产。
// 候选地址里根本没有 github.com，CandidateURLs 的加速改写无从下手，于是国内直连时
// 请求在重定向后的 github 连接上一直卡到超时（表现为「下载停在 0%」）。
// 在这条路径上改写，才能让「GitHub 下载加速」覆盖所有走 github.com 的流量，而不只是
// 恰好把 github 写进候选清单的那几个源。
func newDownloadClient() *http.Client {
	return &http.Client{
		Transport: ProxyTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("重定向层数过多（>10），疑似跳转环: %s", req.URL)
			}
			if GitHubAccelerateEnabled() {
				if p := ghProxyURL(req.URL.String()); p != "" {
					if u, err := url.Parse(p); err == nil {
						req.URL = u
						// Host 必须同步：http 包用 req.Host（若为空则回落到 URL.Host）建连与发 Host 头
						req.Host = u.Host
					}
				}
			}
			return nil
		},
	}
}

func downloadOne(ctx context.Context, urlStr, dst string, onProgress func(written, total int64)) error {
	// stall 保护：即便调用方 ctx 无 deadline（如 Manager.Install 传 context.Background()），
	// 服务器返回 200 后 body 中途长时间无新字节也不得永久挂起。
	// 派生一个可取消 ctx：stallTimer 在"持续无新数据超过 stallTimeout"时触发取消，
	// 中断阻塞中的 resp.Body.Read（http.NewRequestWithContext 的 body 读会随 ctx 取消而中止）。
	stallTimeout := 60 * time.Second
	dlCtx, stallCancel := context.WithCancel(ctx)
	defer stallCancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	// 显式声明不收 gzip：下载目标恒为 .zip/.tar.gz（本身已是压缩格式），而默认 Go 会自动附上
	// Accept-Encoding: gzip，个别源（实测 Go 官方包经 dl.google.com 分发）会据此 gzip 响应，
	// Go 随即透明解压并把 resp.ContentLength 置为 -1（Uncompressed=true）→ 进度回调拿不到 total，
	// 前端 percent() 退化只显示「已下载 N MB」，百分比永不出现（67MB 的 Go 包实测 CL=67418204 vs -1）。
	// 注意不要改用 Transport.DisableCompression：Clone() 出来的 transport 会破坏 HTTP/2 握手
	// （实测 h2 源全部报 malformed HTTP response / EOF），请求头方式无此副作用。
	req.Header.Set("Accept-Encoding", "identity")
	client := newDownloadClient()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isFatalStatus(resp.StatusCode) {
			return &httpFatalError{Code: resp.StatusCode, URL: urlStr}
		}
		return fmt.Errorf("下载失败 HTTP %d: %s", resp.StatusCode, urlStr)
	}
	// 防御：部分源（如 Apache Lounge）对不存在的路径返回 HTTP 200 + text/html 错误页，
	// 若把 HTML 当成 zip 下载，解压阶段才会报 "zip: not a valid zip file"，极难定位。
	// 这里在下载阶段提前拦截（仅对 .zip 目标）。
	if strings.HasSuffix(strings.ToLower(dst), ".zip") {
		if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "text/html") {
			// 源站把错误页包上 200 返回时，重试同样内容毫无意义，按确定性失败处理
			return &httpFatalError{URL: urlStr, Msg: fmt.Sprintf("下载源返回了 HTML 页面而非 ZIP 压缩包（HTTP %d, Content-Type=%s）: %s", resp.StatusCode, ct, urlStr)}
		}
	}

	tmp, err := os.CreateTemp("", "quickdock-dl-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpPath) }()

	total := resp.ContentLength
	written := int64(0)
	buf := make([]byte, 64*1024)

	// stall 监控 goroutine：progress 收到一次"有新数据"即重置定时器；定时器到点（超时无数据）
	// 调用 stallCancel 令 dlCtx 取消，从而中止阻塞中的 body 读。done 用于随函数退出回收 goroutine。
	progress := make(chan struct{}, 1)
	stallTimer := time.NewTimer(stallTimeout)
	done := make(chan struct{})
	defer func() { close(done); stallTimer.Stop() }()
	go func() {
		for {
			select {
			case <-stallTimer.C:
				stallCancel()
				return
			case <-progress:
				if !stallTimer.Stop() {
					select {
					case <-stallTimer.C:
					default:
					}
				}
				stallTimer.Reset(stallTimeout)
			case <-done:
				return
			}
		}
	}()

	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
			select {
			case progress <- struct{}{}:
			default:
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 区分"无数据停滞被取消"与真实错误，给用户可读提示
			if ctxErr := dlCtx.Err(); ctxErr != nil && rerr == context.Canceled {
				return fmt.Errorf("下载停滞（超过 %s 无新数据）已中断: %w", stallTimeout, ctxErr)
			}
			return rerr
		}
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// 调用方常把 zip 落到 os.TempDir()/quickdock-runtime/ 之类的子目录，dst 父目录可能不存在，
	// os.Create 会直接报 "The system cannot find the path specified"。提前建好父目录。
	if dir := filepath.Dir(dst); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建下载目录失败 %s: %w", dir, err)
		}
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, tmp); err != nil {
		return err
	}
	// 落盘后再做一次魔数校验，拦截「后缀为 .zip 但内容不是 zip」的情况
	// （如源站对不存在路径返回 HTML 错误页但 HTTP 200，Content-Type 未必标记 text/html）。
	if strings.HasSuffix(strings.ToLower(dst), ".zip") {
		if err := verifyZipMagic(dst); err != nil {
			os.Remove(dst)
			// 同为确定性失败（内容不是 zip），包装成 httpFatalError 让上层跳过重试
			return &httpFatalError{URL: urlStr, Msg: fmt.Sprintf("%v（下载源可能返回了错误页）: %s", err, urlStr)}
		}
	}
	return nil
}

// verifyZipMagic 校验文件是否为合法的 ZIP 压缩包（检查本地文件头魔数）。
// 仅接受 PK\x03\x04（常规）、PK\x05\x06（空归档）、PK\x07\x08（分卷）三种起始签名。
func verifyZipMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, 4)
	n, rerr := io.ReadFull(f, hdr)
	if rerr != nil && rerr != io.EOF {
		return rerr
	}
	if n < 4 {
		return fmt.Errorf("下载文件过小，可能不是有效的 ZIP 压缩包")
	}
	if !bytes.Equal(hdr, []byte("PK\x03\x04")) &&
		!bytes.Equal(hdr, []byte("PK\x05\x06")) &&
		!bytes.Equal(hdr, []byte("PK\x07\x08")) {
		return fmt.Errorf("下载文件不是有效的 ZIP 压缩包（下载源可能返回了错误页）")
	}
	return nil
}
