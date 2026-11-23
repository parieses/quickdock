package platform

import "testing"

func TestSanitizeLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"color", "\x1b[32mok\x1b[0m done", "ok done"},
		{"bold+color", "\x1b[1m\x1b[31mERR\x1b[0m: boom", "ERR: boom"},
		{"osc title", "\x1b]0;npm install\x07installing", "installing"},
		{"osc st", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"charset", "\x1b(Btext", "text"},
		{"cr progress", "10%\r50%\r100%", "100%"},
		{"cr then color", "\x1b[2K\rDownloading 1MB", "Downloading 1MB"},
		{"cr empty tail", "done 100%\r", "done 100%"},
		{"bare bracket", "a[b]c", "a[b]c"},
		// 8 位 CSI 是单字节 0x9b（不是 UTF-8 编码的 U+009B），按原始字节构造。
		{"8bit csi", string([]byte{0x9b}) + "32mgreen" + string([]byte{0x9b}) + "0m", "green"},
		{"nul", "abc\x00", "abc"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := sanitizeLine(c.in); got != c.want {
			t.Errorf("%s: sanitizeLine(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
