package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startExecSSHServer 起一个支持 exec 的最小 SSH 服务端。
// 命令是假的，只按固定规则回输出与退出码，用来验证客户端行为。
func startExecSSHServer(t *testing.T) (host string, port int) {
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

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						newCh.Reject(ssh.UnknownChannelType, "only session")
						continue
					}
					go serveExecChannel(newCh)
				}
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func serveExecChannel(newCh ssh.NewChannel) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	go func() {
		defer ch.Close()
		for req := range reqs {
			if req.Type != "exec" || len(req.Payload) < 4 {
				req.Reply(false, nil)
				continue
			}
			n := binary.BigEndian.Uint32(req.Payload[:4])
			if int(n) > len(req.Payload)-4 {
				req.Reply(false, nil)
				continue
			}
			cmd := string(req.Payload[4 : 4+n])
			req.Reply(true, nil)
			runFakeCommand(cmd, ch)
			return // defer ch.Close 结束通道
		}
		// 没有 exec 请求就关掉，让客户端拿到 ExitMissingError。
	}()
}

func runFakeCommand(cmd string, ch ssh.Channel) {
	switch {
	case strings.HasPrefix(cmd, "echo "):
		ch.Write([]byte(strings.TrimPrefix(cmd, "echo ") + "\n"))
		sendExitStatus(ch, 0)
	case cmd == "both":
		ch.Write([]byte("out-line\n"))
		ch.Stderr().Write([]byte("err-line\n"))
		sendExitStatus(ch, 0)
	case cmd == "fail":
		ch.Stderr().Write([]byte("boom\n"))
		sendExitStatus(ch, 7)
	case cmd == "slow":
		// 模拟卡住的命令，等客户端超时关通道。
		time.Sleep(5 * time.Second)
	case cmd == "big":
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for i := 0; i < 64; i++ { // 2 MiB，超过客户端 1 MiB 上限
			if _, err := ch.Write(chunk); err != nil {
				return
			}
		}
		sendExitStatus(ch, 0)
	case cmd == "noexit":
		ch.Write([]byte("bye\n"))
		// 刻意不发 exit-status
	default:
		sendExitStatus(ch, 127)
	}
}

func sendExitStatus(ch ssh.Channel, code int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(code))
	ch.SendRequest("exit-status", false, payload)
}

func dialExec(t *testing.T, host string, port int) *Client {
	t.Helper()
	c, err := dialWith(t, host, port, okHostKey)
	if err != nil {
		t.Fatalf("连接测试服务端失败: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestExecSuccess(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	res, err := c.Exec(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if res.Stdout != "hello\n" {
		t.Fatalf("stdout 不对: %q", res.Stdout)
	}
	if res.Stderr != "" {
		t.Fatalf("stderr 应为空: %q", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("退出码应为 0，实际 %d", res.ExitCode)
	}
	// 命令可能在 1ms 内结束，Windows 时钟粒度下 Duration 允许为 0，
	// 只做区间断言（不能为负 / 不能异常大）。
	if res.Duration < 0 || res.Duration > 5*time.Second {
		t.Fatalf("耗时异常: %v", res.Duration)
	}
	if res.Truncated {
		t.Fatal("小输出不应标记截断")
	}
}

// TestExecNonZeroExitIsNotError：非零退出码是命令的正常结果（grep 没匹配也会返回 1），
// 不能当成传输错误，否则调用方只能靠解析错误字符串才能知道退出码。
func TestExecNonZeroExitIsNotError(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	res, err := c.Exec(context.Background(), "fail")
	if err != nil {
		t.Fatalf("非零退出码不应返回 error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("应透传退出码 7，实际 %d", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Fatalf("应带回 stderr，实际 %q", res.Stderr)
	}
	if res.Stdout != "" {
		t.Fatalf("stdout 应为空，实际 %q", res.Stdout)
	}
}

func TestExecSeparatesStreams(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	res, err := c.Exec(context.Background(), "both")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(res.Stdout, "out-line") {
		t.Fatalf("stdout 缺失: %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, "err-line") {
		t.Fatal("stderr 不应混进 stdout")
	}
	if !strings.Contains(res.Stderr, "err-line") {
		t.Fatalf("stderr 缺失: %q", res.Stderr)
	}
	if !strings.Contains(res.Combined(), "out-line") || !strings.Contains(res.Combined(), "err-line") {
		t.Fatalf("Combined 应同时包含两路输出: %q", res.Combined())
	}
}

func TestExecTimeout(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Exec(ctx, "slow")
	elapsed := time.Since(start)

	if !errors.Is(err, ErrExecTimeout) {
		t.Fatalf("应返回 ErrExecTimeout，实际: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
	}
	// 超时后连接本身不应被误关（同一 Client 还能继续用）。
	if !c.Alive() {
		t.Fatal("单条命令超时不应连带关闭连接")
	}
	if _, err := c.Exec(context.Background(), "echo again"); err != nil {
		t.Fatalf("超时后连接应仍可执行新命令: %v", err)
	}
}

func TestExecCanceledContext(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Exec(ctx, "echo hi"); !errors.Is(err, ErrExecTimeout) {
		t.Fatalf("已取消的 ctx 应直接返回超时错误，实际: %v", err)
	}
}

func TestExecTruncatesLargeOutput(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	res, err := c.Exec(context.Background(), "big")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(res.Stdout) != maxExecOutput {
		t.Fatalf("输出应被截到 %d 字节，实际 %d", maxExecOutput, len(res.Stdout))
	}
	if !res.Truncated {
		t.Fatal("应标记已截断")
	}
	if res.ExitCode != 0 {
		t.Fatalf("截断不应影响退出码，实际 %d", res.ExitCode)
	}
}

func TestExecMissingExitStatus(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	res, err := c.Exec(context.Background(), "noexit")
	if !errors.Is(err, ErrExecAborted) {
		t.Fatalf("无退出状态应返回 ErrExecAborted，实际: %v", err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("未知退出码应为 -1，实际 %d", res.ExitCode)
	}
	if res.Stdout != "bye\n" {
		t.Fatalf("中断前的输出应保留，实际 %q", res.Stdout)
	}
}

func TestExecEmptyCommand(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)

	if _, err := c.Exec(context.Background(), "   "); !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("空命令应报 ErrEmptyCommand，实际: %v", err)
	}
}

func TestExecOnClosedClient(t *testing.T) {
	host, port := startExecSSHServer(t)
	c := dialExec(t, host, port)
	c.Close()

	if _, err := c.Exec(context.Background(), "echo hi"); !errors.Is(err, ErrClosed) {
		t.Fatalf("已关闭的连接应返回 ErrClosed，实际: %v", err)
	}
}

// TestLimitedBuffer 单独守上限逻辑：超限后必须返回 len(p)，
// 否则 ssh 的拷贝循环会把短写当错误，整条流直接断掉。
func TestLimitedBuffer(t *testing.T) {
	var b limitedBuffer
	b.limit = 10

	n, err := b.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if n != 16 {
		t.Fatalf("超限也应报告完整写入长度，实际 %d", n)
	}
	if b.String() != "0123456789" {
		t.Fatalf("应只保留前 10 字节，实际 %q", b.String())
	}
	if !b.truncated {
		t.Fatal("应标记截断")
	}
	// 后续写入继续吞掉，不能让上游觉得写失败。
	if n, err := b.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("超限后写入应被吞掉，实际 n=%d err=%v", n, err)
	}

	var unlimited limitedBuffer
	unlimited.limit = 0
	if n, _ := unlimited.Write([]byte("abc")); n != 3 || unlimited.truncated {
		t.Fatal("limit=0 应视为不保留内容且不标记截断")
	}
}
