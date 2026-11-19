package mcp

import (
	"os"
	"path/filepath"
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

// findFiles 是 file_search 缺席时的兜底：没有 Everything 也得能按名字找文件。
// 这里锁住三件最容易坏的事：跳过构建产物目录、命中目录本身、上限要如实说明。
func TestFindFilesByName(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	write("a.go")
	write("sub/b.go")
	write("sub/readme.md")
	write("node_modules/dep/c.js") // 必须被 SkipDir 剪掉
	write(".tmp_scan/keep.txt")

	names := func(res any) ([]string, map[string]any) {
		m, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("结果类型不对: %T", res)
		}
		matches, _ := m["matches"].([]map[string]any)
		out := make([]string, 0, len(matches))
		for _, it := range matches {
			out = append(out, filepath.ToSlash(it["path"].(string)))
		}
		return out, m
	}
	rel := func(paths []string) []string {
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			r, err := filepath.Rel(root, filepath.FromSlash(p))
			if err != nil {
				t.Fatalf("Rel 失败: %v", err)
			}
			out = append(out, filepath.ToSlash(r))
		}
		return out
	}

	res, err := findFiles(nil, root, "*.go", true, 50, 8, root, nil)
	if err != nil {
		t.Fatalf("file_find 失败: %v", err)
	}
	got, _ := names(res)
	if want := []string{"a.go", "sub/b.go"}; strings.Join(rel(got), ",") != strings.Join(want, ",") {
		t.Fatalf("通配符命中不对: %v（期望 %v）", rel(got), want)
	}

	// 子串匹配 + 目录命中：用户说「找 tmp_scan」指的多半是这个目录本身。
	res, err = findFiles(nil, root, "tmp_scan", true, 50, 8, root, nil)
	if err != nil {
		t.Fatalf("file_find 失败: %v", err)
	}
	m := (res).(map[string]any)
	matches := m["matches"].([]map[string]any)
	if len(matches) != 1 || matches[0]["type"] != "dir" {
		t.Fatalf("应命中 .tmp_scan 目录本身，实际 %v", matches)
	}

	// 上限必须报出来：不报 truncated，模型会以为这就是全部。
	res, err = findFiles(nil, root, "*.go", true, 1, 8, root, nil)
	if err != nil {
		t.Fatalf("file_find 失败: %v", err)
	}
	if m := (res).(map[string]any); m["truncated"] != true || m["truncatedNote"] == "" {
		t.Fatalf("到上限时应标 truncated 并说明原因，实际 %v", m)
	}
}

// 名字匹配的两条口径：有通配符走 Match（大小写折叠），没有就按子串。
// 不折叠的话 Windows 上 *.Go 会一个都匹配不到，用户只会觉得「工具坏了」。
func TestNameMatcher(t *testing.T) {
	if !nameMatcher("*.go", true)("Main.GO") {
		t.Error("ignoreCase 下 *.go 应匹配 Main.GO")
	}
	if nameMatcher("*.go", false)("Main.GO") {
		t.Error("区分大小写时 *.go 不该匹配 Main.GO")
	}
	if !nameMatcher("config", true)("My-Config.yaml") {
		t.Error("无通配符时应按子串包含匹配（且折叠大小写）")
	}
	if nameMatcher("config", true)("readme.md") {
		t.Error("不相关名字不该命中")
	}
}
