package bookmark

import (
	"net/url"
	"sort"
	"strings"
)

// trackingParams 纯跟踪参数，不改变页面内容，归一化时直接丢弃。
//
// 只收有共识的几个：utm_* 全家、广告点击 id（fbclid / gclid / msclkid / yclid）、
// 分享来源（spm 是阿里系，igshid 是 Instagram）。像 from / ref / source 这类
// 在不少站点上是真实功能参数（分页来源、推荐位），删了会把不同页面判成同一条，
// 故一律保留——宁可漏判一次重复，也不要把两条不同内容合并掉。
var trackingParams = []string{
	"fbclid", "gclid", "msclkid", "yclid", "igshid", "spm", "_hsenc", "_hsmi",
}

func isTracking(key string) bool {
	key = strings.ToLower(key)
	if strings.HasPrefix(key, "utm_") {
		return true
	}
	for _, p := range trackingParams {
		if key == p {
			return true
		}
	}
	return false
}

// NormalizeURL 把 URL 归一成「判重用的键」。
//
// 只做不改变指向的改动：协议与主机小写、去默认端口、去 #fragment、去跟踪参数、
// 查询参数排序、去路径尾斜杠。刻意不做的事：不剥 www.（存在 www 与非 www 内容
// 不同的站点）、不跟随跳转、不改大小写路径（很多站点路径大小写敏感）。
//
// 解析失败时返回小写去空白的原文——宁可让它参与判重也不返回空串，
// 否则一批畸形链接会挤在同一个空键上被误判成互相重复。
func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Host = strings.TrimSuffix(u.Host, ":80")
	u.Host = strings.TrimSuffix(u.Host, ":443")
	if u.Scheme == "http" && strings.HasSuffix(u.Host, ":443") {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	u.Fragment = ""
	u.RawFragment = ""

	if u.Path != "" && u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
	}

	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if isTracking(k) {
				q.Del(k)
			}
		}
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			vs := q[k]
			sort.Strings(vs)
			for _, v := range vs {
				if b.Len() > 0 {
					b.WriteByte('&')
				}
				b.WriteString(url.QueryEscape(k))
				b.WriteByte('=')
				b.WriteString(url.QueryEscape(v))
			}
		}
		u.RawQuery = b.String()
	}

	s := u.String()
	// url.String 会把根路径补成 "/"，统一去掉尾部斜杠让 "a.com" 与 "a.com/" 相等
	s = strings.TrimSuffix(s, "/")
	return s
}

// IsWebURL 判断是不是 http/https 链接。书签里混着 javascript:/chrome:// 这类
// 伪协议（部分扩展会写进去），导入时直接跳过，它们在项目里点开也没有意义。
func IsWebURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// HostOf 取主机名用于兜底标题（书签没写标题时用域名，比留空好认）。
func HostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}
