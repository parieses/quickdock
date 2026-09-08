package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveExecutable 验证裸名工具解析：
//   - 不存在的名字必须原样返回（保持旧报错行为）
//   - 命中的结果必须是存在的绝对路径
//   - chrome/msedge 在常规 Windows 上必装，用于验证 App Paths/安装目录链路
func TestResolveExecutable(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantSame bool // 期望原样返回
	}{
		{"empty", "", true},
		{"missing tool keeps name", "definitely-not-a-real-tool-xyz", true},
		{"abs path passthrough", os.Args[0], true},
		{"chrome via app paths or dir", "chrome", false},
		{"edge via app paths or dir", "msedge", false},
		{"cmd via PATH", "cmd", false},
	}
	for _, c := range cases {
		got := resolveExecutable(c.in)
		if got == "" && !c.wantSame {
			t.Errorf("resolveExecutable(%q) = empty", c.in)
			continue
		}
		if c.wantSame {
			if c.in != "" && got != strings.TrimSpace(c.in) {
				t.Errorf("resolveExecutable(%q) = %q, want unchanged", c.in, got)
			}
			continue
		}
		if !filepath.IsAbs(got) {
			t.Errorf("resolveExecutable(%q) = %q, want absolute path (installed browsers/cmd must resolve)", c.in, got)
		}
		if _, err := os.Stat(got); err != nil {
			t.Errorf("resolveExecutable(%q) = %q which does not exist: %v", c.in, got, err)
		}
	}
}
