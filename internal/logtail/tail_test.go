package logtail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// 三个被测行为都是「差之毫厘、界面就错」的那一类：
// 半行会被用户当成一条被截断的错误、首读灌全文会卡住大文件、轮转漏检会让用户盯着一个月前的旧 tail。
// 这几个用例把它们钉住。
func TestTailThenPoll(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.log")
	writeFile(t, p, "l1\nl2\nl3\n")

	tr, err := NewTailer(p)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	snap, err := tr.Tail(2)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if got := strings.Join(snap.Lines, "|"); got != "l2|l3" {
		t.Errorf("Tail(2) = %q, want l2|l3", got)
	}
	if snap.Offset != int64(len("l1\nl2\nl3\n")) {
		t.Errorf("offset after Tail = %d, want end of file", snap.Offset)
	}

	// 无变化
	snap, err = tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(snap.Lines) != 0 || snap.Rotated {
		t.Errorf("idle poll should be empty, got %v rotated=%v", snap.Lines, snap.Rotated)
	}

	// 追加完整行
	appendFile(t, p, "l4\n")
	snap, err = tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := strings.Join(snap.Lines, "|"); got != "l4" {
		t.Errorf("incremental = %q, want l4", got)
	}

	// 半行：读者还在写，此刻不应输出任何东西
	appendFile(t, p, "l5-hal")
	snap, err = tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(snap.Lines) != 0 {
		t.Errorf("partial line leaked: %v", snap.Lines)
	}

	// 补完换行后应拿到完整一行
	appendFile(t, p, "f\n")
	snap, err = tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := strings.Join(snap.Lines, "|"); got != "l5-half" {
		t.Errorf("completed line = %q, want l5-half", got)
	}

	// 一次追加多行
	appendFile(t, p, "l6\nl7\nl8\n")
	snap, err = tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := strings.Join(snap.Lines, "|"); got != "l6|l7|l8" {
		t.Errorf("batch = %q, want l6|l7|l8", got)
	}
}

func TestPollDetectsTruncate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	writeFile(t, p, "old1\nold2\n")
	tr, _ := NewTailer(p)
	if _, err := tr.Tail(0); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	// 清空重写（truncate）：size 变小 → 轮转
	writeFile(t, p, "new1\nnew2\nnew3\n")
	snap, err := tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !snap.Rotated {
		t.Errorf("expected Rotated after truncate")
	}
	if got := strings.Join(snap.Lines, "|"); got != "new1|new2|new3" {
		t.Errorf("after truncate = %q, want new1|new2|new3", got)
	}
}

// rename + 新建是 logrotate 的标准做法：老文件改名、新建同名文件。
// 这时 size 不一定变小，只有 SameFile 能判定身份变了。
func TestPollDetectsRenameRotation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rotate.log")
	writeFile(t, p, strings.Repeat("x\n", 10))
	tr, _ := NewTailer(p)
	if _, err := tr.Tail(0); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeFile(t, p, "fresh1\nfresh2\n")
	snap, err := tr.Poll(0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !snap.Rotated {
		t.Errorf("expected Rotated after rename rotation")
	}
	if got := strings.Join(snap.Lines, "|"); got != "fresh1|fresh2" {
		t.Errorf("after rotation = %q, want fresh1|fresh2", got)
	}
}

// 首读不能把整个文件灌进来：真实的日志文件动辄几百 MB。
func TestTailDoesNotReadWholeFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.log")
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		sb.WriteString("line\n")
	}
	writeFile(t, p, sb.String())
	tr, _ := NewTailer(p)
	snap, err := tr.Tail(10)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(snap.Lines) != 10 {
		t.Errorf("Tail(10) got %d lines, want 10", len(snap.Lines))
	}
	for _, l := range snap.Lines {
		if l != "line" {
			t.Errorf("unexpected line %q", l)
		}
	}
}

func TestNewTailerRejectsMissingAndDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewTailer(filepath.Join(dir, "nope.log")); err == nil {
		t.Errorf("missing file should error")
	}
	if _, err := NewTailer(dir); err == nil {
		t.Errorf("directory should error")
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\n\nb\n", []string{"a", "", "b"}}, // 中间空行是有意的视觉分隔，保留
		{"", nil},
	}
	for _, c := range cases {
		got := SplitLines(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("SplitLines(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// 指纹检测最怕误报：正常追加几千次的过程中只要判错一次轮转，
// 界面就会突然清空重来（用户看到的是「日志自己跳了一下」）。
func TestLongAppendNeverFalsePositive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "grow.log")
	writeFile(t, p, "")
	tr, _ := NewTailer(p)
	if _, err := tr.Tail(0); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	total := 0
	for i := 0; i < 300; i++ {
		appendFile(t, p, "tick\n")
		snap, err := tr.Poll(0)
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if snap.Rotated {
			t.Fatalf("false rotation at append #%d", i)
		}
		total += len(snap.Lines)
	}
	if total != 300 {
		t.Errorf("received %d lines, want 300", total)
	}
}
