package aitools

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// web_fetch 的核心价值是「把 HTML 变成人读的正文」。
// 这里钉住三件容易退化的事：标题能取到、导航/脚本不被当成正文、段落不粘连。
func TestFetchWebPageExtractsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>测试标题</title><style>.a{color:red}</style></head>
			<body><nav>站点菜单</nav>
			<h1>小节标题</h1>
			<p>第一段正文</p>
			<script>var secret = 1</script>
			<p>第二段正文</p>
			<footer>版权信息</footer></body></html>`)
	}))
	defer srv.Close()

	got, err := fetchWebPage(srv.URL, 0, 8000)
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("返回类型不是 map: %T", got)
	}
	if m["title"] != "测试标题" {
		t.Fatalf("标题错误: %v", m["title"])
	}
	text, _ := m["text"].(string)
	for _, bad := range []string{"站点菜单", "var secret", "color:red", "版权信息"} {
		if strings.Contains(text, bad) {
			t.Fatalf("正文里不该出现 %q：\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "第一段正文") || !strings.Contains(text, "第二段正文") {
		t.Fatalf("正文缺段落:\n%s", text)
	}
}

// 二进制要明确报错而不是返回一堆乱码——模型看到乱码会照着编。
func TestFetchWebPageRejectsBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()

	if _, err := fetchWebPage(srv.URL, 0, 8000); err == nil {
		t.Fatal("二进制内容应返回错误")
	} else if !strings.Contains(err.Error(), "image/png") {
		t.Fatalf("错误信息应说明实际类型: %v", err)
	}
}

// 截断必须写进正文：模型读的是 content，单独的 truncated 字段它经常忽略。
func TestClipWebText(t *testing.T) {
	text, total, truncated, _ := clipWebTextAt("一二三四五", 0, 3)
	if !truncated || total != 5 {
		t.Fatalf("截断标记错误: truncated=%v total=%d", truncated, total)
	}
	if !strings.Contains(text, "一二三") || !strings.Contains(text, "已截断") {
		t.Fatalf("截断结果不对: %q", text)
	}
	// 刚好等长不该被当成截断
	if _, _, tr, _ := clipWebTextAt("abc", 0, 3); tr {
		t.Fatal("等长不该截断")
	}
}

// 只放行 http/https：file:// 能读本机任意文件，等于给这个只读工具开后门。
func TestNormalizeWebURL(t *testing.T) {
	ok := map[string]string{
		"example.com/docs": "http://example.com/docs",
		"https://a.b/c":    "https://a.b/c",
	}
	for in, want := range ok {
		got, err := normalizeWebURL(in)
		if err != nil {
			t.Fatalf("%s 应通过: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s 归一化错误: got %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "file:///c:/windows/win.ini", "ftp://x.com/a", "javascript:alert(1)"} {
		if _, err := normalizeWebURL(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}

// 链路本地地址（云元数据端点）必须挡住；本机 dev server 要放行。
func TestCheckFetchHost(t *testing.T) {
	if err := checkFetchHost("169.254.169.254"); err == nil {
		t.Fatal("链路本地地址应被拒绝")
	}
	if err := checkFetchHost("127.0.0.1"); err != nil {
		t.Fatalf("本机地址应放行（看 dev server 是主要用途之一）: %v", err)
	}
	// 解析不了的域名交给 HTTP 客户端报错，这里不该提前拦截
	if err := checkFetchHost("this-domain-should-not-exist.invalid"); err != nil {
		t.Fatalf("DNS 失败应放行给客户端处理: %v", err)
	}
}

// web_links：正文字段会把 <a> 压成纯文字，没有这个工具模型无法「顺着目录找子页面」。
// 这里钉住三件事：相对地址要绝对化、非网页协议要丢掉、同一地址只出现一次。
func TestWebLinksExtractsAbsoluteURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><body>
			<a href="/docs/intro">文档首页</a>
			<a href="guide.html">指南</a>
			<a href="javascript:void(0)">无效</a>
			<a href="mailto:a@b.c">邮件</a>
			<a href="#top">锚点</a>
			<a href="/docs/intro">重复链接</a>
		</body></html>`)
	}))
	defer srv.Close()

	res, err := webLinks(srv.URL+"/", 50, false)
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	m := res.(map[string]any)
	links, _ := m["links"].([]map[string]any)
	if len(links) != 2 {
		t.Fatalf("应只剩两条有效链接，实际 %d 条: %v", len(links), links)
	}
	if links[0]["url"] != srv.URL+"/docs/intro" {
		t.Fatalf("相对地址没绝对化: %v", links[0]["url"])
	}
	if links[1]["url"] != srv.URL+"/guide.html" {
		t.Fatalf("相对地址没绝对化: %v", links[1]["url"])
	}
}

// offset 续读：截断后返回的 nextOffset 必须能接着上次的位置，
// 否则模型只能调大 maxChars 把整页重新下载一遍。
func TestClipWebTextAtResumes(t *testing.T) {
	full := "一二三四五六七八九十"
	head, total, truncated, next := clipWebTextAt(full, 0, 4)
	if !truncated || total != 10 || next != 4 {
		t.Fatalf("第一次截断参数错误: truncated=%v total=%d next=%d", truncated, total, next)
	}
	if !strings.HasPrefix(head, "一二三四") {
		t.Fatalf("第一次内容不对: %q", head)
	}

	tail, _, truncated2, next2 := clipWebTextAt(full, next, 4)
	if !truncated2 || next2 != 8 {
		t.Fatalf("续读参数错误: truncated=%v next=%d", truncated2, next2)
	}
	if !strings.HasPrefix(tail, "五六七八") {
		t.Fatalf("续读内容不对: %q", tail)
	}

	// 越界不该报错，要明确说「没有更多了」——模型据此结束，而不是再试一次
	end, _, over, _ := clipWebTextAt(full, 99, 4)
	if over || !strings.Contains(end, "没有更多") {
		t.Fatalf("越界应说明没有更多内容: %q", end)
	}
}

// web_head 只取头，不该把正文读进来；证书信息在 https 站点才有，http 站点不能报错。
func TestWebHeadSkipsCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "1")
		w.Header().Set("Set-Cookie", "session=secret")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := webHead(srv.URL)
	if err != nil {
		t.Fatalf("取头失败: %v", err)
	}
	m := res.(map[string]any)
	headers, _ := m["headers"].(map[string]string)
	if headers["X-Test"] != "1" {
		t.Fatalf("普通响应头应保留: %v", headers)
	}
	for k := range headers {
		if strings.EqualFold(k, "Set-Cookie") {
			t.Fatal("Set-Cookie 是会话凭据，不该交给模型")
		}
	}
	if _, ok := m["tls"]; ok {
		t.Fatal("http 站点不该有 tls 字段")
	}
}
