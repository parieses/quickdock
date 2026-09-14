package sites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return string(data)
}

func TestSyncHostsFile_AppendAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n\n# 用户自己的条目\n10.0.0.1 corp.internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncHostsFile(path, []string{"b.test", "a.test", "a.test"}); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	first := readFile(t, path)
	if !strings.Contains(first, "# 用户自己的条目") || !strings.Contains(first, "10.0.0.1 corp.internal") {
		t.Errorf("用户既有条目被破坏:\n%s", first)
	}
	if !strings.Contains(first, "127.0.0.1 a.test\n127.0.0.1 b.test") {
		t.Errorf("域名未按序写入:\n%s", first)
	}

	// 幂等：重复写入内容必须完全一致
	if err := syncHostsFile(path, []string{"a.test", "b.test"}); err != nil {
		t.Fatalf("二次写入失败: %v", err)
	}
	if second := readFile(t, path); second != first {
		t.Errorf("重复写入不是幂等的:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestSyncHostsFile_ReplaceExistingBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	orig := "127.0.0.1 localhost\n" +
		hostsBlockBegin + "\n127.0.0.1 old.test\n" + hostsBlockEnd + "\n" +
		"10.0.0.1 corp.internal\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncHostsFile(path, []string{"new.test"}); err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "old.test") {
		t.Errorf("旧域名未清除:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1 new.test") {
		t.Errorf("新域名未写入:\n%s", got)
	}
	if !strings.Contains(got, "10.0.0.1 corp.internal") {
		t.Errorf("区块后的内容被破坏:\n%s", got)
	}
	// 反复替换不应累积空行
	for i := 0; i < 3; i++ {
		if err := syncHostsFile(path, []string{"new.test"}); err != nil {
			t.Fatal(err)
		}
	}
	if again := readFile(t, path); again != got {
		t.Errorf("反复替换产生了累积差异:\n--- got ---\n%s\n--- again ---\n%s", got, again)
	}
}

func TestSyncHostsFile_IncompleteBlockRecovered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	// 用户手动删掉了 end 标记：应当从 begin 起整段替换，而不是抛错或双写
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"+hostsBlockBegin+"\n127.0.0.1 stale.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncHostsFile(path, []string{"ok.test"}); err != nil {
		t.Fatalf("修复不完整区块失败: %v", err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "stale.test") {
		t.Errorf("残留旧域名: %s", got)
	}
	if strings.Count(got, hostsBlockBegin) != 1 {
		t.Errorf("begin 标记重复: %s", got)
	}
}

func TestSyncHostsFile_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "hosts")
	if err := syncHostsFile(path, []string{"x.test"}); err == nil {
		t.Error("目录不存在应返回错误（此处由 os.WriteFile 报错）")
	}
}

func TestSyncHostsFile_ClearKeepsMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syncHostsFile(path, []string{"a.test"}); err != nil {
		t.Fatal(err)
	}
	if err := syncHostsFile(path, nil); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "a.test") {
		t.Errorf("清空后仍残留域名: %s", got)
	}
	if !strings.Contains(got, hostsBlockBegin) || !strings.Contains(got, hostsBlockEnd) {
		t.Errorf("清空应保留标记以便下次原地更新: %s", got)
	}
	if domains, err := hostsBlockDomains(path); err != nil || len(domains) != 0 {
		t.Errorf("清空后解析结果应为空: %v %v", domains, err)
	}
}

func TestParseHostsBlock_OnlyOurLines(t *testing.T) {
	content := "127.0.0.1 localhost\n" +
		hostsBlockBegin + "\n" +
		"127.0.0.1 a.test\n" +
		"# 注释行\n" +
		"\n" +
		"::1 ipv6.test\n" + // 非 127.0.0.1，不认
		"malformed\n" +
		hostsBlockEnd + "\n"
	got := parseHostsBlock(content)
	if len(got) != 1 || got[0] != "a.test" {
		t.Errorf("应只解析出 a.test: %v", got)
	}
	if parseHostsBlock("no markers here") != nil {
		t.Error("无标记应返回 nil")
	}
}

func TestSameDomainSet(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{[]string{"a.test", "b.test"}, []string{"B.TEST", "a.test"}, true},
		{[]string{"a.test"}, []string{"a.test", "b.test"}, false},
		{[]string{"a.test", "a.test"}, []string{"a.test"}, true}, // 去重后等价
		{nil, nil, true},
		{nil, []string{"a.test"}, false},
	}
	for _, c := range cases {
		if got := sameDomainSet(c.a, c.b); got != c.want {
			t.Errorf("sameDomainSet(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestHostsFilePath_NonEmpty(t *testing.T) {
	if hostsFilePath() == "" {
		t.Error("hosts 路径不应为空")
	}
}
