package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quickdock/internal/aria2"
)

func TestCleanImportName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"big.raw", "big.raw", false},
		{"  a b.iso  ", "a b.iso", false},
		{"file.", "file", false},
		{"", "", true},
		{".", "", true},
		{"..", "", true},
		{`..\evil.iso`, "", true},
		{"sub/dir/file.iso", "", true},
		{`D:\dl\file.iso`, "", true},
		{"a..b.iso", "", true},
		{"x\x00y.iso", "xy.iso", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := cleanImportName(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("cleanImportName(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("cleanImportName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	if got, err := cleanImportName(strings.Repeat("长", 300)); err != nil || len([]rune(got)) != 200 {
		t.Errorf("超长文件名应截到 200 rune, got %d, err=%v", len([]rune(got)), err)
	}
}

func TestImportOptions(t *testing.T) {
	in := map[string]string{
		"header":                    "Authorization: Bearer x=y",
		"referer":                   "https://example.com",
		"split":                     "8",
		"gid":                       "old-gid",
		"pause":                     "true",
		"dir":                       `F:\Downloads`,
		"out":                       "old.iso",
		"file-allocation":           "prealloc",
		"continue":                  "true",
		"check-integrity":           "true",
		"bt-save-metadata":          "true",
		"HEADER-X-not-in-whitelist": "nope",
		"user-agent":                strings.Repeat("u", 5000),
	}
	got := importOptions(in)
	for _, k := range []string{"gid", "pause", "dir", "out", "file-allocation", "continue", "check-integrity", "bt-save-metadata", "HEADER-X-not-in-whitelist"} {
		if _, ok := got[k]; ok {
			t.Errorf("不该透传 %q", k)
		}
	}
	if got["header"] != "Authorization: Bearer x=y" {
		t.Errorf("header 里带 = 的值被截断: %q", got["header"])
	}
	if got["referer"] != "https://example.com" || got["split"] != "8" {
		t.Errorf("白名单选项丢失: %#v", got)
	}
	if len(got["user-agent"]) != 4096 {
		t.Errorf("超长值应截到 4096, got %d", len(got["user-agent"]))
	}
	if len(importOptions(nil)) != 0 {
		t.Error("nil 输入应返回空 map")
	}
}

func TestImportUris(t *testing.T) {
	got := importUris([]string{
		" https://a/x.iso ", "https://a/x.iso", "", `F:\old\a.torrent`,
		"magnet:?xt=urn:btih:abcd&dn=ubuntu.iso", "ftp://mirror/y.iso", "javascript:alert(1)",
	})
	want := []string{"https://a/x.iso", "magnet:?xt=urn:btih:abcd&dn=ubuntu.iso", "ftp://mirror/y.iso"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("importUris = %#v, want %#v", got, want)
	}
}

func TestImportSessionFileName(t *testing.T) {
	cases := []struct {
		name string
		task aria2.SessionTask
		want string
	}{
		{"out 优先", aria2.SessionTask{
			URIs:    []string{"https://a/real-name.iso"},
			Options: map[string]string{"out": `F:\Downloads\my file.iso`},
		}, "my file.iso"},
		{"URL 带查询串", aria2.SessionTask{
			URIs: []string{"https://oss.example.com/big.raw?Expires=1&Signature=a%3Db"},
		}, "big.raw"},
		{"磁力 dn", aria2.SessionTask{
			URIs: []string{"magnet:?xt=urn:btih:abcd&dn=ubuntu.iso"},
		}, "ubuntu.iso"},
		{"空任务", aria2.SessionTask{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := importSessionFileName(c.task); got != c.want {
				t.Errorf("importSessionFileName = %q, want %q", got, c.want)
			}
		})
	}
}

func TestImportReason(t *testing.T) {
	cases := []struct {
		item DlImportItem
		want string
	}{
		{DlImportItem{HasControl: true, HasData: true, Uris: []string{"https://a/x"}}, ""},
		{DlImportItem{HasControl: true, Uris: []string{"https://a/x"}}, "dlImportReasonNoData"},
		{DlImportItem{HasData: true, Uris: []string{"https://a/x"}}, "dlImportReasonNoControl"},
		{DlImportItem{Uris: []string{"https://a/x"}}, "dlImportReasonFresh"},
		{DlImportItem{HasControl: true, HasData: true}, "dlImportReasonNoUri"},
	}
	for i, c := range cases {
		if got := importReason(c.item); got != c.want {
			t.Errorf("case %d: importReason = %q, want %q", i, got, c.want)
		}
	}
}

func TestScanControlFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "movie")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sys := filepath.Join(root, "$RECYCLE.BIN")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "a.iso"), strings.Repeat("x", 123))
	write(filepath.Join(root, "a.iso.aria2"), "")
	write(filepath.Join(sub, "b.bin"), strings.Repeat("y", 7))
	write(filepath.Join(sub, "b.bin.ARIA2"), "") // 后缀大小写不敏感
	write(filepath.Join(sys, "c.iso.aria2"), "")
	write(filepath.Join(root, "not-a-control.txt"), "")

	got, err := scanControlFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("控制文件数 = %d, want 2: %#v", len(got), got)
	}
	byName := map[string]controlInfo{}
	for _, c := range got {
		byName[c.name] = c
	}
	a, ok := byName["a.iso"]
	if !ok || !a.hasData || a.size != 123 || a.dir != root {
		t.Errorf("a.iso 解析错: %#v", a)
	}
	b, ok := byName["b.bin"]
	if !ok || !b.hasData || b.size != 7 || b.dir != sub {
		t.Errorf("b.bin 解析错: %#v", b)
	}
}

func TestIsImportOptionError(t *testing.T) {
	if isImportOptionError(nil) {
		t.Error("nil 不算选项错误")
	}
	if !isImportOptionError(&importOptionErr{}) {
		t.Error("带 option 字样的错误应触发降级重试")
	}
	if isImportOptionError(os.ErrInvalid) {
		t.Error("普通 I/O 错误不该触发降级重试")
	}
}

// 只用来造一个带 option 字样的错误，验证降级重试的判定分支。
type importOptionErr struct{}

func (e *importOptionErr) Error() string { return "Invalid option: pause" }
