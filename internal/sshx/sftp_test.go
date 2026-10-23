package sshx

// SFTP 引擎层的测试。
//
// 这里用 pkg/sftp 自带的服务端（sftp.NewServer）在临时目录上跑真实的
// SFTP 子系统，而不是给 SftpSession 打桩。理由：这一层的风险几乎全在
// 协议交互与路径处理上（相对路径、多重斜杠、mkdir -p 语义、递归删除的
// 顺序），假接口一样都覆盖不到。
//
// 测试直接对 internal/sshx 的 SftpSession 下手，不经过服务层：
// 服务层只是一层「取连接 + 转错误码」的壳子，另有覆盖。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sftpNorm 把服务端回的 chroot 绝对路径还原成可与本地路径比对的形态。
//
// pkg/sftp 的服务端带 chroot 语义：客户端给的路径一律当作 root 内的路径，
// 回给客户端时前面统一补一个 "/"。Windows 上 root 自带盘符，于是会出现
// 形如 "/E:/tmp/xxx" 的结果。剥掉这层前导斜杠、统一分隔符即可比对。
func sftpNorm(p string) string {
	return strings.TrimPrefix(strings.ReplaceAll(p, `\`, "/"), "/")
}

// sftpTestServer 一个只提供 SFTP 子系统的最小 SSH 服务端，根是 root 目录。
type sftpTestServer struct {
	host string
	port int
	root string
	ln   net.Listener
}

// startSftpTestServer 起服务端。清理与关闭由 t.Cleanup 负责。
func startSftpTestServer(t *testing.T) *sftpTestServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成服务端密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造 signer 失败: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	s := &sftpTestServer{
		host: "127.0.0.1",
		port: ln.Addr().(*net.TCPAddr).Port,
		root: t.TempDir(),
		ln:   ln,
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveConn(conn, cfg)
		}
	}()

	return s
}

func (s *sftpTestServer) serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		go s.serveSession(newCh)
	}
}

func (s *sftpTestServer) serveSession(newCh ssh.NewChannel) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	for req := range reqs {
		if req.Type != "subsystem" {
			_ = req.Reply(false, nil)
			continue
		}
		name := ""
		if len(req.Payload) >= 4 {
			name = string(req.Payload[4:])
		}
		if name != "sftp" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		// 必须把服务端的工作目录钉在临时目录上：sftp.NewServer 默认服务的是
		// 进程 CWD（也就是源码目录），不加限制的话本测试里的 Remove 会去
		// 动真实文件——尤其是递归删除那条用例。
		server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.root))
		if err != nil {
			return
		}
		// Serve 必须同步堵在这里：一旦本函数返回，channel 就会被关掉，
		// 客户端那边的 SFTP 会话立刻 EOF。
		_ = server.Serve()
		return
	}
}

// newSftpForTest 直连一个 SftpSession，跳过服务层与连接池。
func newSftpForTest(t *testing.T, s *sftpTestServer) *SftpSession {
	t.Helper()

	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(s.host, strconv.Itoa(s.port)), cfg)
	if err != nil {
		t.Fatalf("连接测试服务端失败: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	sess, err := NewSftpSession(conn)
	if err != nil {
		t.Fatalf("打开 SFTP 失败: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// TestSftpCleanRemotePath 路径归一化是后面所有操作的前提，先锁住。
func TestSftpCleanRemotePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"/", "/"},
		{"/var/log", "/var/log"},
		{"/var/log/", "/var/log"},
		{"//var///log", "/var/log"},
		{"/var/log/../log", "/var/log"},
		{"/../etc", "/etc"}, // 越界到根以上要被压回根
		{"/var/./log", "/var/log"},
		{"~", ""}, // 单独波浪号 = 家目录，由 RealPath 解析
		{"~/downloads", "downloads"},
		{"logs", "logs"},
		{"./logs", "logs"},
	}
	for _, c := range cases {
		if got := CleanRemotePath(c.in); got != c.want {
			t.Errorf("CleanRemotePath(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestSftpParentOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/", "/"},
		{"/var", "/"},
		{"/var/log", "/var"},
		{"/var/log/", "/var"},
	}
	for _, c := range cases {
		if got := parentOf(c.in); got != c.want {
			t.Errorf("parentOf(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestSftpListAndNavigate 覆盖列举、排序与目录进出。
func TestSftpListAndNavigate(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	// 造一棵固定的树：两个目录 + 两个文件。
	mustWrite(t, filepath.Join(srv.root, "b.log"), "bbb")
	mustWrite(t, filepath.Join(srv.root, "a.txt"), "aaa")
	if err := os.MkdirAll(filepath.Join(srv.root, "zeta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srv.root, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := sess.List("")
	if err != nil {
		t.Fatalf("列举家目录失败: %v", err)
	}
	// 服务端回的是 chroot 视角下的绝对路径，Windows 上形如 "/E:/tmp/xxx/001"。
	// 去掉前导斜杠再比，省得被盘符和分隔符绊住。
	if sftpNorm(res.Path) != sftpNorm(srv.root) {
		t.Fatalf("列举路径不对: %q，期望 %q", res.Path, srv.root)
	}

	// 目录优先，其次字典序：alpha, zeta, a.txt, b.log
	got := make([]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		got = append(got, e.Name)
	}
	want := []string{"alpha", "zeta", "a.txt", "b.log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("排序不对: %v，期望 %v", got, want)
	}

	// 目录条目的字段要齐：前端靠 isDir 决定双击是进入还是选中。
	dir := res.Entries[0]
	if !dir.IsDir || dir.Size == 0 && dir.Ext != "" {
		t.Fatalf("目录条目字段异常: %+v", dir)
	}
	if sftpNorm(dir.Path) != sftpNorm(filepath.Join(srv.root, "alpha")) {
		t.Fatalf("目录绝对路径不对: %q", dir.Path)
	}

	// 文件条目带扩展名，前端据此判断能不能文本预览。
	file := res.Entries[2]
	if file.IsDir || file.Ext != "txt" {
		t.Fatalf("文件条目字段异常: %+v", file)
	}

	// 进子目录。
	sub, err := sess.List("alpha")
	if err != nil {
		t.Fatalf("进入子目录失败: %v", err)
	}
	if sftpNorm(sub.Path) != sftpNorm(filepath.Join(srv.root, "alpha")) {
		t.Fatalf("子目录路径不对: %q", sub.Path)
	}
	if sub.Parent != res.Path {
		t.Fatalf("子目录的 parent 不对: %q，期望 %q", sub.Parent, res.Path)
	}
	if len(sub.Entries) != 0 {
		t.Fatalf("新目录应为空，实际 %d 项", len(sub.Entries))
	}
}

func TestSftpMkdirCreatesIntermediates(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	// 一次建三层，中间两层都不存在——这才是 MkdirAll 该有的样子。
	if err := sess.Mkdir("one/two/three"); err != nil {
		t.Fatalf("建多层目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.root, "one", "two", "three")); err != nil {
		t.Fatalf("三层目录没建出来: %v", err)
	}

	// 已存在时不报错（幂等）——用户连点两次「新建」很常见。
	if err := sess.Mkdir("one/two/three"); err != nil {
		t.Fatalf("重复建同名目录不该报错: %v", err)
	}

	// 空路径要被挡住，而不是被当成「家目录」建出一个奇怪的目录。
	if err := sess.Mkdir("   "); !errors.Is(err, ErrInvalidRemotePath) {
		t.Fatalf("空目录名应返回 ErrInvalidRemotePath，实际 %v", err)
	}
}

func TestSftpRemoveRefusesNonEmptyDir(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	dir := filepath.Join(srv.root, "keep")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "inner.txt"), "x")

	// 非空目录 + recursive=false 必须拒绝，绝不能静默递归。
	err := sess.Remove("keep", false)
	if err == nil {
		t.Fatal("非空目录在非递归模式下不该被删掉")
	}
	if !errors.Is(err, ErrRemoteNotEmpty) {
		t.Fatalf("错误类型不对，期望 ErrRemoteNotEmpty: %v", err)
	}
	if _, serr := os.Stat(dir); serr != nil {
		t.Fatalf("目录被误删了: %v", serr)
	}

	// 显式要求递归才删。
	if err := sess.Remove("keep", true); err != nil {
		t.Fatalf("递归删除失败: %v", err)
	}
	if _, serr := os.Stat(dir); serr == nil {
		t.Fatal("递归删除后目录还在")
	}
}

// TestSftpIsRootPath 锁住根目录判定。
//
// 这里测纯函数而不是真去 Remove("/")：测试用的 SFTP 服务端是 chroot 语义，
// RealPath("/") 会被重写成带盘符的形态，防护分支根本不会命中，
// 于是遍历会真的扫过开发者整个 E: 盘——那比漏测危险得多。
func TestSftpIsRootPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"//", true},
		{"/.", true},
		{"/var/", false},
		{"/var", false},
		{"", false},
		{".", false},
	}
	for _, c := range cases {
		if got := isRootPath(c.in); got != c.want {
			t.Errorf("isRootPath(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestSftpRemoveFile(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	mustWrite(t, filepath.Join(srv.root, "gone.txt"), "x")
	if err := sess.Remove("gone.txt", false); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(srv.root, "gone.txt")); serr == nil {
		t.Fatal("文件还在")
	}
}

func TestSftpRename(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	mustWrite(t, filepath.Join(srv.root, "before.txt"), "x")

	// 相对新名：按源文件所在目录解释，与 shell 的 mv 一致。
	if err := sess.Rename("before.txt", "after.txt"); err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(srv.root, "after.txt")); serr != nil {
		t.Fatalf("新文件不存在: %v", serr)
	}

	// 再改一次：确认连续重命名都生效。
	if err := sess.Rename("after.txt", "final.txt"); err != nil {
		t.Fatalf("二次重命名失败: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(srv.root, "final.txt")); serr != nil {
		t.Fatalf("最终文件不存在: %v", serr)
	}
}

func TestSftpReadWriteText(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	if err := sess.WriteText("conf.txt", "hello=world\n", 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	text, err := sess.ReadText("conf.txt", 0)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if text != "hello=world\n" {
		t.Fatalf("读回来的内容不对: %q", text)
	}

	// 权限位只在 Unix 上有意义：Windows 的 NTFS 不存 Unix mode，
	// Go 拿不到、pkg/sftp 也写不进去，断言会恒失败。
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(srv.root, "conf.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("权限不对: %v", info.Mode().Perm())
		}
	}

	// limit 生效：超长文件只取前若干字节。
	mustWrite(t, filepath.Join(srv.root, "big.log"), strings.Repeat("x", 5000))
	part, err := sess.ReadText("big.log", 100)
	if err != nil {
		t.Fatalf("限量读取失败: %v", err)
	}
	if len(part) != 100 {
		t.Fatalf("限量读取长度不对: %d", len(part))
	}

	// 目录不能当文件读。
	if _, err := sess.ReadText(".", 10); !errors.Is(err, ErrRemoteIsDir) {
		t.Fatalf("期望 ErrRemoteIsDir，实际 %v", err)
	}
}

func TestSftpStatMissingPath(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	_, err := sess.Stat("nope-does-not-exist")
	if !errors.Is(err, ErrRemoteNotExist) {
		t.Fatalf("期望 ErrRemoteNotExist，实际 %v", err)
	}
}

// TestSftpDownloadAndUpload 覆盖本地 ↔ 远端的两个方向。
func TestSftpDownloadAndUpload(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	mustWrite(t, filepath.Join(srv.root, "payload.txt"), "download-me")
	// 目录里放一个文件，用来验证「下载目录」是递归的。
	if err := os.MkdirAll(filepath.Join(srv.root, "bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(srv.root, "bundle", "inner.txt"), "inner")

	// 下载单个文件到指定完整路径。
	target := filepath.Join(t.TempDir(), "out.txt")
	if err := sess.Download(context.Background(), "payload.txt", target, nil); err != nil {
		t.Fatalf("下载文件失败: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "download-me" {
		t.Fatalf("下载内容不对: %q, err=%v", string(b), err)
	}

	// 下载到目录：按源文件名落盘。
	dir := t.TempDir()
	if err := sess.Download(context.Background(), "payload.txt", dir, nil); err != nil {
		t.Fatalf("下载到目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "payload.txt")); err != nil {
		t.Fatalf("目录下载没落盘: %v", err)
	}

	// 下载整个目录。
	dirRoot := t.TempDir()
	if err := sess.Download(context.Background(), "bundle", dirRoot, nil); err != nil {
		t.Fatalf("下载目录失败: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dirRoot, "inner.txt")); err != nil || string(b) != "inner" {
		t.Fatalf("目录下载内容不对: %q, err=%v", string(b), err)
	}

	// 上传：本地文件 → 远端目录。
	local := filepath.Join(t.TempDir(), "up.txt")
	mustWrite(t, local, "upload-me")
	// 目标目录传空 = 家目录（chroot 下就是 root）。
	// 这里不能传 srv.root 的绝对路径：服务端对绝对路径不套 workDir，
	// 会真的写到盘符根目录去。
	if err := sess.Upload(context.Background(), local, "", nil); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(srv.root, "up.txt")); err != nil || string(b) != "upload-me" {
		t.Fatalf("上传内容不对: %q, err=%v", string(b), err)
	}
}

func TestSftpUploadMissingLocalFile(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	if err := sess.Upload(context.Background(), filepath.Join(t.TempDir(), "missing.bin"), srv.root, nil); err == nil {
		t.Fatal("本地文件不存在时应报错")
	}
}

// 上传目录：嵌套子目录与文件都要落到 <目标目录>/<本地目录名>/ 下。
func TestSftpUploadDir(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	src := filepath.Join(t.TempDir(), "bundle")
	mustWrite(t, filepath.Join(src, "top.txt"), "top")
	mustWrite(t, filepath.Join(src, "sub", "deep.txt"), "deep")

	// 目标目录传空 = 家目录（chroot 下就是 root），与服务端对绝对路径的处理有关。
	if err := sess.Upload(context.Background(), src, "", nil); err != nil {
		t.Fatalf("上传目录失败: %v", err)
	}
	for rel, want := range map[string]string{
		"bundle/top.txt":      "top",
		"bundle/sub/deep.txt": "deep",
	} {
		b, err := os.ReadFile(filepath.Join(srv.root, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Fatalf("%s 内容不对: %q, err=%v", rel, string(b), err)
		}
	}
}

// 取消：ctx 已取消时复制必须立刻停，且返回的必须是 context.Canceled
// （服务层靠 errors.Is 认它，包一层就会变成「下载失败」的红色报错）。
func TestSftpTransferCanceled(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	mustWrite(t, filepath.Join(srv.root, "payload.txt"), "download-me")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	target := filepath.Join(t.TempDir(), "out.txt")
	err := sess.Download(ctx, "payload.txt", target, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应返回 context.Canceled，实际: %v", err)
	}
}

// 改权限：644 → 600，再读一次确认服务端真的改了。
func TestSftpChmod(t *testing.T) {
	srv := startSftpTestServer(t)
	sess := newSftpForTest(t, srv)

	mustWrite(t, filepath.Join(srv.root, "payload.txt"), "download-me")

	if err := sess.Chmod("payload.txt", 0o600); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	fi, err := os.Stat(filepath.Join(srv.root, "payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限没改成功: %v", fi.Mode().Perm())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
