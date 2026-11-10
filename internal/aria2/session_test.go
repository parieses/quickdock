package aria2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 直接拿本机 aria2 真实写出来的格式做样本：URI 行尾带 TAB，
// 后面每个选项是一行「空格 + key=value」。
const realSession = "https://oss.example.com/big.raw?Expires=1795241594&Signature=a%3Db\t\n" +
	" gid=766322f4c7a2a3db\n" +
	" pause=true\n" +
	" dir=F:\\Downloads\n" +
	" split=8\n" +
	" continue=true\n"

func TestParseSessionRealFormat(t *testing.T) {
	got := ParseSession([]byte(realSession))
	if len(got) != 1 {
		t.Fatalf("任务数 = %d, want 1", len(got))
	}
	task := got[0]
	if len(task.URIs) != 1 || !strings.HasSuffix(task.URIs[0], "Signature=a%3Db") {
		t.Errorf("URI 解析错: %#v", task.URIs)
	}
	if task.GID() != "766322f4c7a2a3db" {
		t.Errorf("gid = %q", task.GID())
	}
	if !task.Paused() {
		t.Error("pause=true 没被读到")
	}
	if task.Options["dir"] != `F:\Downloads` {
		t.Errorf("dir = %q", task.Options["dir"])
	}
	if task.Options["split"] != "8" || task.Options["continue"] != "true" {
		t.Errorf("选项丢失: %#v", task.Options)
	}
}

func TestParseSessionMultipleTasks(t *testing.T) {
	// BOM + CRLF + 多 URI 镜像 + 注释 + 空行 + 值里带 = 的 header，
	// 全都是旧版 aria2 / 第三方前端可能写出来的东西，不能让后面的块错位。
	raw := "\uFEFF" +
		"# saved by aria2\n" +
		"https://a/one.iso\thttps://mirror/two.iso\t\r\n" +
		" gid=aaaa\r\n" +
		" header=Authorization: Bearer x=y\r\n" +
		" dir=/old/downloads\r\n" +
		"\r\n" +
		"magnet:?xt=urn:btih:1234&dn=ubuntu.iso\t\n" +
		" gid=bbbb\n" +
		" broken line without equals\n" +
		" pause=false\n"
	got := ParseSession([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("任务数 = %d, want 2: %#v", len(got), got)
	}
	if len(got[0].URIs) != 2 || got[0].URIs[1] != "https://mirror/two.iso" {
		t.Errorf("镜像 URI 丢了: %#v", got[0].URIs)
	}
	if got[0].Options["header"] != "Authorization: Bearer x=y" {
		t.Errorf("带 = 的值被截断: %q", got[0].Options["header"])
	}
	if got[0].Options["dir"] != "/old/downloads" || got[0].GID() != "aaaa" {
		t.Errorf("第一块选项错: %#v", got[0].Options)
	}
	if got[1].GID() != "bbbb" || got[1].Paused() {
		t.Errorf("第二块选项错: %#v", got[1].Options)
	}
	if _, ok := got[1].Options["dir"]; ok {
		t.Error("第二块不该继承第一块的 dir")
	}
}

func TestParseSessionEmptyAndOrphanOptions(t *testing.T) {
	if got := ParseSession(nil); len(got) != 0 {
		t.Errorf("空文件应返回空列表, got %#v", got)
	}
	// 出现在任何 URI 行之前的选项行必须丢弃，不能造出空 URI 任务。
	got := ParseSession([]byte(" gid=orphan\n\thttps://a/x.bin\t\n pause=true\n"))
	if len(got) != 1 || got[0].URIs[0] != "https://a/x.bin" {
		t.Fatalf("孤立选项处理错: %#v", got)
	}
	if _, ok := got[0].Options["gid"]; ok {
		t.Error("URI 行之前的 gid 不该串到本任务")
	}
	// 缩进的预签名 URL 查询串里带 '='，不能被当成选项吃掉。
	got = ParseSession([]byte("\thttps://a/x.bin?Expires=1&Signature=a=b\t\n pause=true\n"))
	if len(got) != 1 || got[0].URIs[0] != "https://a/x.bin?Expires=1&Signature=a=b" {
		t.Fatalf("带等号的 URI 被误判成选项: %#v", got)
	}
	if !got[0].Paused() {
		t.Error("URI 后面的 pause=true 没读到")
	}
}

func TestReadSessionGuards(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSession(filepath.Join(dir, "missing")); err == nil {
		t.Error("缺失文件应报错")
	}
	if _, err := ReadSession(dir); err == nil {
		t.Error("传入目录应报错")
	}
	p := filepath.Join(dir, "aria2.session")
	if err := os.WriteFile(p, []byte(realSession), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSession(p)
	if err != nil || len(got) != 1 {
		t.Fatalf("ReadSession = %#v, %v", got, err)
	}
}
