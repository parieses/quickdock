package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// memStore 内存实现，替代 #6 的数据库存储。
type memStore struct {
	mu    sync.Mutex
	hosts map[string][]KnownHost
	// listErr / saveErr 用于注入故障，验证「存储坏了必须拒连」。
	listErr error
	saveErr error
	deleted int
}

func newMemStore() *memStore {
	return &memStore{hosts: make(map[string][]KnownHost)}
}

func (m *memStore) ListKnownHosts(host string, port int) ([]KnownHost, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	src := m.hosts[targetKey(host, port)]
	out := make([]KnownHost, len(src))
	copy(out, src)
	return out, nil
}

func (m *memStore) SaveKnownHost(kh KnownHost) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	k := targetKey(kh.Host, kh.Port)
	list := m.hosts[k]
	for i := range list {
		if list[i].KeyType == kh.KeyType {
			list[i] = kh
			m.hosts[k] = list
			return nil
		}
	}
	m.hosts[k] = append(list, kh)
	return nil
}

func (m *memStore) DeleteKnownHosts(host string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted++
	delete(m.hosts, targetKey(host, port))
	return nil
}

// genEd25519 生成一把 ed25519 主机密钥。
func genEd25519(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 ed25519 失败: %v", err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("转换公钥失败: %v", err)
	}
	return k
}

// genRSA 生成一把 rsa 主机密钥（用于「不同算法」分支）。
func genRSA(t *testing.T) ssh.PublicKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 rsa 失败: %v", err)
	}
	k, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("转换公钥失败: %v", err)
	}
	return k
}

var sha256Fingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// --- 首次连接 ---------------------------------------------------------------

func TestVerifyUnknownHostKey(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)
	key := genEd25519(t)

	err := k.Verify("10.0.0.5", 22, key)
	if !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("首次连接应返回 ErrUnknownHostKey，实际: %v", err)
	}

	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("应能取出 *UnknownHostKeyError，实际: %T", err)
	}
	if want := ssh.FingerprintSHA256(key); uk.Fingerprint != want {
		t.Fatalf("指纹应为 %s，实际 %s", want, uk.Fingerprint)
	}
	if !sha256Fingerprint.MatchString(uk.Fingerprint) {
		t.Fatalf("指纹格式应为 SHA256:xxx，实际 %q", uk.Fingerprint)
	}
	if uk.KeyType != "ssh-ed25519" {
		t.Fatalf("密钥类型应为 ssh-ed25519，实际 %q", uk.KeyType)
	}
	if len(uk.KnownTypes) != 0 {
		t.Fatalf("首连不应带已记录类型，实际 %v", uk.KnownTypes)
	}

	// 必须同时留下 pending，否则用户确认后无从落库。
	obs, ok := k.Pending("10.0.0.5", 22)
	if !ok {
		t.Fatal("首连失败后应记录待确认密钥")
	}
	if obs.Fingerprint != uk.Fingerprint {
		t.Fatal("pending 指纹应与报错内容一致")
	}
}

func TestVerifyNewKeyTypeIsNotChange(t *testing.T) {
	// 同主机已有 ed25519，这次协商到 rsa：这是另一把密钥，不是「指纹变了」。
	// 判成变更会天天误报（打开兼容模式后必然发生），判成首连才符合密码学事实。
	store := newMemStore()
	store.SaveKnownHost(KnownHost{Host: "h", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "SHA256:aaa"})
	k := NewKnownHosts(store)

	err := k.Verify("h", 22, genRSA(t))
	if errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("新算法不应判为指纹变更，实际: %v", err)
	}
	if !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("新算法应按首连处理，实际: %v", err)
	}
	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("应能取出详情，实际 %T", err)
	}
	if len(uk.KnownTypes) != 1 || uk.KnownTypes[0] != "ssh-ed25519" {
		t.Fatalf("应带上已记录类型供 UI 提示，实际 %v", uk.KnownTypes)
	}
}

// --- 已信任 -----------------------------------------------------------------

func TestVerifyTrustedMatch(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)
	key := genEd25519(t)
	store.SaveKnownHost(KnownHost{Host: "10.0.0.5", Port: 22, KeyType: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)})

	if err := k.Verify("10.0.0.5", 22, key); err != nil {
		t.Fatalf("指纹一致时应放行，实际: %v", err)
	}
}

func TestVerifyMultipleKeyTypes(t *testing.T) {
	// 多种算法并存时，命中任意一条就要放行。
	store := newMemStore()
	k := NewKnownHosts(store)
	ed := genEd25519(t)
	rsaKey := genRSA(t)
	store.SaveKnownHost(KnownHost{Host: "h", Port: 22, KeyType: ed.Type(), Fingerprint: ssh.FingerprintSHA256(ed)})
	store.SaveKnownHost(KnownHost{Host: "h", Port: 22, KeyType: rsaKey.Type(), Fingerprint: ssh.FingerprintSHA256(rsaKey)})

	for _, key := range []ssh.PublicKey{ed, rsaKey} {
		if err := k.Verify("h", 22, key); err != nil {
			t.Fatalf("已信任的 %s 应放行，实际: %v", key.Type(), err)
		}
	}
}

func TestVerifyHostKeyChanged(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)
	oldKey := genEd25519(t)
	newKey := genEd25519(t) // 同类型、不同密钥
	store.SaveKnownHost(KnownHost{Host: "h", Port: 2222, KeyType: oldKey.Type(), Fingerprint: ssh.FingerprintSHA256(oldKey)})

	err := k.Verify("h", 2222, newKey)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("同类型指纹不一致应报变更，实际: %v", err)
	}
	var hc *HostKeyChangedError
	if !errors.As(err, &hc) {
		t.Fatalf("应能取出 *HostKeyChangedError，实际 %T", err)
	}
	if hc.OldFingerprint != ssh.FingerprintSHA256(oldKey) {
		t.Fatal("应带上旧指纹供用户比对")
	}
	if hc.NewFingerprint != ssh.FingerprintSHA256(newKey) {
		t.Fatal("应带上新指纹供用户比对")
	}
	if hc.Port != 2222 {
		t.Fatalf("端口应透传，实际 %d", hc.Port)
	}

	// 变更同样要留 pending：用户确认「接受新密钥」后可覆盖旧记录。
	if obs, ok := k.Pending("h", 2222); !ok || obs.Fingerprint != hc.NewFingerprint {
		t.Fatal("变更后应记录新指纹为待确认")
	}
}

// --- 信任落库 ---------------------------------------------------------------

func TestTrustFlow(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)
	key := genEd25519(t)

	if err := k.Verify("h", 22, key); err == nil {
		t.Fatal("首连应报错")
	}
	obs, _ := k.Pending("h", 22)

	rec, err := k.Trust("h", 22, obs.Fingerprint)
	if err != nil {
		t.Fatalf("信任应成功，实际: %v", err)
	}
	if rec.KeyType != key.Type() || rec.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("落库内容不对: %+v", rec)
	}
	if rec.AddedAt == 0 {
		t.Fatal("应记录添加时间")
	}

	if err := k.Verify("h", 22, key); err != nil {
		t.Fatalf("信任后应放行，实际: %v", err)
	}
	if _, ok := k.Pending("h", 22); ok {
		t.Fatal("信任后应清掉 pending，避免重复弹窗")
	}
}

func TestTrustRejectsWrongFingerprint(t *testing.T) {
	// 前端传了一把「没见过的指纹」——说明状态错乱或被注入，必须拒绝。
	store := newMemStore()
	k := NewKnownHosts(store)
	k.Verify("h", 22, genEd25519(t))

	_, err := k.Trust("h", 22, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("指纹不一致应拒绝，实际: %v", err)
	}
	if len(store.hosts) != 0 {
		t.Fatal("被拒绝的信任不应写入存储")
	}
}

func TestTrustRequiresPending(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)

	_, err := k.Trust("h", 22, "SHA256:whatever")
	if !errors.Is(err, ErrNoPendingHostKey) {
		t.Fatalf("没有 pending 时应拒绝，实际: %v", err)
	}
}

func TestTrustRejectsEmptyFingerprint(t *testing.T) {
	k := NewKnownHosts(newMemStore())
	k.Verify("h", 22, genEd25519(t))

	if _, err := k.Trust("h", 22, "   "); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("空指纹应拒绝，实际: %v", err)
	}
}

func TestTrustSurfacesSaveError(t *testing.T) {
	store := newMemStore()
	store.saveErr = errors.New("disk full")
	k := NewKnownHosts(store)
	k.Verify("h", 22, genEd25519(t))
	obs, _ := k.Pending("h", 22)

	if _, err := k.Trust("h", 22, obs.Fingerprint); err == nil {
		t.Fatal("存储写入失败时不应报告信任成功")
	}
	// pending 保留，用户重试仍能落库。
	if _, ok := k.Pending("h", 22); !ok {
		t.Fatal("写入失败不应丢掉 pending")
	}
}

// --- 失败关闭 ---------------------------------------------------------------

func TestVerifyFailsClosedOnStoreError(t *testing.T) {
	store := newMemStore()
	store.listErr = errors.New("db is locked")
	k := NewKnownHosts(store)

	err := k.Verify("h", 22, genEd25519(t))
	if err == nil {
		t.Fatal("读不到记录时必须拒连")
	}
	if errors.Is(err, ErrUnknownHostKey) || errors.Is(err, ErrHostKeyChanged) {
		t.Fatal("存储故障不能被伪装成「待确认」，否则用户一确认就把未知密钥写进白名单")
	}
}

func TestVerifyNilStoreFailsClosed(t *testing.T) {
	if err := NewKnownHosts(nil).Verify("h", 22, genEd25519(t)); !errors.Is(err, ErrKnownHostStoreMissing) {
		t.Fatalf("未配置存储应拒连，实际: %v", err)
	}
	// nil 接收者也不能 panic（服务层退出路径可能没初始化）。
	var nilKH *KnownHosts
	if err := nilKH.Verify("h", 22, genEd25519(t)); !errors.Is(err, ErrKnownHostStoreMissing) {
		t.Fatalf("nil 校验器应拒连，实际: %v", err)
	}
}

func TestVerifyNilKey(t *testing.T) {
	k := NewKnownHosts(newMemStore())
	if err := k.Verify("h", 22, nil); err == nil {
		t.Fatal("无主机密钥应拒连")
	}
}

// --- 规范化与清理 -----------------------------------------------------------

func TestNormalizeTarget(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
		wp   int
	}{
		{" Example.COM ", 22, "example.com", 22},
		{"10.0.0.5", 0, "10.0.0.5", defaultPort},
		{"[::1]", 2202, "::1", 2202},
		{"h", 70000, "h", defaultPort},
	}
	for _, c := range cases {
		h, p := normalizeTarget(c.host, c.port)
		if h != c.want || p != c.wp {
			t.Fatalf("normalizeTarget(%q,%d) = %q,%d，期望 %q,%d", c.host, c.port, h, p, c.want, c.wp)
		}
	}
}

func TestForgetClearsPending(t *testing.T) {
	k := NewKnownHosts(newMemStore())
	k.Verify("h", 22, genEd25519(t))
	k.Forget("h", 22)
	if _, ok := k.Pending("h", 22); ok {
		t.Fatal("Forget 后不应还有 pending")
	}
}

func TestRemoveAll(t *testing.T) {
	store := newMemStore()
	k := NewKnownHosts(store)
	key := genEd25519(t)
	store.SaveKnownHost(KnownHost{Host: "h", Port: 22, KeyType: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)})

	if err := k.RemoveAll("h", 22); err != nil {
		t.Fatalf("清除应成功，实际: %v", err)
	}
	if store.deleted != 1 {
		t.Fatal("应调用存储删除")
	}
	// 清除后回到「待确认」状态，而不是静默放行。
	if err := k.Verify("h", 22, key); !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("清除后应重新要求确认，实际: %v", err)
	}
}

// --- 与真实握手串联 ---------------------------------------------------------

// startTestSSHServer 起一个最小可用的 SSH 服务端（ed25519 主机密钥 + 密码认证）。
func startTestSSHServer(t *testing.T) (host string, port int, hostKey ssh.PublicKey) {
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
				// 只做握手，不开通道。
				for newCh := range chans {
					newCh.Reject(ssh.Prohibited, "none")
				}
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, signer.PublicKey()
}

func dialWith(t *testing.T, host string, port int, cb ssh.HostKeyCallback) (*Client, error) {
	t.Helper()
	return Dial(context.Background(), DialConfig{
		Host:            host,
		Port:            port,
		User:            "root",
		Auth:            AuthSpec{Type: AuthPassword, Password: "pw"},
		Timeout:         5 * time.Second,
		HostKeyCallback: cb,
	})
}

// TestUnknownHostKeySurvivesHandshake 是本任务最关键的用例：
// 断言带类型的主机密钥错误能穿过 ssh.NewClientConn 的 %w 包装一路传到这里。
// 如果上游把 %w 改成 %v，这个测试会立刻失败，而不是等到用户看到一条无信息的报错才发现。
func TestUnknownHostKeySurvivesHandshake(t *testing.T) {
	host, port, srvKey := startTestSSHServer(t)
	k := NewKnownHosts(newMemStore())

	c, err := dialWith(t, host, port, k.Callback(host, port))
	if err == nil {
		c.Close()
		t.Fatal("未信任的主机不应连接成功")
	}
	if !errors.Is(err, ErrUnknownHostKey) {
		t.Fatalf("应返回 ErrUnknownHostKey，实际: %v", err)
	}
	var uk *UnknownHostKeyError
	if !errors.As(err, &uk) {
		t.Fatalf("错误链里应保留 *UnknownHostKeyError，实际: %v", err)
	}
	if want := ssh.FingerprintSHA256(srvKey); uk.Fingerprint != want {
		t.Fatalf("指纹应为 %s，实际 %s", want, uk.Fingerprint)
	}

	// 用户点「信任」后重连必须直接成功。
	if _, err := k.Trust(host, port, uk.Fingerprint); err != nil {
		t.Fatalf("信任失败: %v", err)
	}
	c, err = dialWith(t, host, port, k.Callback(host, port))
	if err != nil {
		t.Fatalf("信任后应连接成功，实际: %v", err)
	}
	defer c.Close()
	if c.Info().Fingerprint != uk.Fingerprint {
		t.Fatalf("握手信息里的指纹应为 %s，实际 %s", uk.Fingerprint, c.Info().Fingerprint)
	}
	if !c.Alive() {
		t.Fatal("连接应处于可用状态")
	}
}

func TestHostKeyChangedSurvivesHandshake(t *testing.T) {
	host, port, _ := startTestSSHServer(t)
	store := newMemStore()
	k := NewKnownHosts(store)

	// 预置一条同类型但指纹不同的记录，模拟「服务器换了密钥」。
	store.SaveKnownHost(KnownHost{
		Host: host, Port: port, KeyType: "ssh-ed25519",
		Fingerprint: ssh.FingerprintSHA256(genEd25519(t)),
	})

	c, err := dialWith(t, host, port, k.Callback(host, port))
	if err == nil {
		c.Close()
		t.Fatal("指纹变更时不应连接成功")
	}
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("应返回 ErrHostKeyChanged，实际: %v", err)
	}
	var hc *HostKeyChangedError
	if !errors.As(err, &hc) {
		t.Fatalf("错误链里应保留 *HostKeyChangedError，实际: %v", err)
	}
	if hc.OldFingerprint == hc.NewFingerprint {
		t.Fatal("新旧指纹应不同")
	}
}
