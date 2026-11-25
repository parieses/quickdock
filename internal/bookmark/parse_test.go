package bookmark

import "testing"

// chromeSample 结构照 Chrome 导出真实产物写：书签栏是 H3 但没有外层文件夹名，
// 「其他书签」多一层，ICON 属性是 data URI（解析阶段忽略）。
const chromeSample = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<!-- This is an automatically generated file. -->
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><H3 ADD_DATE="1700000000" LAST_MODIFIED="1700000001" PERSONAL_TOOLBAR_FOLDER="true">书签栏</H3>
    <DL><p>
        <DT><H3 ADD_DATE="1700000002">前端 &amp; 设计</H3>
        <DL><p>
            <DT><A HREF="https://developer.mozilla.org/zh-CN/docs/Web" ADD_DATE="1700000010">MDN &amp; 文档</A>
            <DT><A HREF="https://tailwindcss.com/docs?utm_source=x&amp;v=4" ADD_DATE="1700000011">Tailwind</A>
            <DD>原子化 CSS 文档
            <DT><A HREF="https://example.com/path/" ADD_DATE="1700000012"></A>
        </DL><p>
        <DT><A HREF="https://news.ycombinator.com/">Hacker News</A>
        <DT><H3>空文件夹</H3>
        <DL><p>
        </DL><p>
    </DL><p>
    <DT><H3 PERSONAL_TOOLBAR_FOLDER="false">其他书签</H3>
    <DL><p>
        <DT><A HREF="https://github.com/trending">Trending</A>
        <DT><A HREF="javascript:void(0)">扩展按钮</A>
    </DL><p>
</DL><p>
`

func TestParseChromeExport(t *testing.T) {
	entries, err := Parse(chromeSample)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 「扩展按钮」是 javascript: 伪协议，解析阶段照样保留（过滤在导入层），
	// 所以这里 6 条：MDN / Tailwind / example / HN / Trending / 扩展按钮
	if len(entries) != 6 {
		t.Fatalf("条目数 = %d，期望 6", len(entries))
	}

	var mdn Entry
	for _, e := range entries {
		if e.URL == "https://developer.mozilla.org/zh-CN/docs/Web" {
			mdn = e
		}
	}
	if mdn.Title != "MDN & 文档" {
		t.Errorf("标题实体未解码: %q", mdn.Title)
	}
	// 书签栏剥掉，只留真实文件夹层级
	if len(mdn.Folder) != 1 || mdn.Folder[0] != "前端 & 设计" {
		t.Errorf("文件夹路径 = %v，期望 [前端 & 设计]", mdn.Folder)
	}
	if mdn.AddDate != 1700000010 {
		t.Errorf("AddDate = %d", mdn.AddDate)
	}

	for _, e := range entries {
		if e.Title == "Tailwind" {
			if e.URL != "https://tailwindcss.com/docs?utm_source=x&v=4" {
				t.Errorf("URL 属性实体未解码: %q", e.URL)
			}
			if e.Desc != "原子化 CSS 文档" {
				t.Errorf("DD 描述未取到: %q", e.Desc)
			}
		}
	}

	// 空标题条目要保留（导入层用域名兜底），且 URL 尾斜杠按原文保留
	for _, e := range entries {
		if e.URL == "https://example.com/path/" {
			if e.Title != "" {
				t.Errorf("空标题被改写了: %q", e.Title)
			}
		}
	}

	// 根上的 H3「其他书签」不是书签栏，保留为文件夹
	for _, e := range entries {
		if e.URL == "https://github.com/trending" {
			if len(e.Folder) != 1 || e.Folder[0] != "其他书签" {
				t.Errorf("其他书签层级 = %v", e.Folder)
			}
		}
	}
}

func TestParseRejectsNonBookmark(t *testing.T) {
	if _, err := Parse("<html><body><p>hello</p></body></html>"); err == nil {
		t.Fatal("普通 HTML 应当报错")
	}
	if _, err := Parse(""); err == nil {
		t.Fatal("空内容应当报错")
	}
}

// 浏览器之间差异最大的地方是闭合标签，残缺时要能继续跑完而不是整份失败。
func TestParseToleratesBrokenMarkup(t *testing.T) {
	broken := `<DL><p>
<DT><A HREF="https://a.test/1">A1
<DT><A HREF="https://a.test/2">A2</A>
<DT><A HREF='https://a.test/3' ADD_DATE="1">A3</A>
`
	entries, err := Parse(broken)
	if err != nil {
		t.Fatalf("残缺标记不该失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(entries))
	}
	if entries[0].Title != "A1" || entries[0].URL != "https://a.test/1" {
		t.Errorf("未闭合 A 解析错: %+v", entries[0])
	}
	if entries[2].Title != "A3" || entries[2].URL != "https://a.test/3" {
		t.Errorf("单引号属性解析错: %+v", entries[2])
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Example.COM/path/", "https://example.com/path"},
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com"},
		{"https://example.com/a?utm_source=x&utm_medium=y", "https://example.com/a"},
		{"https://example.com/a?b=2&a=1", "https://example.com/a?a=1&b=2"},
		{"https://example.com/a#frag", "https://example.com/a"},
		{"https://example.com:443/a", "https://example.com/a"},
		// from 是真实功能参数，必须保留
		{"https://example.com/a?from=mail", "https://example.com/a?from=mail"},
	}
	for _, c := range cases {
		if got := NormalizeURL(c.in); got != c.want {
			t.Errorf("NormalizeURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	// 归一化后相等的两条必须真的相等（判重的前提）
	a := NormalizeURL("https://Example.com/x/?utm_source=a#top")
	b := NormalizeURL("https://example.com/x")
	if a != b {
		t.Fatalf("判重键不一致: %q vs %q", a, b)
	}
}

func TestIsWebURL(t *testing.T) {
	yes := []string{"https://a.com", "http://a.com/x", "https://a.com:8443/x"}
	no := []string{"javascript:void(0)", "chrome://bookmarks", "file:///c:/x", "", "not a url"}
	for _, s := range yes {
		if !IsWebURL(s) {
			t.Errorf("%q 应判为网页", s)
		}
	}
	for _, s := range no {
		if IsWebURL(s) {
			t.Errorf("%q 不该判为网页", s)
		}
	}
}
