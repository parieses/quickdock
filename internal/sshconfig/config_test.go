package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertHosts 逐字段比较：Host 全是可比较字段，直接 != 就能定位到是哪一台、哪个字段不符。
func assertHosts(t *testing.T, got, want []Host) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("主机数 = %d, want %d; got = %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 台不符:\n got: %+v\nwant: %+v", i, got[i], want[i])
		}
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s 失败: %v", path, err)
	}
}

func TestParse_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Host
	}{
		{
			name: "多别名+通配默认值+first-value-wins",
			// Host * 在文件顶部，其 Port/User 先出现即胜出；具体块里后写的同名指令被忽略。
			in: "Host *\n" +
				"    User root\n" +
				"    Port 2200\n" +
				"    ServerAliveInterval 30\n" +
				"\n" +
				"Host web db\n" +
				"    HostName 10.0.0.1\n" +
				"    Port 8022\n" +
				"    User deploy\n" +
				"\n" +
				"Host api\n" +
				"    HostName 10.0.0.2\n",
			want: []Host{
				{Alias: "web", HostName: "10.0.0.1", User: "root", Port: 2200, ServerAliveInterval: 30, HasAliveInterval: true},
				{Alias: "db", HostName: "10.0.0.1", User: "root", Port: 2200, ServerAliveInterval: 30, HasAliveInterval: true},
				{Alias: "api", HostName: "10.0.0.2", User: "root", Port: 2200, ServerAliveInterval: 30, HasAliveInterval: true},
			},
		},
		{
			name: "Match 块隔离",
			// Match 之后的指令整体忽略，既不污染前面的 good，也不污染后面的 after。
			in: "Host good\n" +
				"    HostName good.example.com\n" +
				"    User alice\n" +
				"\n" +
				"Match exec \"test -f /tmp/flag\"\n" +
				"    User attacker\n" +
				"    Port 9999\n" +
				"    HostName evil.example.com\n" +
				"\n" +
				"Host after\n" +
				"    HostName after.example.com\n",
			want: []Host{
				{Alias: "good", HostName: "good.example.com", User: "alice", Port: 22},
				{Alias: "after", HostName: "after.example.com", Port: 22},
			},
		},
		{
			name: "Key=Value+引号+行内注释+显式0",
			// 引号内的 # 不是注释；引号外的 # 才是。ServerAliveInterval=0 要用 HasAliveInterval 区分未配置。
			in: "Host=quoted\n" +
				"    HostName=\"box.example.com\"\n" +
				"    User = \"bob # not a comment\"  # real comment\n" +
				"    Port=2222\n" +
				"    ProxyJump=jump.example.com # inline\n" +
				"    ProxyCommand=\"nc -W %h:%p jump\" # only recorded\n" +
				"    IdentitiesOnly=YES\n" +
				"    RequestTTY=force\n" +
				"    ServerAliveInterval=0\n" +
				"    ServerAliveCountMax=3\n",
			want: []Host{
				{
					Alias: "quoted", HostName: "box.example.com", User: "bob # not a comment",
					Port: 2222, ProxyJump: "jump.example.com", ProxyCommand: "nc -W %h:%p jump",
					IdentitiesOnly: true, RequestTTY: true,
					ServerAliveInterval: 0, HasAliveInterval: true, ServerAliveCountMax: 3,
				},
			},
		},
		{
			name: "CRLF",
			in:   "Host crlf\r\n\tHostName crlf.example.com\r\n\tPort 2022\r\n",
			want: []Host{{Alias: "crlf", HostName: "crlf.example.com", Port: 2022}},
		},
		{
			name: "UTF-8 BOM",
			in:   "\xEF\xBB\xBFHost bom\n    HostName bom.example.com\n",
			want: []Host{{Alias: "bom", HostName: "bom.example.com", Port: 22}},
		},
		{
			name: "非法端口回退 22",
			in: "Host p1\n    Port abc\n" +
				"Host p2\n    Port 70000\n" +
				"Host p3\n    Port 0\n" +
				"Host p4\n    Port -5\n",
			want: []Host{
				{Alias: "p1", HostName: "p1", Port: 22},
				{Alias: "p2", HostName: "p2", Port: 22},
				{Alias: "p3", HostName: "p3", Port: 22},
				{Alias: "p4", HostName: "p4", Port: 22},
			},
		},
		{
			name: "非法端口被跳过而非占用字段",
			// 解析不了的指令跳过，后面的有效 Port 仍能生效。
			in:   "Host recover\n    Port abc\n    Port 8022\n",
			want: []Host{{Alias: "recover", HostName: "recover", Port: 8022}},
		},
		{
			name: "以 - 开头的别名与主机名被拒",
			// -oProxyCommand=... 这类取值映射到命令行会被当成选项注入；别名与主机名各测一处。
			in: "Host -oProxyCommand=evil good\n    HostName 10.0.0.1\n" +
				"Host victim\n    HostName -oProxyCommand=pwn\n",
			want: []Host{{Alias: "good", HostName: "10.0.0.1", Port: 22}},
		},
		{
			name: "空文件",
			in:   "",
			want: nil,
		},
		{
			name: "仅注释与空行",
			in:   "\n\n# 只有注释\n    # 缩进注释\n",
			want: nil,
		},
		{
			name: "空别名块不产出主机",
			in:   "Host\n    User x\n",
			want: nil,
		},
		{
			name: "HostName 未写回落到 Alias",
			in:   "Host foo bar\n    User bob\n",
			want: []Host{
				{Alias: "foo", HostName: "foo", User: "bob", Port: 22},
				{Alias: "bar", HostName: "bar", User: "bob", Port: 22},
			},
		},
		{
			name: "底部 Host * 只补未赋值字段",
			// 具体块里已赋值的 User 不能被后面的通配块覆盖；未赋值的 ServerAliveInterval 由它补上。
			in: "Host early\n" +
				"    User alice\n" +
				"    HostName early.example.com\n" +
				"\n" +
				"Host *\n" +
				"    User global\n" +
				"    ServerAliveInterval 60\n",
			want: []Host{
				{Alias: "early", HostName: "early.example.com", User: "alice", Port: 22, ServerAliveInterval: 60, HasAliveInterval: true},
			},
		},
		{
			name: "yes/no/auto 取值",
			in: "Host flags\n" +
				"    IdentitiesOnly no\n" +
				"    RequestTTY auto\n" +
				"    ServerAliveCountMax 5\n",
			want: []Host{
				{Alias: "flags", HostName: "flags", Port: 22, ServerAliveCountMax: 5},
			},
		},
		{
			name: "* ? ! 通配别名跳过",
			in:   "Host real fallback? !negate\n    HostName 10.0.0.9\n",
			want: []Host{{Alias: "real", HostName: "10.0.0.9", Port: 22}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(c.in))
			if err != nil {
				t.Fatalf("Parse 返回错误: %v", err)
			}
			assertHosts(t, got, c.want)
		})
	}
}

func TestParse_TildeAndPlaceholders(t *testing.T) {
	in := "Host tilde\n" +
		"    IdentityFile ~/.ssh/id_ed25519\n" +
		"Host ph\n" +
		"    IdentityFile %d/.ssh/%h_key\n"
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 台主机, got %d: %+v", len(got), got)
	}
	if strings.HasPrefix(got[0].IdentityFile, "~") || !filepath.IsAbs(got[0].IdentityFile) {
		t.Errorf("~ 应展开为绝对路径, got %q", got[0].IdentityFile)
	}
	if got[1].IdentityFile != "%d/.ssh/%h_key" {
		t.Errorf("占位符应原样保留, got %q", got[1].IdentityFile)
	}
}

func TestParse_ProxyCommandNeverExecuted(t *testing.T) {
	// 若把 ProxyCommand 拼进 shell 执行，marker 文件会被创建；解析器必须只记录、不执行。
	marker := filepath.Join(t.TempDir(), "pwned")
	cmd := "echo pwned > " + marker
	got, err := Parse(strings.NewReader("Host pc\n    ProxyCommand " + cmd + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 台主机, got %d", len(got))
	}
	if got[0].ProxyCommand != cmd {
		t.Errorf("ProxyCommand 应逐字记录, got %q, want %q", got[0].ProxyCommand, cmd)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("ProxyCommand 被执行了（marker 已存在）: %v", err)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("读取失败") }

func TestParse_ReaderError(t *testing.T) {
	if _, err := Parse(failReader{}); err == nil {
		t.Error("Reader 报错时 Parse 应返回 error")
	}
}

func TestParseFile_BOMAndEmpty(t *testing.T) {
	dir := t.TempDir()

	bom := filepath.Join(dir, "bom.conf")
	writeConfig(t, bom, "\xEF\xBB\xBFHost fromfile\n    HostName file.example.com\n")
	got, err := ParseFile(bom)
	if err != nil {
		t.Fatal(err)
	}
	assertHosts(t, got, []Host{{Alias: "fromfile", HostName: "file.example.com", Port: 22}})

	empty := filepath.Join(dir, "empty.conf")
	writeConfig(t, empty, "")
	got, err = ParseFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	assertHosts(t, got, nil)
}

func TestParseFile_IncludeRecursiveAndCycle(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	// 根文件一行 Include 带两个路径；a.conf 再递归一层并把自己绕回根文件（成环）。
	writeConfig(t, root, "Host root\n    HostName 10.0.0.1\nInclude a.conf extra.conf\n")
	writeConfig(t, filepath.Join(dir, "a.conf"),
		"Host a\n    HostName 10.0.0.2\n    User alice\nInclude b.conf\nInclude config\n")
	writeConfig(t, filepath.Join(dir, "b.conf"),
		"Host b\n    HostName 10.0.0.3\n    Port 2022\n")
	writeConfig(t, filepath.Join(dir, "extra.conf"),
		"Host extra\n    HostName 10.0.0.4\n")

	got, err := ParseFile(root)
	if err != nil {
		t.Fatal(err)
	}
	// 根文件被回环引用不应导致重复或死循环；Include 内容按位置插进来参与解析。
	assertHosts(t, got, []Host{
		{Alias: "root", HostName: "10.0.0.1", Port: 22},
		{Alias: "a", HostName: "10.0.0.2", User: "alice", Port: 22},
		{Alias: "b", HostName: "10.0.0.3", Port: 2022},
		{Alias: "extra", HostName: "10.0.0.4", Port: 22},
	})
}

func TestParseFile_IncludeAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "abs.conf")
	writeConfig(t, child, "Host abs\n    HostName 10.0.0.7\n")
	root := filepath.Join(dir, "config")
	// 绝对路径写进 Include；Windows 上是 C:\...，反斜杠必须按字面量保留。
	// 再用引号包一层，覆盖「引号内也带反斜杠」这一路。
	writeConfig(t, root, "Include \""+child+"\"\n")
	got, err := ParseFile(root)
	if err != nil {
		t.Fatal(err)
	}
	assertHosts(t, got, []Host{{Alias: "abs", HostName: "10.0.0.7", Port: 22}})
}

func TestParseFile_IncludeDepthLimit(t *testing.T) {
	dir := t.TempDir()
	// c0 -> c1 -> ... -> c7 共 8 层，深度上限 5，只有 c0..c5 应被展开。
	for i := 0; i < 8; i++ {
		content := fmt.Sprintf("Host h%d\n    HostName 10.0.0.%d\n", i, i)
		if i < 7 {
			content += fmt.Sprintf("Include c%d.conf\n", i+1)
		}
		writeConfig(t, filepath.Join(dir, fmt.Sprintf("c%d.conf", i)), content)
	}

	got, err := ParseFile(filepath.Join(dir, "c0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]Host, 0, 6)
	for i := 0; i < 6; i++ {
		want = append(want, Host{
			Alias:    fmt.Sprintf("h%d", i),
			HostName: fmt.Sprintf("10.0.0.%d", i),
			Port:     22,
		})
	}
	assertHosts(t, got, want)
}

func TestParseFile_MissingFile(t *testing.T) {
	got, err := ParseFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err == nil {
		t.Error("文件不存在应返回 error")
	}
	if got != nil {
		t.Errorf("出错时不应返回主机, got %+v", got)
	}
}
