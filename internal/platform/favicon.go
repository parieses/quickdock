package platform

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FetchFavicon 抓取网页 URL 站点的 favicon，返回 base64 data URL（PNG/JPEG/ICO/SVG）。
// 用于「网页」类型 item 自动填充图标。失败或超时返回空字符串。
// 结果按站点主机名缓存到磁盘，避免每次保存重复请求网络。
//
// 安全性：仅当响应体确实是图像（按 magic bytes 嗅探）时才接受，避免把站点返回的
// HTML 错误页（如 SPA 的 index.html）当成图标存进数据库。
func FetchFavicon(rawURL string) string {
	u, err := parseSiteURL(rawURL)
	if err != nil {
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
		if dataURL, ok := tryFetchIcon(scheme + "://" + host + "/favicon.ico"); ok {
			writeFavCache(cachePath, dataURL)
			return dataURL
		}
	}

	// 3. 解析首页 HTML 中的 <link rel="icon"> 等，定位真实图标地址
	if dataURL, ok := discoverIconFromHTML(host); ok {
		writeFavCache(cachePath, dataURL)
		return dataURL
	}

	return ""
}

// tryFetchIcon 抓取给定 URL 并校验其为图像，成功返回 data URL
func tryFetchIcon(urlStr string) (string, bool) {
	body, _, ok := fetchWithTimeout(urlStr)
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
func discoverIconFromHTML(host string) (string, bool) {
	for _, scheme := range []string{"https", "http"} {
		html, ok := fetchHTML(scheme + "://" + host + "/")
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
		if dataURL, ok := tryFetchIcon(abs); ok {
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
func fetchHTML(urlStr string) (string, bool) {
	client := &http.Client{Timeout: 2500 * time.Millisecond}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := client.Do(req)
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
	linkRe := regexp.MustCompile(`(?i)<link\b[^>]*>`)
	links := linkRe.FindAllString(html, -1)
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

// attr 从单个 HTML 标签中取出某属性的值（支持带引号与无引号）
func attr(tag, name string) string {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `=["']([^"']*)["']`)
	if m := re.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	re2 := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `=([^\s>]+)`)
	if m := re2.FindStringSubmatch(tag); m != nil {
		return m[1]
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
	if err != nil {
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

// fetchWithTimeout 带 2.5s 超时的 GET 请求，读取最多 512KB
func fetchWithTimeout(urlStr string) ([]byte, string, bool) {
	client := &http.Client{Timeout: 2500 * time.Millisecond}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("User-Agent", "QuickDock/1.0")
	resp, err := client.Do(req)
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
