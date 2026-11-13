package db

// 笔记全文检索的查询构造与命中片段。
//
// notes_fts 用 trigram tokenizer：中文没有空格，unicode61 会把整句切成一个
// token，搜「数据库」永远 0 命中。trigram 按 3 字滑窗切，中文子串可用，
// 代价是索引单位最小 3 字 —— 短于 3 字的查询根本不在索引里，必须退回 LIKE。
// 所以 SearchNotes 是「FTS 主路径 + 短查询/零命中回退 LIKE」的混合结构，
// 不能只留 FTS 那条。

import (
	"strings"
)

// noteFTSMatch 把用户输入转成 notes_fts 的 MATCH 表达式。
// 返回空串表示该查询 trigram 索引不到，调用方应走 LIKE 兜底。
//
// 与 kbFTSEscape 的区别：知识库那边收的是整段自然语言提问，要切几十个 gram
// 靠 bm25 排序；笔记搜索框收的是一两个关键词，整段作短语精度更高、也更便宜，
// 所以这里只在「中文长词」时才拆 3-gram，且不设 gram 预算上限（关键词本身很短）。
func noteFTSMatch(query string) string {
	q := strings.TrimSpace(kbFoldWidth(query))
	if q == "" {
		return ""
	}
	var parts []string
	for _, word := range strings.Fields(q) {
		if expr := noteFTSWord(word); expr != "" {
			parts = append(parts, expr)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// 多个关键词之间是 AND，与旧 LIKE 实现「整串子串」的直觉一致；
	// 单个词内部的多个 gram 是 OR（见 noteFTSWord）。
	return strings.Join(parts, " AND ")
}

// noteFTSWord 把一个词转成可检索条件。双引号内一切是字面量，
// 所以 : / . - 这些 FTS5 语法字符都不必转义；只需把引号翻倍。
func noteFTSWord(word string) string {
	runes := []rune(word)
	if len(runes) < 3 {
		return ""
	}
	var grams []string
	i := 0
	for i < len(runes) {
		j := i
		cjk := kbIsCJK(runes[i])
		for j < len(runes) && kbIsCJK(runes[j]) == cjk {
			j++
		}
		seg := runes[i:j]
		i = j
		if len(seg) < 3 {
			continue
		}
		if !cjk {
			// 英文/数字：整段作短语，trigram 会自行切成连续 3-gram 并要求全部命中。
			grams = append(grams, `"`+strings.ReplaceAll(string(seg), `"`, `""`)+`"`)
			continue
		}
		// 中文：3-gram 滑窗取 OR。原文含完整词时所有 gram 同时命中，
		// bm25 自然把它排最前，不必再叠一个整段短语。
		for k := 0; k+3 <= len(seg); k++ {
			grams = append(grams, `"`+strings.ReplaceAll(string(seg[k:k+3]), `"`, `""`)+`"`)
		}
	}
	if len(grams) == 0 {
		return ""
	}
	if len(grams) == 1 {
		return grams[0]
	}
	return "(" + strings.Join(grams, " OR ") + ")"
}

// NoteSnippet 从正文里截一段命中上下文，供搜索结果 / MCP 精简返回使用。
// 按 rune 截取，避免把中文切在 UTF-8 字节中间；换行压成空格，前端直接转义展示。
func NoteSnippet(content, query string, width int) string {
	if width <= 0 {
		width = 120
	}
	runes := []rune(strings.TrimSpace(content))
	if len(runes) <= width {
		return strings.Join(strings.Fields(string(runes)), " ")
	}
	pos := -1
	if fields := strings.Fields(strings.TrimSpace(query)); len(fields) > 0 {
		pos = runeSubIndex(runes, []rune(fields[0]))
	}
	start := 0
	if pos > width/3 {
		start = pos - width/3
	}
	if start+width > len(runes) {
		start = len(runes) - width
	}
	end := start + width
	if end > len(runes) {
		end = len(runes)
	}
	out := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

// runeSubIndex 在 rune 切片里找子序列，找不到返回 -1。
// 笔记正文规模是几千字量级，O(n*m) 足够，不必引 bytes.Index 那套字节换算。
func runeSubIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		matched := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}
