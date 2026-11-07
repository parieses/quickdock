// Package linkutil 提供「条目 ↔ 笔记 ↔ 待办」互链所需的纯文本解析。
//
// 刻意做成零依赖的叶子包：db 层要在事务内解析正文，service 层要在绑定方法里
// 拆 hint，两边都需要同一套 [[...]] 语义；放在这里谁都能引，且不会引入 import 环。
package linkutil

import (
	"regexp"
	"strings"
)

// WikiRef 是一个 [[...]] 链接解析后的结果。
// Type 为 "" 表示原文没写前缀（目标类型未知，由解析顺序决定）；
// 否则 ∈ note | todo | item。Label 是去掉前缀后的显示文本 / 标题。
type WikiRef struct {
	Type  string
	Label string
}

// wikiRe 只认同一行内的 [[...]]：字符类排除 [ ] 与换行，长度 1~120。
//
// Go regexp 是 RE2 —— 不能用 (?=) 环视、\1 反向引用，包级 MustCompile 若写错
// 会在 init 阶段 panic 且 go vet 查不出来，所以这里保持最朴素的形态：
// 两个字面量 \[\[ / \]\] 夹一个否定字符类，没有任何高级构造。
var wikiRe = regexp.MustCompile(`\[\[([^\[\]\n]{1,120})\]\]`)

const maxWikiRefs = 200

// ParseWikiLabel 拆出「类型前缀 + 显示文本」。
// 前缀大小写不敏感，但 Label 保留原文大小写（标题本身大小写有意义）。
// 单独导出，供 ResolveWikiTarget 与 ExtractWikiLabels 复用，避免两处口径漂移。
func ParseWikiLabel(raw string) WikiRef {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	for _, t := range []string{"note", "todo", "item"} {
		prefix := t + ":"
		if strings.HasPrefix(lower, prefix) {
			return WikiRef{Type: t, Label: strings.TrimSpace(raw[len(prefix):])}
		}
	}
	return WikiRef{Type: "", Label: raw}
}

// ExtractWikiLabels 从正文中提取所有 [[...]]，转成去重、限长的 WikiRef 列表。
//
// 代码块跳过：逐行扫描，遇到以 ``` 开头的行就 toggles 围栏状态，围栏内的行
// （含围栏标记行本身）一律不参与匹配。这是最朴素但足够的做法 —— 正则本身
// 已排除换行，链接不可能跨行，逐行处理与整段处理等价。
//
// 去重口径是「同 Type+Label 只留一条」：于是 [[标题]] 与 [[note:标题]]
// 被视为两条不同的引用（Type 不同），而重复出现的 [[标题]] 只算一次。
func ExtractWikiLabels(content string) []WikiRef {
	lines := strings.Split(content, "\n")
	refs := make([]WikiRef, 0)
	seen := make(map[string]bool)
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		for _, m := range wikiRe.FindAllStringSubmatch(line, -1) {
			ref := ParseWikiLabel(m[1])
			// 空标题（如 [[note:]]）没有落库意义，直接丢掉，避免生成无法兜底的空边。
			if ref.Label == "" {
				continue
			}
			key := ref.Type + "\x00" + ref.Label
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, ref)
			if len(refs) >= maxWikiRefs {
				return refs
			}
		}
	}
	return refs
}
