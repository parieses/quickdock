package bookmark

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Chromium 系（Chrome / Edge / Brave / Chromium / Vivaldi / 360 / QQ）的书签文件。
//
// 结构是固定的三层 JSON：roots → bookmark_bar / other / synced → 递归 children。
// 节点 type 只有 "url" 与 "folder" 两种，url 节点带 url / name / date_added。
//
// 为什么不复用 Parse（HTML 解析器）：两者格式毫无交集，JSON 有现成的
// encoding/json，硬塞进 HTML 扫描器反而要把两种语法混在一段循环里。

// chromiumNode 一个书签或文件夹节点。
//
// 只用得到这几个字段；meta_info / guid / id 之类对导入没有意义，不声明。
type chromiumNode struct {
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	URL       string         `json:"url"`
	DateAdded string         `json:"date_added"`
	Children  []chromiumNode `json:"children"`
}

// chromiumNodeLimit 递归深度上限。正常书签不会超过十层，挡住畸形文件把栈打穿。
const chromiumNodeLimit = 32

// ParseChromiumBookmarks 解析 Chromium 系的 Bookmarks 文件。
func ParseChromiumBookmarks(path string) ([]Entry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("选中的是文件夹，不是 Bookmarks 文件")
	}
	if info.Size() > maxParseBytes {
		return nil, fmt.Errorf("文件过大（超过 %d MB），不像是浏览器书签", maxParseBytes>>20)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var file struct {
		Roots struct {
			BookmarkBar *chromiumNode `json:"bookmark_bar"`
			Other       *chromiumNode `json:"other"`
			Synced      *chromiumNode `json:"synced"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("不是 Chromium 系书签文件：%w", err)
	}

	// 从三个根的 children 开始走：书签栏 / 其他书签 / 移动设备书签是浏览器的固定
	// 容器，和 HTML 导出里的 PERSONAL_TOOLBAR_FOLDER 一样，不该变成一级文件夹。
	entries := make([]Entry, 0, 64)
	for _, root := range []*chromiumNode{file.Roots.BookmarkBar, file.Roots.Other, file.Roots.Synced} {
		if root != nil {
			walkChromium(root, nil, 0, &entries)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("没有解析到任何书签")
	}
	return entries, nil
}

func walkChromium(node *chromiumNode, folder []string, depth int, out *[]Entry) {
	if depth > chromiumNodeLimit {
		return
	}
	for i := range node.Children {
		child := &node.Children[i]
		switch child.Type {
		case "url":
			if strings.TrimSpace(child.URL) == "" {
				continue
			}
			*out = append(*out, Entry{
				Title:   strings.TrimSpace(child.Name),
				URL:     child.URL,
				Folder:  cloneFolder(folder),
				AddDate: chromiumTime(child.DateAdded),
			})
		case "folder":
			name := sanitizeFolder(child.Name)
			next := folder
			if name != "" {
				next = append(cloneFolder(folder), name)
			}
			walkChromium(child, next, depth+1, out)
		}
	}
}

// chromiumTime Chromium 的 date_added 是「1601 年起的微秒数」，转成 Unix 秒。
// 解析失败返回 0（导入不依赖时间，只是排序参考）。
func chromiumTime(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	const epochOffset = 11644473600 // 1601-01-01 → 1970-01-01 的秒数
	return v/1_000_000 - epochOffset
}

func cloneFolder(folder []string) []string {
	if len(folder) == 0 {
		return nil
	}
	return append([]string(nil), folder...)
}
