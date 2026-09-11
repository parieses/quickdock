package env

import "testing"

// ollama 的 releases 里 rc/beta tag 常常没打 prerelease 标记，仅靠 prerelease 字段会漏，
// 故解析需按 tag 形态二次过滤（reOllamaVersion）。
func TestParseOllamaVersions(t *testing.T) {
	body := []byte(`[
		{"tag_name":"v0.34.0","prerelease":false,"draft":false},
		{"tag_name":"v0.33.3","prerelease":false,"draft":false},
		{"tag_name":"v0.34.1-rc0","prerelease":false,"draft":false},
		{"tag_name":"v0.32.10-beta.1","prerelease":false,"draft":false},
		{"tag_name":"v0.35.0","prerelease":true,"draft":false},
		{"tag_name":"v0.36.0","prerelease":false,"draft":true},
		{"tag_name":"v0.30.11","prerelease":false,"draft":false}
	]`)
	want := []string{"0.34.0", "0.33.3", "0.30.11"}
	got := parseOllamaVersions(body)
	if len(got) != len(want) {
		t.Fatalf("版本数不符: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项不符: got=%v want=%v", i, got, want)
		}
	}
	if parseOllamaVersions([]byte("not json")) != nil {
		t.Fatalf("非法 JSON 应返回 nil")
	}
}

// ollamaURL 一期只提供 Windows 资产：非 Windows 必须返回空串，
// 否则会去下不存在的包（macOS 是 .app bundle，Linux 是 .tar.zst，链路都未适配）。
func TestOllamaURLEmptyOnNonWindows(t *testing.T) {
	fn := ollamaURL("https://example.com/v{version}/ollama-windows-amd64.zip")
	if got := fn("0.34.0", "windows", "amd64"); got != "https://example.com/v0.34.0/ollama-windows-amd64.zip" {
		t.Fatalf("windows URL 不符: %s", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := fn("0.34.0", goos, "arm64"); got != "" {
			t.Fatalf("%s 应返回空 URL，实际: %s", goos, got)
		}
	}
}

// 注册表缺默认版本会让 Install 的 Versions()[0] 越界 panic；下载源为空则无从安装。
func TestOllamaRegistryDefaults(t *testing.T) {
	if len(Versions(RuntimeOllama)) == 0 {
		t.Fatalf("Registry 缺少默认版本")
	}
	if got := DisplayName(RuntimeOllama); got != "Ollama" {
		t.Fatalf("显示名不符: %s", got)
	}
	if len(CandidateURLs(RuntimeOllama, "0.34.0")) == 0 {
		t.Fatalf("Windows 下应至少有一个可用下载源")
	}
}

// 拉取进度按 digest 聚合：Ollama 并行下多层，只看最新一层会来回跳。
func TestOllamaPullAgg(t *testing.T) {
	agg := &ollamaPullAgg{}
	// 清单阶段：无 digest / 无 total → 百分比保持 0，状态透传
	if p := agg.add(OllamaPullEvent{Status: "pulling manifest"}); p.Percent != 0 || p.Status != "pulling manifest" {
		t.Fatalf("清单阶段不符: %+v", p)
	}
	// 两个层各 100 字节，第一层下满 → 50%
	agg.add(OllamaPullEvent{Status: "downloading", Digest: "sha256:a", Total: 100, Completed: 100})
	if p := agg.add(OllamaPullEvent{Status: "downloading", Digest: "sha256:b", Total: 100, Completed: 50}); p.Percent != 75 {
		t.Fatalf("聚合百分比应为 75，实际 %v", p.Percent)
	}
	// 同层重复事件取最新值，不是累加
	if p := agg.add(OllamaPullEvent{Status: "downloading", Digest: "sha256:b", Total: 100, Completed: 100}); p.Percent != 100 {
		t.Fatalf("同层重复应覆盖而非累加，实际 %v", p.Percent)
	}
	if p := agg.add(OllamaPullEvent{Status: "success"}); p.Total != 200 || p.Completed != 200 {
		t.Fatalf("总量统计不符: %+v", p)
	}
}
