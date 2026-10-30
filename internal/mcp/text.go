package mcp

import "unicode/utf8"

// TruncateRunes 按字符截断（不切断中文/emoji），超出时补省略号。
//
// 工具结果几乎总要截断：模型的上下文是有限资源，而一行压缩过的 JS、
// 一条 grep 命中、一份 diff 都可能长到没人想读完。按 rune 而不是按 byte 截，
// 否则中文会被切成半个字，模型看到的是一串替换符。
func TruncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}
