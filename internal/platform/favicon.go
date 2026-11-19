package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 共享 HTTP 客户端：favicon 抓取复用连接（连接级 Timeout 仍生效）
var faviconClient = &http.Client{Timeout: 2500 * time.Millisecond}

// faviconBudget 一次抓取的**总**预算：根 favicon + 首页 HTML + <link> 图标，
// 全部加起来也不能超过这个时间。单请求各有 2.5s 超时，串联三轮就是 7.5s，
// 而抓图标这件事整体只值 3s。
const faviconBudget = 3 * time.Second

// 预编译正则：避免每次抓取 favicon 时重新编译
var linkTagRe = regexp.MustCompile(`(?i)<link\b[^>]*>`)

// attrReCache 按属性名缓存编译后的正则（attr 每调用都会拼正则，抓取多个 link 标签时反复编译）
var attrReCache sync.Map // name → *regexp.Regexp（两种模式各存一份）

// FetchFavicon 抓取网页 URL 站点的 favicon，返回 base64 data URL（PNG/JPEG/ICO/SVG）。
// 用于「网页」类型 item 自动填充图标。失败或超时返回空字符串。
// 结果按站点主机名缓存到磁盘，避免每次保存重复请求网络。
//
// 安全性：
//   - 仅当响应体确实是图像（按 magic bytes 嗅探）时才接受，避免把站点返回的
//     HTML 错误页（如 SPA 的 index.html）当成图标存进数据库；
//   - 只访问 http/https、默认端口、非环回/私网的、与条目同 host 的地址。
//     条目值来自用户，链接里的图标 href 也可能指向任意地方（http://127.0.0.1:6379
//     之类），不设限等于给了一个「保存条目即可探测内网」的原语；
//   - 总预算 3s：保存路径不等人，抓不到就用默认图标。
func FetchFavicon(rawURL string) string {
	ctx, cancel := context.WithTimeout(context.Background(), faviconBudget)
	defer cancel()

	u, err := parseSiteURL(rawURL)
	if err != nil {
		return ""
	}
	if !safeFaviconURL(ctx, u) {
		return ""
	}
	host := u.Host
	if host == "" {
		return ""
	}

	// 1. 命中磁盘缓存
	cacheKey := "fav_" + hostHash(host)
	cachePath := filepath.Join(iconCacheDir(), cacheKey)
	if data, err := os.ReadFile(cachePath); err == nil {
		s := string(data)
		// 旧缓存可能误存了站点的 HTML 错误页，丢弃重抓
		if strings.HasPrefix(s, "data:text/html") {
			_ = os.Remove(cachePath)
		} else {
			return s
		}
	}

	// 2. 优先尝试根路径 /favicon.ico（先 https 后 http）
	for _, scheme := range []string{"https", "http"} {
		if dataURL, ok := tryFetchIcon(ctx, scheme+"://"+host+"/favicon.ico", host); ok {
			writeFavCache(cachePath, dataURL)
			return dataURL
		}
	}

	// 3. 解析首页 HTML 中的 <link rel="icon"> 等，定位真实图标地址
	if dataURL, ok := discoverIconFromHTML(ctx, host); ok {
		writeFavCache(cachePath, dataURL)
		return dataURL
	}

	return ""
}

// safeFaviconURL 校验图标抓取目标：只允许 http/https、默认端口、
// 非环回/私网/链路本地地址。域名会做一次 DNS 解析，任一地址落在内网即拒绝
// （防 DNS 指向 127.0.0.1 / 169.254.x.x 这类绕过）。
func safeFaviconURL(ctx context.Context, u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !isPrivateIP(ip)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if isPrivateIP(a.IP) {
			return false
		}
	}
	return true
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// sameHost 判断两个 host[:port] 是否指向同一站点（大小写不敏感，忽略默认端口）。
func sameHost(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimSuffix(s, ":80")
		s = strings.TrimSuffix(s, ":443")
		return s
	}
	return norm(a) == norm(b)
}

// tryFetchIcon 抓取给定 URL 并校验其为图像，成功返回 data URL。
// expectHost 非空时要求目标与条目同 host（图标可能来自 CDN，但那等于让
// 插件/用户借 favicon 抓取去访问任意域名，代价大于收益，一律拒绝）。
func tryFetchIcon(ctx context.Context, urlStr, expectHost string) (string, bool) {
	body, _, ok := fetchWithTimeout(ctx, urlStr, expectHost)
	if !ok {
		return "", false
	}
	mime, isImg := sniffImage(body)
	if !isImg {
		return "", false
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body)
	return dataURL, true
}

// discoverIconFromHTML 抓取首页并解析 <link rel="icon/shortcut icon/apple-touch-icon"> 的 href，
// 再抓取该图标地址。返回 data URL。
func discoverIconFromHTML(ctx context.Context, host string) (string, bool) {
	for _, scheme := range []string{"https", "http"} {
		html, ok := fetchHTML(ctx, scheme+"://"+host+"/", host)
		if !ok {
			continue
		}
		href := findIconHref(html)
		if href == "" {
			continue
		}
		abs := resolveURL(scheme, host, href)
		if abs == "" {
			continue
		}
		if dataURL, ok := tryFetchIcon(ctx, abs, host); ok {
			return dataURL, true
		}
	}
	return "", false
}

// sniffImage 按文件头（magic bytes）判断是否为图像，并返回对应 MIME
func sniffImage(data []byte) (string, bool) {
	if len(data) < 4 {
		return "", false
	}
	switch {
	case data[0] == 0x00 && data[1] == 0x00 && data[2] == 0x01 && data[3] == 0x00:
		return "image/x-icon", true // ICO
	case bytes.HasPrefix(data, []byte("\x89PNG")):
		return "image/png", true
	case bytes.HasPrefix(data, []byte("GIF8")):
		return "image/gif", true
	case data[0] == 'B' && data[1] == 'M':
		return "image/bmp", true
	case bytes.HasPrefix(data, []byte("RIFF")) && len(data) >= 12 && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", true
	}
	// SVG（XML 文本）。仅当确实包含 svg 标记时才认作图像，避免误收普通 HTML/XML
	head := string(data[:min(512, len(data))])
	if strings.Contains(head, "<svg") || strings.Contains(head, "http://www.w3.org/2000/svg") {
		return "image/svg+xml", true
	}
	return "", false
}

// fetchHTML 抓取首页 HTML 文本（仅要求 200，不限 MIME）
func fetchHTML(ctx context.Context, urlStr, expectHost string) (string, bool) {
	u, err := url.Parse(urlStr)
	if err != nil || !safeFaviconURL(ctx, u) {
		return "", false
	}
	if expectHost != "" && !sameHost(u.Host, expectHost) {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := faviconClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// findIconHref 从 HTML 中找出图标 link 的 href。优先级：icon/shortcut icon > apple-touch-icon
func findIconHref(html string) string {
	links := linkTagRe.FindAllString(html, -1)
	var fallback string
	for _, tag := range links {
		rel := attr(tag, "rel")
		relLower := strings.ToLower(rel)
		if !strings.Contains(relLower, "icon") {
			continue
		}
		href := attr(tag, "href")
		if href == "" {
			continue
		}
		if strings.Contains(relLower, "apple-touch-icon") {
			if fallback == "" {
				fallback = href
			}
			continue
		}
		return href // icon / shortcut icon 优先
	}
	return fallback
}

// attr 从单个 HTML 标签中取出某属性的值（支持带引号与无引号），正则按属性名缓存
func attr(tag, name string) string {
	cacheKey := "q_" + name // quoted
	if v, ok := attrReCache.Load(cacheKey); ok {
		if m := v.(*regexp.Regexp).FindStringSubmatch(tag); m != nil {
			return m[1]
		}
	} else {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `=["']([^"']*)["']`)
		attrReCache.Store(cacheKey, re)
		if m := re.FindStringSubmatch(tag); m != nil {
			return m[1]
		}
	}
	cacheKey = "u_" + name // unquoted
	if v, ok := attrReCache.Load(cacheKey); ok {
		if m := v.(*regexp.Regexp).FindStringSubmatch(tag); m != nil {
			return m[1]
		}
	} else {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `=([^\s>]+)`)
		attrReCache.Store(cacheKey, re)
		if m := re.FindStringSubmatch(tag); m != nil {
			return m[1]
		}
	}
	return ""
}

// resolveURL 将 href 解析为绝对 URL
func resolveURL(scheme, host, href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	if strings.HasPrefix(href, "//") {
		return scheme + ":" + href
	}
	if strings.HasPrefix(href, "/") {
		return scheme + "://" + host + href
	}
	// 相对路径（站点根目录场景足够）
	return scheme + "://" + host + "/" + strings.TrimPrefix(href, "./")
}

func writeFavCache(path, dataURL string) {
	_ = os.WriteFile(path, []byte(dataURL), 0o644)
}

// parseSiteURL 兼容裸域名（如 github.com）与完整 URL，返回含 Host 的 *url.URL
func parseSiteURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	u, err := url.Parse(s)
	// url.Parse("github.com") 不报错但 Host 为空（它把整串当 Path），
	// 所以判断条件是「没有 Host」而不是「解析出错」——否则裸域名永远拿不到图标。
	if err != nil || u.Host == "" {
		u, err = url.Parse("https://" + s)
		if err != nil {
			return nil, err
		}
	}
	if u.Host == "" {
		return nil, fmt.Errorf("no host")
	}
	return u, nil
}

// fetchWithTimeout 在总预算内发起 GET，读取最多 512KB（复用 faviconClient 连接池）。
// expectHost 非空时要求目标与条目同 host；任何情况下都拒绝环回/私网/非默认端口。
func fetchWithTimeout(ctx context.Context, urlStr, expectHost string) ([]byte, string, bool) {
	u, err := url.Parse(urlStr)
	if err != nil || !safeFaviconURL(ctx, u) {
		return nil, "", false
	}
	if expectHost != "" && !sameHost(u.Host, expectHost) {
		return nil, "", false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := faviconClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	limited := io.LimitReader(resp.Body, 512*1024)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", false
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/x-icon"
	}
	return data, mime, true
}

// hostHash 对主机名做 FNV-1a 短哈希（缓存文件名）
func hostHash(host string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(host)))
	return fmt.Sprintf("%08x", h.Sum32())
}
