package env

import (
	"reflect"
	"testing"
)

// 本文件只覆盖「纯函数」解析器与选择逻辑，不发网络请求——CI 里不会因上游抖动而红。

func TestParseFFmpegVersions(t *testing.T) {
	// GyanD/codexffmpeg 同一个仓库既有正式版 tag（9.0.2），也有每日 git 快照
	// （2026-10-19-git-xxx）与预发布。版本列表只应保留正式版。
	body := []byte(`[
		{"tag_name":"2026-10-19-git-2344518cc5"},
		{"tag_name":"9.0.2"},
		{"tag_name":"9.0"},
		{"tag_name":"9.1.0-rc1","prerelease":true},
		{"tag_name":"8.1.2"}
	]`)
	got := parseFFmpegVersions(body)
	want := []string{"9.0.2", "9.0", "8.1.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFFmpegVersions = %v, want %v", got, want)
	}
}

func TestParseFFmpegVersionsHTML(t *testing.T) {
	body := []byte(`<a href="/GyanD/codexffmpeg/releases/tag/9.0.2">9.0.2</a>
		<a href="/GyanD/codexffmpeg/releases/tag/2026-10-19-git-2344518cc5">snapshot</a>
		<a href="/GyanD/codexffmpeg/releases/tag/9.0.2">dup</a>`)
	got := parseFFmpegVersionsHTML(body)
	want := []string{"9.0.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseFFmpegVersionsHTML = %v, want %v", got, want)
	}
}

func TestParseMinioVersions(t *testing.T) {
	// MinIO 用日期 tag（含 "-"），不能走 parseTagVersions（它会剔除带 "-" 的 tag）
	body := []byte(`[
		{"tag_name":"RELEASE.2025-09-07T16-13-09Z"},
		{"tag_name":"RELEASE.2025-10-15T17-29-55Z"},
		{"tag_name":"RELEASE.2025-09-07T16-13-09Z.rc1","prerelease":true},
		{"tag_name":"v1.0.0"}
	]`)
	got := parseMinioVersions(body)
	want := []string{"RELEASE.2025-09-07T16-13-09Z", "RELEASE.2025-10-15T17-29-55Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMinioVersions = %v, want %v", got, want)
	}
}

func TestParseMinioVersionsHTML(t *testing.T) {
	body := []byte(`<a href="/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z">x</a>
		<a href="/minio/minio/releases/tag/RELEASE.2025-10-15T17-29-55Z">y</a>
		<a href="/minio/minio/releases/tag/RELEASE.2025-09-07T16-13-09Z">dup</a>`)
	got := parseMinioVersionsHTML(body)
	want := []string{"RELEASE.2025-09-07T16-13-09Z", "RELEASE.2025-10-15T17-29-55Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMinioVersionsHTML = %v, want %v", got, want)
	}
}

func TestParseMongoCurrentVersions(t *testing.T) {
	body := []byte(`{"versions":[
		{"version":"9.0.4"},
		{"version":"9.1.0-rc0"},
		{"version":"8.0.34"},
		{"version":""}
	]}`)
	got := parseMongoCurrentVersions(body)
	want := []string{"9.0.4", "8.0.34"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMongoCurrentVersions = %v, want %v", got, want)
	}
}

func TestValidateTopVersionsOnlyChecksHead(t *testing.T) {
	orig := registry[RuntimePython]
	t.Cleanup(func() { registry[RuntimePython] = orig })

	def := orig
	// 模拟「3.16.0 是幽灵版本」：只有它判 false
	def.versValidate = func(v string) bool { return v != "3.16.0" }
	def.versValidateTopN = 2
	registry[RuntimePython] = def

	// 头部 2 个（3.16.0 / 3.15.0）被复核，3.16.0 剔除
	got := validateTopVersions(RuntimePython, []string{"3.16.0", "3.15.0", "3.14.8"})
	want := []string{"3.15.0", "3.14.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validateTopVersions = %v, want %v", got, want)
	}

	// 超出 topN 的版本不复核：把 3.16.0 放到第 3 位应原样保留
	got2 := validateTopVersions(RuntimePython, []string{"3.15.0", "3.14.8", "3.16.0"})
	want2 := []string{"3.15.0", "3.14.8", "3.16.0"}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("validateTopVersions(out of range) = %v, want %v", got2, want2)
	}
}

func TestDedupeStrings(t *testing.T) {
	got := dedupeStrings([]string{"a", "b", "a", "", "c", "b"})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupeStrings = %v, want %v", got, want)
	}
}

func TestGHProxyURL(t *testing.T) {
	// github.com 的 Release 资产应被改写成 gh-proxy 前缀
	in := "https://github.com/GyanD/codexffmpeg/releases/download/9.0.2/ffmpeg-9.0.2-essentials_build.zip"
	want := ghProxyPrefix + in
	if got := ghProxyURL(in); got != want {
		t.Fatalf("ghProxyURL = %q, want %q", got, want)
	}
	// 非 GitHub 地址不改写
	if got := ghProxyURL("https://nodejs.org/dist/v22.23.3/node-v22.23.3-win-x64.zip"); got != "" {
		t.Fatalf("ghProxyURL(non-github) = %q, want empty", got)
	}
	// 已经是 gh-proxy 的地址不重复改写
	if got := ghProxyURL(want); got != "" {
		t.Fatalf("ghProxyURL(already proxied) = %q, want empty", got)
	}
}

func TestIsFatalStatus(t *testing.T) {
	cases := map[int]bool{
		404: true, 410: true, 403: true, 400: true,
		408: false, 429: false, // 临时性故障，保留重试
		500: false, 502: false, 200: false,
	}
	for code, want := range cases {
		if got := isFatalStatus(code); got != want {
			t.Errorf("isFatalStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestIsRedirectStatus(t *testing.T) {
	cases := map[int]bool{
		301: true, 302: true, 303: true, 307: true, 308: true,
		200: false, 206: false, 304: false, 404: false, 410: false,
	}
	for code, want := range cases {
		if got := isRedirectStatus(code); got != want {
			t.Errorf("isRedirectStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestResolveRedirect(t *testing.T) {
	base := "https://api.adoptium.net/v3/binary/latest/21/ga/windows/x64/jdk/hotspot/normal/eclipse"
	abs := "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12.1%2B1/OpenJDK21U.zip"
	if got, err := resolveRedirect(base, abs); err != nil || got != abs {
		t.Fatalf("绝对跳转: got %q err %v", got, err)
	}
	// 相对跳转要基于 base 的目录解析（base 末段 eclipse 被替换，再回退一层）
	if got, err := resolveRedirect(base, "../x.zip"); err != nil || got != "https://api.adoptium.net/v3/binary/latest/21/ga/windows/x64/jdk/hotspot/x.zip" {
		t.Fatalf("相对跳转: got %q err %v", got, err)
	}
	// 非 http(s) 协议拒绝，避免把 file:// 当成可下载源
	if _, err := resolveRedirect(base, "file:///c:/x.zip"); err == nil {
		t.Fatal("file:// 应被拒绝")
	}
}
