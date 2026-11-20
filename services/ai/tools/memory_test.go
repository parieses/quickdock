package aitools

// project_memory 的分册（memory/_distill/）行为守卫。
//
// 分册是「细节长文放这里、MEMORY.md 只留结论」那半边：如果每次写都新建一个
// 03-插件.md、04-插件.md，索引会变成一串同名条目，等于没有索引。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTopicName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"插件系统", "插件系统"},
		{"01-插件系统", "01-插件系统"},
		{"插件系统/宿主", "插件系统-宿主"},
		{"  a b  c  ", "a-b-c"},
		{"构建&发布", "构建-发布"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeTopicName(c.in); got != c.want {
			t.Errorf("sanitizeTopicName(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 同名分册复用同一个编号；新主题自动续号。
func TestMemoryTopicPathReusesAndNumbers(t *testing.T) {
	root := t.TempDir()
	p1, err := memoryTopicPath(root, "插件系统")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p1) != "01-插件系统.md" {
		t.Fatalf("新主题应从 01 开始，实际 %q", filepath.Base(p1))
	}
	if err := os.MkdirAll(filepath.Dir(p1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p1, []byte("# 插件系统\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 再写同一主题（带不带编号都行）→ 复用 01，不新建 02。
	for _, name := range []string{"插件系统", "01-插件系统", "插件系统.md"} {
		got, perr := memoryTopicPath(root, name)
		if perr != nil {
			t.Fatal(perr)
		}
		if got != p1 {
			t.Errorf("topic=%q 应复用 %q，实际 %q", name, filepath.Base(p1), filepath.Base(got))
		}
	}

	p2, err := memoryTopicPath(root, "截图工具")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p2) != "02-截图工具.md" {
		t.Errorf("新主题应续号到 02，实际 %q", filepath.Base(p2))
	}
}

// 写入分册后，读取能列出它（带第一行标题）。
func TestListMemoryTopics(t *testing.T) {
	root := t.TempDir()
	path, err := memoryTopicPath(root, "构建")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMemoryFile(path, "# 构建与发布全史\n\n正文\n"); err != nil {
		t.Fatal(err)
	}
	topics := listMemoryTopics(root)
	if len(topics) != 1 {
		t.Fatalf("应列出 1 个分册，实际 %v", topics)
	}
	if !strings.Contains(topics[0], "memory/_distill/01-构建.md") && !strings.Contains(topics[0], "memory\\_distill\\01-构建.md") {
		t.Errorf("路径不对: %q", topics[0])
	}
	if !strings.Contains(topics[0], "构建与发布全史") {
		t.Errorf("应带上第一行标题: %q", topics[0])
	}
}

// 空主题名直接报错，不能写出一个叫 ".md" 的文件。
func TestMemoryTopicPathRejectsEmpty(t *testing.T) {
	if _, err := memoryTopicPath(t.TempDir(), "   "); err == nil {
		t.Fatal("空 topic 应报错")
	}
}
