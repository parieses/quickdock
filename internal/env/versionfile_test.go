package env

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", name, err)
	}
}

func findHint(hints []VersionHint, rt Runtime) (VersionHint, bool) {
	for _, h := range hints {
		if h.Runtime == rt {
			return h, true
		}
	}
	return VersionHint{}, false
}

func TestDetectVersionHints_PlainFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".nvmrc", "18.20.0\n")
	writeFile(t, dir, ".php-version", " 8.3.4 \n")
	writeFile(t, dir, ".python-version", "# 注释行\n3.12.1\n")

	hints := DetectVersionHints(dir)
	if len(hints) != 3 {
		t.Fatalf("期望 3 条提示，得到 %d: %+v", len(hints), hints)
	}
	for rt, want := range map[Runtime]string{RuntimeNode: "18.20.0", RuntimePHP: "8.3.4", RuntimePython: "3.12.1"} {
		h, ok := findHint(hints, rt)
		if !ok || h.Version != want {
			t.Errorf("%s 解析错误: got=%+v want=%s", rt, h, want)
		}
	}
}

func TestDetectVersionHints_VPrefixStripped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".nvmrc", "v20.11.1")
	h, ok := findHint(DetectVersionHints(dir), RuntimeNode)
	if !ok || h.Version != "20.11.1" {
		t.Errorf("v 前缀未剥离: %+v", h)
	}
}

func TestDetectVersionHints_PackageJSONEngines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x","engines":{"node":">=18.0.0"}}`)
	h, ok := findHint(DetectVersionHints(dir), RuntimeNode)
	if !ok || h.Version != ">=18.0.0" || h.Source != "package.json" {
		t.Errorf("engines.node 解析错误: %+v", h)
	}
}

func TestDetectVersionHints_ComposerJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "composer.json", `{"require":{"php":"^8.1","monolog/monolog":"^3"}}`)
	h, ok := findHint(DetectVersionHints(dir), RuntimePHP)
	if !ok || h.Version != "^8.1" {
		t.Errorf("composer require.php 解析错误: %+v", h)
	}
}

func TestDetectVersionHints_GoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module quickdock\n\ngo 1.23.2\n\nrequire (\n\tgo.uber.org/zap v1.27.0\n)\n")
	h, ok := findHint(DetectVersionHints(dir), RuntimeGo)
	if !ok || h.Version != "1.23.2" {
		t.Errorf("go.mod go 指令解析错误: %+v", h)
	}
}

func TestDetectVersionHints_GoModToolchainWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module quickdock\n\ngo 1.23.2\n\ntoolchain go1.24.0\n")
	h, ok := findHint(DetectVersionHints(dir), RuntimeGo)
	if !ok || h.Version != "1.24.0" {
		t.Errorf("toolchain 应优先于 go 指令: %+v", h)
	}
}

func TestDetectVersionHints_ToolVersions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".tool-versions", "nodejs 20.11.1\nphp 8.2.15 8.3.4\n# ruby 3.3.0\nerlang 26.2\n")
	hints := DetectVersionHints(dir)
	if len(hints) != 3 {
		t.Fatalf("期望 3 条（node/php/erlang），得到 %d: %+v", len(hints), hints)
	}
	if h, _ := findHint(hints, RuntimePHP); h.Version != "8.2.15" {
		t.Errorf("多版本应取首个: %+v", h)
	}
	if _, ok := findHint(hints, RuntimeNode); !ok {
		t.Error("asdf 插件名 nodejs 未映射到 node")
	}
}

func TestDetectVersionHints_AncestorLookup(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".nvmrc", "18.0.0")
	child := filepath.Join(root, "packages", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	h, ok := findHint(DetectVersionHints(child), RuntimeNode)
	if !ok || h.Version != "18.0.0" {
		t.Errorf("未向上查找祖先目录的版本文件: %+v", h)
	}
}

func TestDetectVersionHints_NearestWins(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".nvmrc", "18.0.0")
	child := filepath.Join(root, "app")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, child, ".nvmrc", "20.11.1")

	hints := DetectVersionHints(child)
	if len(hints) != 1 {
		t.Fatalf("同一运行时只应保留一条（就近优先），得到 %d: %+v", len(hints), hints)
	}
	if hints[0].Version != "20.11.1" {
		t.Errorf("应取就近的 .nvmrc: %+v", hints[0])
	}
}

func TestDetectVersionHints_EmptyAndMissing(t *testing.T) {
	if got := DetectVersionHints(""); got != nil {
		t.Errorf("空目录应返回 nil: %+v", got)
	}
	if got := DetectVersionHints(t.TempDir()); len(got) != 0 {
		t.Errorf("无声明文件的目录应返回空: %+v", got)
	}
	// 内容非法不应 panic 也不应产出条目
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "not json")
	writeFile(t, dir, "go.mod", "")
	writeFile(t, dir, ".nvmrc", "# 只有注释\n")
	if got := DetectVersionHints(dir); len(got) != 0 {
		t.Errorf("非法内容应被静默跳过: %+v", got)
	}
}

func TestMatchInstalledVersion(t *testing.T) {
	installed := []string{"18.20.0", "20.11.1", "8.1.2", "8.1.10", "8.3.4", "1.23.2", "23.1.0"}
	cases := []struct {
		spec string
		want string
	}{
		{"18.20.0", "18.20.0"},  // 精确命中
		{"v20.11.1", "20.11.1"}, // v 前缀
		{"18", "18.20.0"},       // 大版本前缀
		{"^8.1", "8.1.10"},      // 前缀内取最高（8.1.10 > 8.1.2）
		{">=8.1.0", "8.1.10"},   // 三段无命中，回退到两段
		{"8", "8.3.4"},          // 大版本下取最高
		{"1.23.2", "1.23.2"},    // go
		{"2", ""},               // 段边界：2 不匹配 20.x / 23.x
		{"3", ""},               // 无匹配
		{"", ""},                // 空声明
		{"^8.1.0", "8.1.10"},    // 三段前缀回退
		{"8.1", "8.1.10"},       // 两段精确前缀
	}
	for _, c := range cases {
		if got := MatchInstalledVersion(c.spec, installed); got != c.want {
			t.Errorf("MatchInstalledVersion(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
	if got := MatchInstalledVersion("18", nil); got != "" {
		t.Errorf("空安装列表应返回空: %q", got)
	}
}

func TestMatchInstalledVersion_SegmentBoundary(t *testing.T) {
	// "1.2" 不应当匹配 "1.23.0"——这是前缀匹配最容易出的错
	installed := []string{"1.23.0", "1.2.9"}
	if got := MatchInstalledVersion("1.2", installed); got != "1.2.9" {
		t.Errorf("前缀必须落在段边界: got=%q want=1.2.9", got)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"8.1.10", "8.1.2", 1},
		{"8.1.2", "8.1.10", -1},
		{"1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", -1},
		{"2.0.0", "1.9.9", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
