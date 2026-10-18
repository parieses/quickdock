package env

import (
	"strings"
	"testing"
)

// TestGitMinGitURL 锁定 git-for-windows 的 MinGit 下载地址拼法。
// 回归背景：tag 为 v2.45.0.windows.1，但资产文件名是 MinGit-2.45.0-64-bit.zip，
// 早前把 ".windows.1" 一并拼进文件名，导致安装 Git 时 404。
func TestGitMinGitURL(t *testing.T) {
	def, ok := registry[RuntimeGit]
	if !ok || len(def.sources) == 0 {
		t.Fatal("registry 中缺少 git 下载源")
	}
	build := def.sources[0].Build

	const want = "https://github.com/git-for-windows/git/releases/download/v2.45.0.windows.1/MinGit-2.45.0-64-bit.zip"
	if got := build("2.45.0", "windows", "amd64"); got != want {
		t.Fatalf("MinGit 地址不匹配\n got: %s\nwant: %s", got, want)
	}

	// 资产文件名不得出现 .windows.N 后缀（tag 段除外）。
	for _, ver := range def.versions {
		got := build(ver, "windows", "amd64")
		if got == "" {
			t.Fatalf("git %s windows 地址为空", ver)
		}
		file := got[strings.LastIndex(got, "/")+1:]
		if strings.Contains(file, ".windows.") {
			t.Fatalf("git %s 资产文件名不应包含 .windows.N 后缀: %s", ver, file)
		}
		if !strings.HasPrefix(file, "MinGit-"+ver+"-") || !strings.HasSuffix(file, "-64-bit.zip") {
			t.Fatalf("git %s 资产文件名不符合 MinGit-<version>-64-bit.zip: %s", ver, file)
		}
		if !strings.Contains(got, "/v"+ver+".windows.1/") {
			t.Fatalf("git %s tag 段应为 v%s.windows.1: %s", ver, ver, got)
		}
	}

	if got := build("2.45.0", "linux", "amd64"); got != "" {
		t.Fatalf("linux 应返回空 URL，实际: %s", got)
	}
}
