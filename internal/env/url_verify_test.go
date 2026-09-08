package env

import (
	"strings"
	"testing"
)

// 验证 windows 守卫修复后，各构造器在 windows 下返回非空且占位符被正确替换。
func TestWindowsURLResolve(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string, string, string) func(string, string, string) string
		ver  string
	}{
		{"php", phpURL, "8.3.1"},
		{"redis", redisURL, "7.2.4"},
		{"nginx", nginxURL, "1.25.3"},
		{"caddy", caddyURL, "2.7.6"},
		{"ffmpeg", ffmpegURL, "6.1"},
		{"minio", minioURL, "RELEASE.2024-01-01T00-00-00Z"},
		{"traefik", traefikURL, "2.11.0"},
		{"mkcert", mkcertURL, "v1.4.4"},
		{"mariadb", mariadbURL, "11.3.2"},
		{"postgres", postgresURL, "16.2-1"},
		{"mysql", mysqlURL, "8.4.3"},
		{"rabbit", rabbitURL, "3.13.0"},
		{"erlang", erlangURL, "26.2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			winTmpl := "https://win.example/{version}/x64"
			darwinTmpl := "https://darwin.example/{version}/{arch}"
			got := c.fn(winTmpl, darwinTmpl, "")(c.ver, "windows", "amd64")
			if got == "" {
				t.Fatalf("%s windows URL 为空（return bug 未修复）", c.name)
			}
			if strings.Contains(got, "{version}") {
				t.Fatalf("%s windows URL 占位符未替换: %s", c.name, got)
			}
			if !strings.Contains(got, c.ver) {
				t.Fatalf("%s windows URL 未含版本号 %s: %s", c.name, c.ver, got)
			}
		})
	}
}

// 验证 darwin 分支在传入 darwin 时返回非空的 mac URL 且占位符替换正确。
func TestDarwinURLResolve(t *testing.T) {
	cases := []struct {
		name  string
		fn    func(string, string, string) func(string, string, string) string
		ver   string
		style string
	}{
		{"caddy", caddyURL, "2.7.6", ""},
		{"gh", ghURL, "2.45.0", "x86"},
		{"bun", bunURL, "1.0.0", "x64"},
		{"traefik", traefikURL, "2.11.0", ""},
		{"mkcert", mkcertURL, "v1.4.4", ""},
		{"mongodb", mongoURL, "7.0.5", "x86"},
		{"mailpit", mailpitURL, "v1.0.0", ""},
		{"minio", minioURL, "REL", ""},
		{"frpc", frpcURL, "0.58.0", ""},
		{"php", phpURL, "8.3.1", ""},
		{"redis", redisURL, "7.2.4", ""},
		{"nginx", nginxURL, "1.25.3", ""},
		{"postgres", postgresURL, "16.2-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			winTmpl := "https://win.example/{version}"
			darwinTmpl := "https://darwin.example/{version}/{arch}"
			got := c.fn(winTmpl, darwinTmpl, c.style)(c.ver, "darwin", "arm64")
			if got == "" {
				t.Fatalf("%s darwin URL 为空（darwin 分支未生效）", c.name)
			}
			if strings.Contains(got, "{version}") || strings.Contains(got, "{arch}") {
				t.Fatalf("%s darwin URL 占位符未替换: %s", c.name, got)
			}
		})
	}
}

// 验证 linux 等其他平台返回空（不应误下）。
func TestLinuxURLEmpty(t *testing.T) {
	got := caddyURL("win", "darwin", "")("2.7.6", "linux", "amd64")
	if got != "" {
		t.Fatalf("linux 应返回空 URL，实际: %s", got)
	}
}
