package env

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxRedirects 单次下载允许跟随的最大跳转层数。
const maxRedirects = 10

// isPrivateHost 判断主机名是否指向回环 / 内网 / 链路本地地址。
// 既认字面 IP，也认 localhost 这类保留名——后者是内网服务最常见的写法。
func isPrivateHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return false
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}
	return false
}

// checkRedirectSafety 在跟随重定向时做三项硬性检查：
//
//  1. 禁止协议降级：https 源不得跳到 http（明文段足以让任何中间人替换二进制）。
//  2. 禁止非 http(s) 协议：file:// / ftp:// 之类不在下载链路的语义里。
//  3. 禁止「公网源被导向内网」：源站在公网却把我们重定向到 127.0.0.1 / 10.x / .local，
//     等于让本地可达的任意服务决定下载内容。
//
// 第 3 条只在起始地址本身是公网时生效：用户完全可能把自定义源指向内网 NAS 或本地
// 镜像（source.go 的自定义源模板），那种情况下跳到内网是正常用法，不能拦。
//
// https → https 的跨域跳转本身放行：GitHub Release、Adoptium API、各类 CDN 都靠它，
// 真正的完整性保障由 verifyDownloadedFile 的官方 sha256 校验负责（见 assetdigest.go）。
func checkRedirectSafety(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("重定向层数过多（>%d），疑似跳转环: %s", maxRedirects, req.URL)
	}
	scheme := strings.ToLower(req.URL.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("拒绝重定向到非 HTTP(S) 地址: %s", req.URL)
	}
	// 上一跳是 https 时不允许掉到 http（首跳本身允许 http：内网源与本地测试都用它）。
	if prev := via[len(via)-1]; prev != nil && prev.URL != nil {
		if strings.EqualFold(prev.URL.Scheme, "https") && scheme != "https" {
			return fmt.Errorf("拒绝把 HTTPS 下载降级为明文 HTTP: %s", req.URL)
		}
	}
	if first := via[0]; first != nil && first.URL != nil {
		startHost := first.URL.Hostname()
		if !isPrivateHost(startHost) && isPrivateHost(req.URL.Hostname()) {
			return fmt.Errorf("拒绝从公网源重定向到内网地址: %s", req.URL.Hostname())
		}
	}
	return nil
}

// sanitizeRedirectURL 把重定向目标规整成可安全携带的 URL（清掉 userinfo，防止
// 凭据被带到第三方 host）。返回空串表示该目标不该跟。
func sanitizeRedirectURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.User = nil
	return cp.String()
}
