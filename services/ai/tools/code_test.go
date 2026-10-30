package aitools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quickdock/internal/fsguard"
)

// 精确替换成功：只改一处，其余内容原样保留。
func TestFileEditReplacesOneOccurrence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := fileEdit(Deps{}, map[string]any{
		"path":       path,
		"old_string": "line2",
		"new_string": "LINE2",
		"_cwd":       dir,
	})
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	m := res.(map[string]any)
	if m["changed"] != true {
		t.Fatalf("changed 应为 true: %v", m)
	}
	if m["startLine"] != 2 {
		t.Fatalf("起始行应为 2，实际 %v", m["startLine"])
	}
	got, _ := os.ReadFile(path)
	want := "line1\nLINE2\nline3\n"
	if string(got) != want {
		t.Fatalf("文件内容错误:\n got=%q\nwant=%q", string(got), want)
	}
}

// 多处匹配必须拒绝：猜一处改掉的代价是改错代码，而错误信息里要带上位置，
// 模型才能把 old_string 扩大到自己能唯一定位。
func TestFileEditRefusesAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	original := "x = 1\ny = 2\nx = 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)

	_, err := fileEdit(Deps{}, map[string]any{
		"path":       path,
		"old_string": "x = 1",
		"new_string": "x = 9",
		"_cwd":       dir,
	})
	if err == nil {
		t.Fatal("多处匹配应报错")
	}
	if !strings.Contains(err.Error(), "2 次") || !strings.Contains(err.Error(), "第 1 行") {
		t.Fatalf("错误应说明出现次数与位置: %v", err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("报错时不该改动文件")
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatal("文件内容被改动了")
	}
}

// 替换不到时必须给出「最接近的行」：只回「没找到」等于让模型整份重读再猜一次。
func TestFileEditSuggestsSimilarLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.go")
	body := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fileEdit(Deps{}, map[string]any{
		"path": path,
		// 用空格代替了 tab，正是模型最常犯的错
		"old_string": `    println("hi")`,
		"new_string": `    println("bye")`,
		"_cwd":       dir,
	})
	if err == nil {
		t.Fatal("匹配不上应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "第 4 行") {
		t.Fatalf("错误里应给出行号提示: %v", msg)
	}
}

// 写白名单里没有家目录：用户让 AI 看文件很自然，但「改文件」必须有显式授权。
func TestWriteRootsExcludeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("取不到家目录")
	}
	roots := fsguard.WriteRoots(nil, "")
	if len(roots) == 0 {
		return // 没有任何授权目录，全部拒绝，符合预期
	}
	for _, r := range roots {
		if strings.EqualFold(filepath.Clean(r), filepath.Clean(home)) {
			t.Fatalf("家目录不该出现在写白名单里: %s", r)
		}
	}
}

// 没绑定工作目录时，写操作必须明确告诉模型「去让用户选目录」，
// 而不是给一个它看不懂的「路径不在范围内」。
func TestResolveWritablePathWithoutCwd(t *testing.T) {
	_, err := fsguard.ResolveWrite(nil, filepath.Join(t.TempDir(), "x.txt"), "")
	if err == nil {
		t.Skip("本机没有配置任何快捷项目录时才会走到这里")
	}
	if !strings.Contains(err.Error(), "工作目录") && !strings.Contains(err.Error(), "可写范围") {
		t.Fatalf("错误信息应指引用户授权: %v", err)
	}
}

// 目录列举：默认跳过 node_modules，并且目录排在文件前面。
func TestListDirSkipsIgnoredAndSortsDirsFirst(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := listDir(Deps{}, map[string]any{"path": dir, "_cwd": dir})
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	m := res.(map[string]any)
	entries := m["entries"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatalf("应只列出 src 与 a.txt，实际 %d 条: %v", len(entries), entries)
	}
	if entries[0]["type"] != "dir" {
		t.Fatalf("目录应排在前面: %v", entries[0])
	}
	if entries[0]["name"] != "src" {
		t.Fatalf("第一个应是 src，实际 %v", entries[0]["name"])
	}
}

// 新建文件：父目录不存在时要自动建出来（模型常写 docs/x.md 这种还没建的路径）。
func TestFileWriteCreatesFileAndParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs", "guide", "a.md")
	res, err := fileWrite(Deps{}, map[string]any{
		"path":    path,
		"content": "# 标题\n\n正文\n",
		"_cwd":    dir,
	})
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	m := res.(map[string]any)
	if m["created"] != true || m["changed"] != true {
		t.Fatalf("应标记为新建: %v", m)
	}
	if m["addedLines"] != 4 { // "# 标题" / "" / "正文" / 末尾空行的一段
		t.Fatalf("行数应为 4，实际 %v", m["addedLines"])
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("文件没写出来: %v", rerr)
	}
	if string(got) != "# 标题\n\n正文\n" {
		t.Fatalf("内容不符: %q", string(got))
	}
}

// 已存在默认不覆盖：整份覆盖会静默吃掉用户手改的内容，而「新建」本身不需要覆盖。
func TestFileWriteRefusesExistingWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(path, []byte("用户写的"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := fileWrite(Deps{}, map[string]any{
		"path":    path,
		"content": "模型写的",
		"_cwd":    dir,
	})
	if err == nil {
		t.Fatal("已存在且未传 overwrite 时应拒绝")
	}
	if !strings.Contains(err.Error(), "file_edit") || !strings.Contains(err.Error(), "overwrite=true") {
		t.Fatalf("错误应指出下一步该怎么改: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "用户写的" {
		t.Fatal("被拒绝时不该改动文件")
	}
}

// 显式 overwrite=true 才整份替换。
func TestFileWriteOverwriteReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := fileWrite(Deps{}, map[string]any{
		"path":      path,
		"content":   "new",
		"overwrite": true,
		"_cwd":      dir,
	})
	if err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	if m := res.(map[string]any); m["created"] != false {
		t.Fatalf("覆盖不该标记为新建: %v", m)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("内容未替换: %q", string(data))
	}
}

// 内容完全一致时不该再写一次盘（模型的重复调用会走到这里）。
func TestFileWriteSameContentIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("identical"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	res, err := fileWrite(Deps{}, map[string]any{
		"path":    path,
		"content": "identical",
		"_cwd":    dir,
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if m := res.(map[string]any); m["changed"] != false {
		t.Fatalf("内容相同时应报告无改动: %v", m)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("内容相同时不该重写文件")
	}
}

// 新建路径也要过写的白名单：cwd 之外一律拒绝。
func TestFileWriteOutsideWriteRoots(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.txt")
	_, err := fileWrite(Deps{}, map[string]any{
		"path":    outside,
		"content": "x",
		"_cwd":    dir,
	})
	if err == nil {
		t.Fatal("写白名单外的路径应被拒绝")
	}
	if !strings.Contains(err.Error(), "可写范围") && !strings.Contains(err.Error(), "可写的目录") {
		t.Fatalf("错误应说明白名单限制: %v", err)
	}
}
