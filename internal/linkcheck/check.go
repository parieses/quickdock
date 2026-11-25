// Package linkcheck 书签 / 网页条目的存活探测。
//
// 只做一件事：给一个 URL，回一个可持久化的状态码。判定口径刻意保守——
// 只有明确的「服务端说没有」才叫死链，反爬与限流单独成一类，否则整库收藏
// 会被误报成一片红（大量站点对程序请求返回 403）。
package linkcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Status 探测结果分类。前端按这几种上色，不再自己判 HTTP 码。
type Status string

const (
	StatusOK      Status = "ok"      // 2xx
	StatusMoved   Status = "moved"   // 发生了跨域/跨路径跳转，原始地址实际已失效
	StatusBroken  Status = "broken"  // 404 / 410：确实是死链
	StatusBlocked Status = "blocked" // 401 / 403 / 429 / 451：反爬或需登录
	StatusError   Status = "error"   // 5xx、DNS 失败、连接被拒
	StatusTimeout Status = "timeout" // 超时
)

// Result 一次探测的结果，字段与 item_health 表列一一对应。
type Result struct {
	Status      Status
	Code        int
	FinalURL    string
	Err         string
	ContentHash string
}

// timeout 单次探测上限。10 秒是「慢站点也能救回来」与「上千条不至于跑一小时」的折中。
const timeout = 10 * time.Second

// hashLimit 只摘要正文前 256 KB。
//
// 判断「页面内容变没变」不需要全文：头部包含标题、导航与主体开头，改版必然命中。
// 全量读会为了几百条收藏拉出几百 MB 流量。
const hashLimit = 256 << 10

// userAgent 用真实浏览器 UA。
//
// 用 Go 默认 UA 会被大量站点直接 403（Cloudflare、各类 WAF），
// 结果就是把好链接全标成「反爬」。这里声明自己是普通浏览器，不做任何隐藏。
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Check 探测单个 URL。deep 为真时顺带算正文摘要（供「内容已变化」判断）。
//
// 只发 GET 不发 HEAD：不少站点（含 GitHub 部分路径）对 HEAD 回 405/403，
// 会凭空造出一批「死链」。GET 拿到的状态码才是用户点开链接时看到的结果。
func Check(ctx context.Context, rawURL string, deep bool) Result {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Result{Status: StatusError, Err: "不是有效的 http/https 地址"}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{Status: StatusError, Err: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := httpClient.Do(req)
	if err != nil {
		return classifyErr(ctx, err)
	}
	defer resp.Body.Close()

	res := Result{Code: resp.StatusCode, FinalURL: resp.Request.URL.String()}

	if deep {
		h := sha256.New()
		if _, e := io.Copy(h, io.LimitReader(resp.Body, hashLimit)); e == nil {
			res.ContentHash = hex.EncodeToString(h.Sum(nil))
		}
	} else {
		// 不读正文也要把连接收干净，否则连接池里全是半截连接
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	}

	res.Status = classifyCode(resp.StatusCode)
	if res.Status == StatusOK && isMoved(u, resp.Request.URL) {
		res.Status = StatusMoved
	}
	return res
}

// httpClient 复用连接、限制总重定向次数。
//
// 不设 Transport 超时，超时统一由 context 控制（同一份配置同时管 DNS、连接、
// 读取三段），避免两个超时互相遮盖导致「到底卡在哪一段」说不清。
var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return http.ErrUseLastResponse
		}
		return nil
	},
}

func classifyCode(code int) Status {
	switch {
	case code >= 200 && code < 300:
		return StatusOK
	case code == http.StatusUnauthorized, code == http.StatusForbidden,
		code == http.StatusTooManyRequests, code == http.StatusUnavailableForLegalReasons:
		return StatusBlocked
	case code == http.StatusNotFound, code == http.StatusGone:
		return StatusBroken
	case code >= 400 && code < 500:
		return StatusError
	case code >= 500:
		return StatusError
	default:
		// 3xx 且未跟到终点（重定向次数超限或 Location 缺失）
		return StatusError
	}
}

func classifyErr(ctx context.Context, err error) Result {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: StatusTimeout, Err: "请求超时"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return Result{Status: StatusTimeout, Err: "连接超时"}
	}
	msg := err.Error()
	// 只保留可读的那一段：Go 的 url.Error 会把完整 URL 和操作名都拼进去，
	// 存库后前端列表里一行能占到两屏。
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	return Result{Status: StatusError, Err: msg}
}

// isMoved 判断是否发生了「有意义」的跳转。
//
// 仅协议升级 / 补 www / 补尾斜杠（http→https、a.com→www.a.com、a.com→a.com/）
// 不算——那是站点常规做法，收藏依旧有效。只有主机或路径真的变了才提示用户更新地址。
func isMoved(orig, final *url.URL) bool {
	if orig == nil || final == nil {
		return false
	}
	a := strings.TrimPrefix(strings.TrimPrefix(orig.Host, "www."), "m.")
	b := strings.TrimPrefix(strings.TrimPrefix(final.Host, "www."), "m.")
	if a != b {
		return true
	}
	return strings.TrimSuffix(orig.Path, "/") != strings.TrimSuffix(final.Path, "/")
}

// Label 状态的中文短标签。放引擎侧是因为前端 i18n 需要 key 而不是中文，
// 这里只用于日志与导出。
func (s Status) Label() string {
	switch s {
	case StatusOK:
		return "正常"
	case StatusMoved:
		return "地址已变"
	case StatusBroken:
		return "死链"
	case StatusBlocked:
		return "无法访问"
	case StatusTimeout:
		return "超时"
	default:
		return "异常"
	}
}

func (r Result) String() string {
	return fmt.Sprintf("%s(%d)", r.Status.Label(), r.Code)
}
