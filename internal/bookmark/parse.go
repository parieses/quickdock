// Package bookmark 解析浏览器导出的书签文件（Netscape Bookmark File Format）。
//
// 主流浏览器导出的都是同一套格式，只在细节上各有取舍：
//
//	Chrome / Edge  bookmarks_11_25_26.html，带 ICON 属性，标题常有空串
//	Firefox        bookmarks.html，带 <DD> 描述，空文件夹较常见
//	Safari         Bookmarks.html，文件夹层级同样用 <DL> 嵌套
//
// 只用标准库：格式本身极简（DT / H3 / A / DL 四个标签），引 golang.org/x/net/html
// 换来的是完整 DOM，而这里要的只是「按顺序取条目 + 当前文件夹路径」，手写扫描反而更短，
// 也不必为一次导入新增依赖。实体解码用标准库 html.UnescapeString。
package bookmark

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// Entry 一条书签。
//
// Folder 是从根到所在文件夹的路径，空表示直接挂在根上（书签栏 / 其他书签这类
// 浏览器虚拟节点会被剥掉，它们对用户没有分类意义）。Desc 来自 <DD>，Chrome 不写、
// Firefox 常写，落库后对应项目的「备注」。
type Entry struct {
	Title   string
	URL     string
	Desc    string
	Folder  []string
	AddDate int64
}

// maxParseBytes 解析上限。真实导出文件在几十 KB 到几 MB（上万条），
// 上限挡的是误选了大文件（比如整个 .html 网页），而不是正常书签。
const maxParseBytes = 64 << 20

// Parse 解析书签文件内容。返回的条目保持文件里的原始顺序。
//
// 容错优先：残缺标签、未闭合的 A、缺失的 </DL> 都不报错——浏览器之间的导出实现
// 差异比规范大，报错只会让用户拿着一份「explorer 看着完全正常」的文件无从下手。
// 只有「一个 A 都解析不出来」才返回错误，那确实是选错文件了。
func Parse(content string) ([]Entry, error) {
	if len(content) > maxParseBytes {
		return nil, fmt.Errorf("文件过大（超过 %d MB），不像是浏览器书签导出", maxParseBytes>>20)
	}
	if !looksLikeBookmarkFile(content) {
		return nil, fmt.Errorf("不是浏览器书签文件（缺少 HREF 链接，或不是 NETSCAPE-Bookmark 格式）")
	}

	var (
		entries []Entry
		folders []string // 文件夹栈；根用一个空串占位，保证 pop 配平
		pending string   // 刚解析到的 H3 标题，等下一个 <DL> 开栈
	)

	i := 0
	for i < len(content) {
		lt := strings.IndexByte(content[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(content[i:], "<!--") {
			end := strings.Index(content[i:], "-->")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}

		end := strings.IndexByte(content[i:], '>')
		if end < 0 {
			break
		}
		raw := content[i+1 : i+end] // 不含尖括号
		i += end + 1

		name, closing, selfClosing := splitTag(raw)

		switch name {
		case "dl":
			if closing {
				if len(folders) > 0 {
					folders = folders[:len(folders)-1]
				}
				pending = ""
				continue
			}
			// 开栈时把 H3 标题压进去；没有就是根（空串占位）
			folders = append(folders, sanitizeFolder(pending))
			pending = ""
			_ = selfClosing

		case "h3":
			if closing || selfClosing {
				continue
			}
			text, next, ok := readText(content, i)
			if ok {
				i = next
			}
			pending = strings.TrimSpace(text)
			// 书签栏（Chrome/Edge 的 Bookmarks Bar）是浏览器的固定容器，不是用户
			// 建的分类。把它当成一层文件夹，导入后每条书签都会多一层无意义前缀，
			// 故折叠成根。判据是 PERSONAL_TOOLBAR_FOLDER="true"，Chrome / Edge
			// 导出都会写；Firefox 叫「书签工具栏」，没有这个属性，按普通文件夹处理。
			if strings.EqualFold(attrValue(raw, "personal_toolbar_folder"), "true") {
				pending = ""
			}

		case "a":
			if closing || selfClosing {
				continue
			}
			href := attrValue(raw, "href")
			text, next, ok := readText(content, i)
			if ok {
				i = next
			}
			if href == "" {
				// 没有链接的 A 在部分导出里是分隔符，跳过
				continue
			}
			entries = append(entries, Entry{
				Title:   strings.TrimSpace(text),
				URL:     html.UnescapeString(href),
				Folder:  currentFolder(folders),
				AddDate: parseIntAttr(raw, "add_date"),
			})

		case "dd":
			if closing || selfClosing {
				continue
			}
			text, next, ok := readText(content, i)
			if ok {
				i = next
			}
			if n := len(entries); n > 0 {
				if d := strings.TrimSpace(text); d != "" {
					entries[n-1].Desc = d
				}
			}
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("没有解析到任何书签")
	}
	return entries, nil
}

// looksLikeBookmarkFile 粗判是不是书签导出。
//
// 只认 HREF：Safari 导出的 DOCTYPE 写法与 Chrome 不同，靠 DOCTYPE 判会漏；
// 而任何一份书签文件都必然有 A HREF。
func looksLikeBookmarkFile(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "href=")
}

// splitTag 拆出标签名、是否闭合、是否自闭合。
func splitTag(raw string) (name string, closing, selfClosing bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		closing = true
		raw = strings.TrimSpace(raw[1:])
	}
	if strings.HasSuffix(raw, "/") {
		selfClosing = true
		raw = strings.TrimSpace(raw[:len(raw)-1])
	}
	if idx := strings.IndexAny(raw, " \t\r\n"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.ToLower(raw), closing, selfClosing
}

// readText 读取标签之后的文本，返回文本内容与「下一个待处理位置」。
//
// 正常情况停在 </a> / </h3> / </dd>；浏览器导出偶尔漏写闭合标签，
// 此时退回到下一个 '<' 之前，不会把后续整段文档当标题吞掉。
func readText(content string, from int) (text string, next int, ok bool) {
	if from >= len(content) {
		return "", from, false
	}
	lt := strings.IndexByte(content[from:], '<')
	if lt < 0 {
		return html.UnescapeString(content[from:]), len(content), true
	}
	text = content[from : from+lt]
	// 跳过一个紧跟的闭合标签（</A> / </H3> / </DD>），否则下一轮会把它当条目处理
	rest := content[from+lt:]
	if strings.HasPrefix(rest, "</") {
		if gt := strings.IndexByte(rest, '>'); gt >= 0 {
			tag := strings.ToLower(strings.TrimSpace(rest[2:gt]))
			switch tag {
			case "a", "h3", "dd", "dt":
				return html.UnescapeString(text), from + lt + gt + 1, true
			}
		}
	}
	return html.UnescapeString(text), from + lt, true
}

// attrValue 从原始标签串里取属性值，属性名大小写不敏感，值支持双引号/单引号/无引号。
func attrValue(raw, key string) string {
	lower := strings.ToLower(raw)
	key += "="
	idx := 0
	for {
		pos := strings.Index(lower[idx:], key)
		if pos < 0 {
			return ""
		}
		pos += idx
		// 属性名必须紧跟在空白或标签名之后，避免 last_modified 命中 modified= 这类子串
		if pos > 0 {
			prev := lower[pos-1]
			if prev != ' ' && prev != '\t' && prev != '\r' && prev != '\n' {
				idx = pos + len(key)
				continue
			}
		}
		v := raw[pos+len(key):]
		v = strings.TrimLeft(v, " \t\r\n")
		if v == "" {
			return ""
		}
		switch v[0] {
		case '"', '\'':
			q := v[0]
			if end := strings.IndexByte(v[1:], q); end >= 0 {
				return v[1 : 1+end]
			}
			return v[1:]
		default:
			if end := strings.IndexAny(v, " \t\r\n>"); end >= 0 {
				return v[:end]
			}
			return v
		}
	}
}

func parseIntAttr(raw, key string) int64 {
	v := attrValue(raw, key)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// currentFolder 复制当前栈（去掉根占位空串）。
func currentFolder(stack []string) []string {
	out := make([]string, 0, len(stack))
	for _, f := range stack {
		if f != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sanitizeFolder 清洗文件夹名。
//
// 浏览器里能看到的名字可能带首尾空白或换行（Firefox 折叠「书签菜单」时留的空 H3），
// 空名文件夹直接丢弃——它的子条目会自然落到上一级。
func sanitizeFolder(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\r", " ")
	name = strings.ReplaceAll(name, "\n", " ")
	return strings.TrimSpace(name)
}
