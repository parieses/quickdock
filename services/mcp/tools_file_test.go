package mcp

import (
	"strings"
	"testing"
)

// 行号必须对齐真实行：模型说「第 N 行」时，这个 N 得是文件里数出来的那个。
// 对齐不了的后果不是显示难看，而是它按错位的位置去做 file_edit。
func TestNumberLines(t *testing.T) {
	got := numberLines([]string{"a", "b"}, 41)
	if !strings.HasPrefix(got, "   41→a") {
		t.Fatalf("行号格式不对: %q", got)
	}
	if !strings.Contains(got, "\n   42→b") {
		t.Fatalf("第二行行号不对: %q", got)
	}
}

// 空切片返回空串而不是 "0→"：file_read 读到空区间时 content 必须是空，
// 否则模型会以为文件里真有一行空内容。
func TestNumberLinesEmpty(t *testing.T) {
	if got := numberLines(nil, 1); got != "" {
		t.Fatalf("空输入应为空串，实际 %q", got)
	}
}
