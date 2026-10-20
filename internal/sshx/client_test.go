package sshx

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func okHostKey(string, net.Addr, ssh.PublicKey) error { return nil }

func TestDialConfigNormalizeDefaults(t *testing.T) {
	cfg := DialConfig{Host: "example.com", User: "root", HostKeyCallback: okHostKey}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("端口应补默认值 %d，实际 %d", defaultPort, cfg.Port)
	}
	if cfg.Timeout != defaultTimeout {
		t.Fatalf("超时应补默认值 %v，实际 %v", defaultTimeout, cfg.Timeout)
	}
	if cfg.Keepalive != defaultKeepalive {
		t.Fatalf("保活间隔应补默认值 %v，实际 %v", defaultKeepalive, cfg.Keepalive)
	}
}

func TestDialConfigNormalizeRequiresHostKeyCallback(t *testing.T) {
	// 安全红线：没有主机密钥校验就拒绝连接，绝不静默跳过。
	cfg := DialConfig{Host: "example.com", User: "root"}
	err := cfg.normalize()
	if err == nil {
		t.Fatal("缺少 HostKeyCallback 时应报错")
	}
}

func TestDialConfigNormalizeRequiresHostAndUser(t *testing.T) {
	cfg := DialConfig{HostKeyCallback: okHostKey}
	if err := cfg.normalize(); err == nil {
		t.Fatal("缺少主机地址时应报错")
	}

	cfg = DialConfig{Host: "example.com", HostKeyCallback: okHostKey}
	if err := cfg.normalize(); err == nil {
		t.Fatal("缺少用户名时应报错")
	}
}

func TestDialConfigNormalizeInvalidPort(t *testing.T) {
	cfg := DialConfig{Host: "example.com", User: "root", Port: 99999, HostKeyCallback: okHostKey}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("非法端口应回落默认值，实际 %d", cfg.Port)
	}
}

func TestClassifyHandshakeErrorAuthFailed(t *testing.T) {
	// 这条文本来自真机实测（服务器未开 keyboard-interactive 时的原始报错）。
	err := classifyHandshakeError(errors.New(
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none], no supported methods remain"))
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("应识别为认证失败，实际: %v", err)
	}
}

func TestClassifyHandshakeErrorAlgorithm(t *testing.T) {
	err := classifyHandshakeError(errors.New(
		"ssh: handshake failed: ssh: no common algorithm for host key; client offered: [ssh-ed25519], server offered: [ssh-rsa]"))
	if !errors.Is(err, ErrAlgorithmGone) {
		t.Fatalf("应识别为算法不兼容，实际: %v", err)
	}
}

func TestClassifyHandshakeErrorUnknown(t *testing.T) {
	err := classifyHandshakeError(errors.New("ssh: some unexpected failure"))
	if errors.Is(err, ErrAuthFailed) || errors.Is(err, ErrAlgorithmGone) {
		t.Fatalf("未知错误不应误判为认证/算法问题: %v", err)
	}
	if err == nil {
		t.Fatal("不应返回 nil")
	}
}

func TestApplyLegacyAlgorithmsOff(t *testing.T) {
	cfg := &ssh.ClientConfig{}
	applyLegacyAlgorithms(cfg, false)
	if cfg.HostKeyAlgorithms != nil || cfg.KeyExchanges != nil || cfg.Ciphers != nil {
		t.Fatal("兼容模式关闭时不应改动算法列表")
	}
}

func TestApplyLegacyAlgorithmsOn(t *testing.T) {
	cfg := &ssh.ClientConfig{}
	applyLegacyAlgorithms(cfg, true)

	if !contains(cfg.HostKeyAlgorithms, "ssh-rsa") {
		t.Fatalf("兼容模式应包含 ssh-rsa，实际: %v", cfg.HostKeyAlgorithms)
	}
	// 关键：追加而不是覆盖，默认算法一个都不能丢，否则反而连不上现代服务器。
	if !contains(cfg.HostKeyAlgorithms, "ssh-ed25519") {
		t.Fatalf("兼容模式不应丢失默认算法 ssh-ed25519，实际: %v", cfg.HostKeyAlgorithms)
	}
	if !contains(cfg.KeyExchanges, "diffie-hellman-group14-sha1") {
		t.Fatalf("兼容模式应包含老 KEX，实际: %v", cfg.KeyExchanges)
	}
}

func TestWithExtraNoDuplicate(t *testing.T) {
	out := withExtra([]string{"a", "b"}, "b", "c")
	if len(out) != 3 {
		t.Fatalf("期望去重后 3 项，实际 %v", out)
	}
}

func TestDialUnreachable(t *testing.T) {
	cfg := DialConfig{
		Host:            "127.0.0.1",
		Port:            1, // 必然无人监听
		User:            "nobody",
		Auth:            AuthSpec{Type: AuthPassword, Password: "x"},
		Timeout:         800 * time.Millisecond,
		HostKeyCallback: okHostKey,
	}
	_, err := Dial(context.Background(), cfg)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("应返回 ErrUnreachable，实际: %v", err)
	}
}

func TestDialNoAuthMethod(t *testing.T) {
	cfg := DialConfig{
		Host:            "127.0.0.1",
		Port:            1,
		User:            "nobody",
		Auth:            AuthSpec{Type: AuthPassword},
		Timeout:         200 * time.Millisecond,
		HostKeyCallback: okHostKey,
	}
	// 认证方法缺失应在拨号之前就被拦下，而不是先连再报错。
	_, err := Dial(context.Background(), cfg)
	if !errors.Is(err, ErrNoAuthMethod) {
		t.Fatalf("应返回 ErrNoAuthMethod，实际: %v", err)
	}
}

// TestDialHandshakeTimeout 验证「TCP 连得上但服务器不回握手包」不会挂死。
//
// 这是 ssh.Dial 的经典坑：ClientConfig.Timeout 只管 TCP 建连，管不到握手阶段。
// 本包用连接层 deadline 兜底，这里起一个只 accept 不说话的服务端来验证它真的生效。
func TestDialHandshakeTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法监听本地端口: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// 接受连接但永不发送 SSH 版本号，模拟半死服务 / 被中间设备吞包。
			go func() {
				<-time.After(5 * time.Second)
				conn.Close()
			}()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	cfg := DialConfig{
		Host:            "127.0.0.1",
		Port:            port,
		User:            "nobody",
		Auth:            AuthSpec{Type: AuthPassword, Password: "x"},
		Timeout:         400 * time.Millisecond,
		HostKeyCallback: okHostKey,
	}

	start := time.Now()
	_, err = Dial(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("服务器不回握手包时应失败")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("握手超时未生效，实际耗时 %v（应接近 400ms）", elapsed)
	}
}

func TestClientCloseIdempotent(t *testing.T) {
	// 未连通的 Client 关闭不应 panic（托盘退出时会无条件关一遍所有会话）。
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Fatalf("空 Client 关闭不应报错: %v", err)
	}
	if c.Alive() {
		t.Fatal("关闭后 Alive 应为 false")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
