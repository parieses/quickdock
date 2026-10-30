package aitools

// 网页工具：抓取一个 URL 并返回可读正文。
//
// 为什么是 agent 工具而不是 MCP 工具：它不读写宿主任何数据，只往外出一次
// HTTP 请求，能力上属于「一个 code agent 的标配」，和 QuickDock 能配置什么无关。
// 也因此它在内置 AI 的所有模式下都可用——抓文档、看报错页不需要先切换到教程模式。
//
// 不做「搜索引擎」：搜索要 API Key、要计费、要处理结果排序，而且模型自己
// 知道该去哪个文档站。给它一个「能打开网页」的能力，比给它一个内置搜索框有用。

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	mcpsrv "quickdock/internal/mcp"
)

const (
	// webFetchTimeout 单次抓取的总超时。
	// 15s 是「用户还愿意等」与「慢站也可能通」的折中：卡住的站点通常永远不会回，
	// 再等下去只是让 AI 那轮对话白挂。
	webFetchTimeout = 15 * time.Second

	// webFetchMaxBytes 响应体读取上限。超过就停止读取并按截断处理——
	// 有些下载链接会返回几百 MB 的二进制，全读进内存等于把应用拖死。
	webFetchMaxBytes = 4 << 20 // 4MB

	// webFetchMaxChars 一次返回给模型的字符上限。
	// 与 aiToolResultMaxRune（12000）不是一个层级：这个更宽，因为网页正文的
	// 信息密度低，模型常常要通读才能找到那一段；真正超长的会在 ai_tools 那层再截。
	webFetchMaxChars = 30000

	// webFetchDefaultChars 不传 maxChars 时的默认值。
	webFetchDefaultChars = 8000

	// webFetchMaxRedirect 最多跟几次重定向。Go 默认 10 次，对「抓一个页面」来说
	// 已经超过正常站点该有的跳转数，而且每多一跳就多一次网络往返。
	webFetchMaxRedirect = 5

	// webLinksDefault / webLinksMax 链接条数。默认值看着不多，但一个导航站首页
	// 动辄几百条链接，全塞给模型只会把上下文占满，它实际只会顺着前十几条走。
	webLinksDefault = 80
	webLinksMax     = 300
)

// webFetchUA 伪装成普通浏览器。
//
// 不是想骗人：大量站点（知乎、Medium、各种文档站）对非浏览器 UA 直接 403 或返回
// 精简页。末尾带 QuickDock 标识，服务端日志里仍能看出这是谁。
const webFetchUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 QuickDock-AI"

func registerWeb(_ Deps) {
	r := registrar{category: mcpsrv.CategoryWeb}
	str := mcpsrv.Str
	intp := mcpsrv.Int
	boolp := mcpsrv.Bool

	// 三个工具都要「取 url → 补全 → 校验」，抽一处避免某天只改了其中一个。
	webTarget := func(args map[string]any) (string, error) {
		raw := strings.TrimSpace(mcpsrv.Arg(args, "url"))
		if raw == "" {
			return "", errors.New("缺少参数 url（要抓取的网页地址，需带 http:// 或 https://）")
		}
		return normalizeWebURL(raw)
	}

	r.add(func(_ Deps, args map[string]any) (any, error) {
		target, err := webTarget(args)
		if err != nil {
			return nil, err
		}
		maxChars := mcpsrv.ArgInt(args, "maxChars", webFetchDefaultChars)
		if maxChars <= 0 {
			maxChars = webFetchDefaultChars
		}
		if maxChars > webFetchMaxChars {
			maxChars = webFetchMaxChars
		}
		return fetchWebPage(target, mcpsrv.ArgInt(args, "offset", 0), maxChars)
	}, mcpsrv.LevelRead, "web_fetch",
		"抓取一个网页并返回可读正文（标题 + 去掉脚本/导航后的文字）。可抓公网文档、报错页、博客，"+
			"也可抓本机开发服务（http://127.0.0.1:端口）。只支持 http/https。"+
			"内容超长会截断，此时返回 nextOffset：**续读请把它传回 offset**，不要调大 maxChars 重抓整页（那会重复下载一遍）",
		[]string{"url"},
		map[string]any{
			"url":      str("完整网址，必须带 http:// 或 https://"),
			"maxChars": intp("本次最多返回多少字符，默认 8000，上限 30000"),
			"offset":   intp("从第几个字符开始返回，默认 0。续读时传上一次返回的 nextOffset"),
		})

	r.add(func(_ Deps, args map[string]any) (any, error) {
		target, err := webTarget(args)
		if err != nil {
			return nil, err
		}
		limit := mcpsrv.ArgInt(args, "maxLinks", webLinksDefault)
		if limit <= 0 {
			limit = webLinksDefault
		}
		if limit > webLinksMax {
			limit = webLinksMax
		}
		return webLinks(target, limit, mcpsrv.ArgBool(args, "sameHost"))
	}, mcpsrv.LevelRead, "web_links",
		"抽出一个网页里的链接列表（锚文本 + 绝对 URL），用来找目录、翻页、看某页引用了哪些资源。"+
			"web_fetch 返回的是纯文字正文，链接会被去掉，所以需要导航时必须用这个工具",
		[]string{"url"},
		map[string]any{
			"url":      str("完整网址，必须带 http:// 或 https://"),
			"maxLinks": intp("最多返回多少条，默认 80，上限 300"),
			"sameHost": boolp("可选。只保留与页面同域名的链接（找站内目录时有用），默认 false"),
		})

	r.add(func(_ Deps, args map[string]any) (any, error) {
		target, err := webTarget(args)
		if err != nil {
			return nil, err
		}
		return webHead(target)
	}, mcpsrv.LevelRead, "web_head",
		"只取一个网址的响应头，不下载正文：状态码、全部响应头（跳过 Set-Cookie）、重定向后的最终地址，"+
			"HTTPS 站点还会返回证书的颁发者、到期时间与剩余天数。用来判断链接是否失效、看服务返回什么头、查证书快过期了没",
		[]string{"url"},
		map[string]any{
			"url": str("完整网址，必须带 http:// 或 https://"),
		})
}

// normalizeWebURL 补全并校验目标地址。
//
// 补 http://：模型经常只给 "example.com/docs"，缺 scheme 时 http.NewRequest 会
// 直接报错，而错误信息（"unsupported protocol scheme"）对模型几乎没有指导意义。
//
// 只放行 http/https：file:// 能读本机任意文件、ftp:// 走的是另一套协议栈，
// 放进来等于给这个只读工具开了一个「读任意文件」的后门。
func normalizeWebURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("url 不能为空")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("网址无法解析：%v", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("只支持 http/https，收到的是 %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("网址缺少主机名")
	}
	return u.String(), nil
}

// checkFetchHost 拒绝明显不该被 AI 打开的地址。
//
// 只挡两类，不搞「私网全禁」：
//   - 非单播/未指定地址（0.0.0.0、::、组播）：连上去也没有意义，只会白等超时；
//   - 链路本地 169.254.0.0/16（含云主机的 169.254.169.254 元数据端点）：
//     那是凭据泄露的经典入口，而正常用户不会让 AI 去抓这个网段。
//
// 127.0.0.1 与私网刻意放行：本工具的一半用途就是看本机 dev server 返回什么，
// 而内置 AI 本来就有读文件、跑插件的能力，抓一个本地 HTTP 服务并不扩大权限面。
func checkFetchHost(host string) error {
	h := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if h == "" {
		return errors.New("网址缺少主机名")
	}
	ips, err := net.LookupIP(h)
	if err != nil {
		// 解析不了就交给 HTTP 客户端去报——这里报「DNS 失败」会掩盖真正的错误
		// （比如域名拼错 vs 网络不通，客户端给的提示更准）。
		return nil
	}
	for _, ip := range ips {
		if ip.IsUnspecified() || ip.IsMulticast() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("地址 %s 属于本机链路本地/保留网段，不抓取", ip.String())
		}
	}
	return nil
}

// webDoc 一次抓取的原始结果。
//
// 抽出来是因为 web_fetch / web_links / web_head 是同一套「发请求、判重定向、限长读」
// 逻辑的三种消费方式：正文抽取、链接抽取、只看头。复制三份的话，重定向校验、
// SSRF 白名单、超时这些安全相关的东西迟早会漏掉其中一份。
type webDoc struct {
	Target    string
	FinalURL  string
	Status    int
	CType     string
	MediaType string
	Header    http.Header
	TLS       *tls.ConnectionState
	Body      []byte
	Overflow  bool
	Elapsed   time.Duration
}

// newWebClient 统一的抓取客户端。
//
// 重定向目标必须重新过 checkFetchHost：否则被挡住的 169.254.169.254 只要
// 由正常站点跳一次就绕过去了，白名单形同虚设。
func newWebClient() *http.Client {
	return &http.Client{
		Timeout: webFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webFetchMaxRedirect {
				return fmt.Errorf("重定向超过 %d 次，已停止", webFetchMaxRedirect)
			}
			return checkFetchHost(req.URL.Hostname())
		},
	}
}

// fetchWebDoc 发一次请求并把响应读进内存（有上限）。
// method 传 HEAD 时不会读 body，只为了拿头与证书。
func fetchWebDoc(target, method string) (*webDoc, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if err := checkFetchHost(parsed.Hostname()); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", webFetchUA)
	if method == http.MethodHead {
		req.Header.Set("Accept", "*/*")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")
	}
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	started := time.Now()
	resp, err := newWebClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败：%v", err)
	}
	defer resp.Body.Close()

	// 限长读取：多读 1 个字节用来判断「是不是还有」。
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
	overflow := int64(len(body)) > webFetchMaxBytes
	if overflow {
		body = body[:webFetchMaxBytes]
	}
	if rerr != nil && len(body) == 0 && method != http.MethodHead {
		return nil, fmt.Errorf("读取响应失败：%v", rerr)
	}

	ctype := resp.Header.Get("Content-Type")
	return &webDoc{
		Target:    target,
		FinalURL:  resp.Request.URL.String(),
		Status:    resp.StatusCode,
		CType:     ctype,
		MediaType: strings.ToLower(strings.TrimSpace(strings.Split(ctype, ";")[0])),
		Header:    resp.Header,
		TLS:       resp.TLS,
		Body:      body,
		Overflow:  overflow,
		Elapsed:   time.Since(started),
	}, nil
}

// docHTML 把响应体按 HTML 解出 DOM，顺带做编码转换。
//
// 编码转换不能省：不少中文站点仍是 GBK/GB18030，直接按 UTF-8 读会得到一片乱码，
// 而模型会一本正经地对着乱码总结。charset 包按响应头与 <meta> 猜。
func docHTML(d *webDoc) (*html.Node, error) {
	r, cerr := charset.NewReader(bytes.NewReader(d.Body), d.CType)
	if cerr != nil {
		r = bytes.NewReader(d.Body)
	}
	return html.Parse(r)
}

// fetchWebPage 抓取并按内容类型决定怎么转成文本。
//
// offset 是字符偏移（不是行、不是字节）：截断后要续读，模型只能拿到上一次
// 返回的字数，按字符续最直观。传 0 就是从开头。
func fetchWebPage(target string, offset, maxChars int) (any, error) {
	d, err := fetchWebDoc(target, http.MethodGet)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"url":         d.Target,
		"finalUrl":    d.FinalURL,
		"status":      d.Status,
		"contentType": d.CType,
		"elapsedMs":   d.Elapsed.Milliseconds(),
	}
	if d.Overflow {
		out["bodyTruncated"] = true
	}

	switch {
	case d.MediaType == "" || strings.Contains(d.MediaType, "html") || strings.Contains(d.MediaType, "xml"):
		root, perr := docHTML(d)
		if perr != nil {
			// 解析失败别丢原文：有些站点返回的是「HTML 外壳里包一段 JSON」，
			// 里面往往就有模型要的那段数据。
			out["title"] = ""
			out["text"], out["chars"], out["truncated"], out["nextOffset"] =
				clipWebTextAt(string(d.Body), offset, maxChars)
			return out, nil
		}
		if offset == 0 {
			out["title"] = webPageTitle(root)
		}
		text := webPageText(root)
		out["text"], out["chars"], out["truncated"], out["nextOffset"] =
			clipWebTextAt(text, offset, maxChars)

	case strings.HasPrefix(d.MediaType, "text/"), strings.Contains(d.MediaType, "json"),
		strings.Contains(d.MediaType, "javascript"):
		out["text"], out["chars"], out["truncated"], out["nextOffset"] =
			clipWebTextAt(string(d.Body), offset, maxChars)

	default:
		// 图片/PDF/压缩包：说清楚是什么，别让模型以为「网页打不开」。
		return nil, fmt.Errorf("返回的是 %s 二进制内容（%d 字节），web_fetch 只能读文本/HTML。"+
			"如果这是下载链接，直接给用户链接让他在浏览器里打开", mediaTypeOrUnknown(d.MediaType), len(d.Body))
	}
	return out, nil
}

// webLinks 抽出页面里的链接与锚文本。
//
// 存在的理由：正文字段把 <a> 压成了纯文字，模型拿到的段落里没有任何 URL——
// 于是「顺着目录找子页面」「翻页」这类操作在 web_fetch 上根本无法完成。
func webLinks(target string, maxLinks int, sameHost bool) (any, error) {
	d, err := fetchWebDoc(target, http.MethodGet)
	if err != nil {
		return nil, err
	}
	root, err := docHTML(d)
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 失败：%v", err)
	}
	base, berr := url.Parse(d.FinalURL)
	if berr != nil {
		base, _ = url.Parse(target)
	}

	links := make([]map[string]any, 0, maxLinks)
	seen := map[string]bool{}
	var truncated bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if truncated {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "a") {
			if href := strings.TrimSpace(attrOf(n, "href")); href != "" {
				if abs := resolveLink(base, href); abs != "" {
					keep := !sameHost || strings.EqualFold(hostOf(abs), hostOf(d.FinalURL))
					if keep && !seen[abs] {
						seen[abs] = true
						if len(links) >= maxLinks {
							truncated = true
							return
						}
						links = append(links, map[string]any{
							"text": mcpsrv.TruncateRunes(nodeText(n), 120),
							"url":  abs,
						})
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)

	return map[string]any{
		"url":         d.Target,
		"finalUrl":    d.FinalURL,
		"status":      d.Status,
		"title":       webPageTitle(root),
		"count":       len(links),
		"links":       links,
		"truncated":   truncated,
		"maxLinksTip": "条数或单条有上限，命中上限时 truncated=true；要用更多链接请调大 maxLinks",
	}, nil
}

// webHead 只取响应头，不读正文。
//
// 用途：判断一个链接是不是死了（状态码/重定向链）、看服务返回了什么头、
// 查 HTTPS 证书还剩几天。这些信息 web_fetch 拿不到——它返回的是抽过的正文。
func webHead(target string) (any, error) {
	d, err := fetchWebDoc(target, http.MethodHead)
	if err != nil {
		return nil, err
	}
	// 有些站点不支持 HEAD（405/501），此时状态码本身没有意义，直接说明。
	out := map[string]any{
		"url":       d.Target,
		"finalUrl":  d.FinalURL,
		"status":    d.Status,
		"elapsedMs": d.Elapsed.Milliseconds(),
		"headers":   flattenHeaders(d.Header),
	}
	if d.Status == http.StatusMethodNotAllowed || d.Status == http.StatusNotImplemented {
		out["note"] = "该站点不接受 HEAD 请求，状态码仅供参考；要拿正文请用 web_fetch"
	}
	if d.TLS != nil && len(d.TLS.PeerCertificates) > 0 {
		c := d.TLS.PeerCertificates[0]
		out["tls"] = map[string]any{
			"subject":     c.Subject.CommonName,
			"issuer":      c.Issuer.CommonName,
			"dnsNames":    c.DNSNames,
			"notBefore":   c.NotBefore.Format(time.RFC3339),
			"notAfter":    c.NotAfter.Format(time.RFC3339),
			"daysLeft":    int(time.Until(c.NotAfter).Hours() / 24),
			"negotiated":  tlsVersionName(d.TLS.Version),
			"verifiedTLS": len(d.TLS.VerifiedChains) > 0,
		}
	}
	return out, nil
}

// attrOf 取元素属性值（大小写不敏感，HTML 属性名本身就是大小写不敏感的）。
func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// resolveLink 把 href 绝对化；明显不是网页的链接返回空串。
//
// 只放行 http/https：javascript: 是脚本、mailto:/tel: 是本地应用、
// data: 是内联数据——这些给模型没有任何用处，混在结果里只会挤掉真链接。
func resolveLink(base *url.URL, href string) string {
	low := strings.ToLower(href)
	for _, prefix := range []string{"#", "javascript:", "mailto:", "tel:", "data:", "blob:"} {
		if strings.HasPrefix(low, prefix) {
			return ""
		}
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(ref)
	if s := strings.ToLower(abs.Scheme); s != "http" && s != "https" {
		return ""
	}
	abs.Fragment = "" // 同一页的锚点不该算成一条独立链接
	return abs.String()
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// flattenHeaders 把 http.Header 摊平成 map[string]string。
// 跳过 Set-Cookie：那是会话凭据，交给模型既没用也不安全。
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

func mediaTypeOrUnknown(t string) string {
	if strings.TrimSpace(t) == "" {
		return "未声明类型"
	}
	return t
}

// webPageTitle 取 <title>，没有就退回第一个 <h1>。
func webPageTitle(root *html.Node) string {
	var title, h1 string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if title == "" && n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "h1":
				if h1 == "" {
					h1 = strings.TrimSpace(nodeText(n))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if title != "" {
		return title
	}
	return h1
}

// webPageText 把 DOM 抽成「人读」的纯文本。
//
// 不追求 Markdown 保真：表格、代码块的结构化还原收益有限，而模型真正需要的是
// 段落顺序与文字内容。跳过 script/style/nav 是因为它们占体积最大、信息量最低——
// 一个现代页面的正文常常只占下载体积的 5%。
func webPageText(root *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			name := strings.ToLower(n.Data)
			if webSkipTags[name] {
				return
			}
			if webBlockTags[name] {
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t)
				b.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return collapseBlankLines(b.String())
}

var webSkipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true, "canvas": true,
	"iframe": true, "template": true, "head": true, "nav": true, "footer": true,
	"form": true, "select": true, "button": true,
}

var webBlockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "section": true,
	"article": true, "header": true, "blockquote": true, "pre": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"dd": true, "dt": true, "figcaption": true, "hr": true,
}

// nodeText 取一个节点的全部文字（不含子块换行），给标题用。
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// collapseBlankLines 压掉连续空行与行首尾空白。
// 不去掉的话，源码里每个标签之间的换行都会留下一行空白，正文会被空行淹没。
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// clipWebTextAt 从第 offset 个字符起截取 maxChars 个字符，并返回
// 「全文总字数 / 是否还有剩余 / 下次该传的 offset」。
//
// offset 是字符偏移，与返回的 chars（全文总字数）同一把尺子：
// 模型只需要把上一次拿到的数字相加就能续读，不用理解字节与编码。
//
// 截断说明写进正文而不是只放字段里：模型看的是正文，字段它经常忽略。
func clipWebTextAt(s string, offset, maxChars int) (text string, total int, truncated bool, nextOffset int) {
	s = strings.TrimSpace(s)
	rune := []rune(s)
	total = len(rune)
	if maxChars <= 0 || total <= maxChars {
		maxChars = webFetchMaxChars
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return fmt.Sprintf("[offset=%d 已超出全文长度 %d，没有更多内容了]", offset, total), total, false, total
	}
	end := offset + maxChars
	if end >= total {
		return string(rune[offset:]), total, false, total
	}
	body := string(rune[offset:end])
	return body + fmt.Sprintf("\n\n[已截断：全文 %d 字，这里是第 %d–%d 字。续读请传 offset=%d。]",
		total, offset+1, end, end), total, true, end
}
