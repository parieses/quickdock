package aria2

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 下载前的文件大小探测：只取响应头，绝不读取响应体。
//
// 为什么不能只发 HEAD：阿里云 OSS 这类预签名 URL 的签名把 HTTP 方法一起签进去了，
// 按 GET 签发的链接发 HEAD 会直接 403 SignatureDoesNotMatch。实测一个 500GiB 对象
// HEAD 403、GET + Range: bytes=0-0 才返回 Content-Range: bytes 0-0/536870912000。
// 所以 HEAD 拿不到长度时必须回退到「只取 1 字节」的 GET Range。
const (
	ProbeMaxURLs         = 30
	probeParallel        = 4
	probeTimeout         = 12 * time.Second
	probeUserAgent       = "Mozilla/5.0 (compatible; QuickDock-Download-Probe/1.0)"
	probeResponseTimeout = 10 * time.Second
)

// ProbeItem 是单个 URL 的探测结果。Size 为 -1 表示源站没有给出长度。
// Method 记录拿到长度的方式，便于诊断：head / range / length。
type ProbeItem struct {
	URL    string `json:"url"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Method string `json:"method"`
	Ranges bool   `json:"ranges"`
	Error  string `json:"error"`
}

// ProbeResult 是一次探测的汇总。Total 只累加已知长度，Unknown 是拿不到长度的条数；
// 前端拿 Total 与目标盘 Free 比对，Unknown > 0 时提示「部分链接大小未知」，不做硬拦截。
type ProbeResult struct {
	Items   []ProbeItem `json:"items"`
	Total   int64       `json:"total"`
	Unknown int         `json:"unknown"`
}

// ProbeURLs 并发探测一组 URL 的大小。ctx 由门面层统一限时，避免批量链接拖住 UI；
// 结果按输入顺序写回，调用方可以直接把 items 和输入框里的行对上。
func ProbeURLs(ctx context.Context, urls []string) ProbeResult {
	out := ProbeResult{Items: make([]ProbeItem, len(urls))}
	sem := make(chan struct{}, probeParallel)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out.Items[i] = ProbeItem{URL: raw, Size: -1, Error: "检测超时"}
				return
			}
			out.Items[i] = probeOne(ctx, raw)
		}(i, u)
	}
	wg.Wait()
	for _, it := range out.Items {
		if it.Size > 0 {
			out.Total += it.Size
		} else {
			out.Unknown++
		}
	}
	return out
}

func probeOne(ctx context.Context, raw string) ProbeItem {
	// 用原始字符串发请求：预签名 URL 把查询串一起签进去了，任何重新编码
	// （u.String() 会规范转义）都会让签名失配，net/url 只用于校验和取文件名。
	clean := strings.TrimSpace(raw)
	item := ProbeItem{URL: clean, Size: -1}
	u, err := neturl.Parse(clean)
	if err != nil || u.Host == "" {
		item.Error = "链接格式无效"
		return item
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		item.Error = "仅支持 HTTP / HTTPS 链接"
		return item
	}
	item.Name = probeFileName(u)

	// 探测是一次性短连接：关掉 keep-alive，避免为几十个链接留下悬挂连接。
	tr := &http.Transport{
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: probeResponseTimeout,
		TLSHandshakeTimeout:   8 * time.Second,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: probeTimeout}

	if size, ranges, ok := probeHead(ctx, client, item.URL); ok {
		item.Size, item.Method, item.Ranges = size, "head", ranges
		return item
	}
	size, ranges, method, err := probeRange(ctx, client, item.URL)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	item.Size, item.Method, item.Ranges = size, method, ranges
	return item
}

// probeHead 先试 HEAD。签名只签 GET 的站点会返回 403，此时 ok=false 交给 probeRange。
func probeHead(ctx context.Context, client *http.Client, raw string) (int64, bool, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, raw, nil)
	if err != nil {
		return 0, false, false
	}
	req.Header.Set("User-Agent", probeUserAgent)
	// 显式 identity：否则 Go 会自动加 Accept-Encoding: gzip 并解压，
	// 那时 ContentLength 变成 -1，大文件会被误判成「大小未知」。
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.ContentLength <= 0 {
		return 0, false, false
	}
	return resp.ContentLength, true, supportsRanges(resp.Header.Get("Accept-Ranges"))
}

// probeRange 用 1 字节的 GET 换取 Content-Range 里的总长度。
// 关键：client.Do 一拿到响应头就返回，这里立刻 Close 掉 body，从不读文件字节；
// 万一服务端忽略 Range 直接回 200，Content-Length 仍是可用的总长度，但同样不读 body。
func probeRange(ctx context.Context, client *http.Client, raw string) (int64, bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, false, "", err
	}
	req.Header.Set("User-Agent", probeUserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false, "", err
	}
	defer resp.Body.Close()

	ranges := supportsRanges(resp.Header.Get("Accept-Ranges"))
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			return total, true, "range", nil
		}
		// 少数服务端回 206 却不给 Content-Range，退而用本次响应长度。
		if resp.ContentLength > 0 {
			return resp.ContentLength, ranges, "length", nil
		}
	case http.StatusOK:
		if resp.ContentLength > 0 {
			return resp.ContentLength, ranges, "length", nil
		}
		return 0, ranges, "", fmt.Errorf("源站未返回文件大小（chunked 传输）")
	default:
		return 0, ranges, "", fmt.Errorf("源站返回 HTTP %d", resp.StatusCode)
	}
	return 0, ranges, "", fmt.Errorf("源站未返回文件大小")
}

func parseContentRangeTotal(v string) (int64, bool) {
	i := strings.LastIndex(v, "/")
	if i < 0 {
		return 0, false
	}
	total := strings.TrimSpace(v[i+1:])
	if total == "" || total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	return n, err == nil && n > 0
}

func supportsRanges(v string) bool {
	return strings.EqualFold(strings.TrimSpace(v), "bytes")
}

func probeFileName(u *neturl.URL) string {
	if u.Path == "" {
		return ""
	}
	name := path.Base(u.Path)
	if decoded, err := neturl.PathUnescape(name); err == nil {
		name = decoded
	}
	if name == "." || name == "/" || name == "" {
		return ""
	}
	return name
}
