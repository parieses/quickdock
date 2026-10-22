// Package sshx 是 SSH 远程连接引擎：连接建立、认证、会话与主机指纹校验。
//
// 本包不直接面向前端，所有对外能力经 services/sshx 门面暴露。
// 约定（与仓库其它 internal 包一致）：
//   - 不引 syscall，保持跨平台可编译；
//   - 不依赖 Wails 运行时，可被单元测试直接驱动；
//   - 所有错误都收敛成 Sentinel，便于门面层映射成前端可识别的错误码。
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// 错误分类。
//
// x/crypto/ssh 没有为握手失败提供类型化错误（只返回 fmt.Errorf 的文本），
// 所以「认证失败」与「算法不兼容」只能靠错误文本区分。这是本包唯一依赖
// 错误字符串的地方，集中在 classifyHandshakeError 里，方便日后官方补类型时替换。
var (
	ErrUnreachable   = errors.New("sshx: 无法连接到服务器")
	ErrAuthFailed    = errors.New("sshx: 认证失败")
	ErrAlgorithmGone = errors.New("sshx: 客户端与服务器没有共同支持的算法")
	ErrNoAuthMethod  = errors.New("sshx: 没有可用的认证方式")
	ErrClosed        = errors.New("sshx: 连接已关闭")
)

const (
	defaultTimeout   = 15 * time.Second
	defaultKeepalive = 45 * time.Second
	defaultPort      = 22
	keepaliveRequest = "keepalive@openssh.com"
)

// HandshakeInfo 握手结果，用于 UI 展示与排障。
type HandshakeInfo struct {
	ServerVersion    string        // 如 SSH-2.0-OpenSSH_7.4
	HostKeyAlgorithm string        // 如 ecdsa-sha2-nistp256
	Fingerprint      string        // SHA256:xxx
	Duration         time.Duration // TCP 建连 + 握手 + 认证的总耗时
}

// DialConfig 建立连接所需的全部参数。
type DialConfig struct {
	Host string
	Port int
	User string
	Auth AuthSpec

	Timeout   time.Duration // TCP 建连与握手各自的超时，默认 15s
	Keepalive time.Duration // 保活间隔，默认 45s（NAT/防火墙通常 5 分钟掐连接）

	// HostKeyCallback 必须有值。留空直接报错而不是「跳过校验」——
	// 远程登录器的第一安全红线是不能静默接受未知主机密钥。
	HostKeyCallback ssh.HostKeyCallback

	// LegacyAlgorithms 打开后用 SHA-1 系列老算法兜底，用于连接老交换机/老 NAS。
	// 默认关闭；UI 上应作为「兼容旧设备」显式开关，不能默认开启。
	LegacyAlgorithms bool
}

// Client 是对 *ssh.Client 的薄封装，额外负责保活、断线通知与幂等关闭。
type Client struct {
	conn *ssh.Client
	cfg  DialConfig
	info HandshakeInfo

	stopKeepalive context.CancelFunc
	closeOnce     sync.Once
	closed        atomic.Bool

	// lastRTT 最近一次保活往返耗时。keepalive 走 wantReply=true 的
	// SendRequest，服务器必须回一个 global request reply，所以这个值是真实的
	// 端到端时延，不是估算。0 表示还没测到（刚建连、或保活失败）。
	lastRTT atomic.Int64

	mu              sync.Mutex
	onDisconnect    func(error)
	disconnectHooks []func(error)
	disconnectFired bool
	disconnectErr   error
	localClosed     bool // 本地主动 Close，与「意外断开」区分开
}

// lastDisconnectErr 取上一次断连的原因（供迟到注册的监听器补一次通知）。
func (c *Client) lastDisconnectErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disconnectErr == nil {
		return ErrClosed
	}
	return c.disconnectErr
}

// Dial 建立连接。ctx 可取消（用户点「取消」或窗口关闭时立即返回）。
func Dial(ctx context.Context, cfg DialConfig) (*Client, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	auths, err := buildAuthMethods(cfg.Auth)
	if err != nil {
		return nil, err
	}

	info := HandshakeInfo{}
	sshCfg := &ssh.ClientConfig{
		User: cfg.User,
		Auth: auths,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			info.HostKeyAlgorithm = key.Type()
			info.Fingerprint = ssh.FingerprintSHA256(key)
			return cfg.HostKeyCallback(hostname, remote, key)
		},
	}
	applyLegacyAlgorithms(sshCfg, cfg.LegacyAlgorithms)

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	start := time.Now()
	raw, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}

	// 握手阶段没有 context 可传，只能靠连接层 deadline 兜底：
	// 服务器接受了 TCP 但打死不回握手包时，没有这行会一直挂着。
	if err := raw.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		raw.Close()
		return nil, fmt.Errorf("%w: 设置握手超时失败: %v", ErrUnreachable, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, sshCfg)
	if err != nil {
		raw.Close()
		return nil, classifyHandshakeError(err)
	}
	// 握手成功必须清掉 deadline，否则连接会在 Timeout 到点后被内核掐断。
	if err := raw.SetDeadline(time.Time{}); err != nil {
		cc.Close()
		return nil, fmt.Errorf("sshx: 清除握手超时失败: %w", err)
	}

	info.Duration = time.Since(start)
	info.ServerVersion = string(cc.ServerVersion())

	c := &Client{conn: ssh.NewClient(cc, chans, reqs), cfg: cfg, info: info}
	c.startKeepalive(cfg.Keepalive)
	c.watchConn()
	return c, nil
}

// normalize 补默认值并做前置校验。
func (c *DialConfig) normalize() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("sshx: 未指定主机地址")
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("sshx: 未指定登录用户名")
	}
	if c.HostKeyCallback == nil {
		return errors.New("sshx: 必须提供主机密钥校验回调（不允许跳过校验）")
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = defaultPort
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Keepalive <= 0 {
		c.Keepalive = defaultKeepalive
	}
	return nil
}

// classifyHandshakeError 把 x/crypto 的文本错误收敛成 Sentinel。
func classifyHandshakeError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"),
		strings.Contains(msg, "no supported methods remain"):
		return fmt.Errorf("%w: %v", ErrAuthFailed, err)
	case strings.Contains(msg, "no common algorithm"):
		return fmt.Errorf("%w（服务器可能是老设备，可开启「兼容旧设备」）: %v", ErrAlgorithmGone, err)
	default:
		return fmt.Errorf("sshx: 握手失败: %w", err)
	}
}

// applyLegacyAlgorithms 在兼容模式下把 SHA-1 系列算法补回候选集。
//
// 注意：一旦给 HostKeyAlgorithms/KeyExchanges 赋值就会整体覆盖默认列表，
// 所以必须先复制 SupportedAlgorithms() 再追加，不能只写老的几个。
func applyLegacyAlgorithms(cfg *ssh.ClientConfig, legacy bool) {
	if !legacy {
		return
	}
	supported := ssh.SupportedAlgorithms()
	cfg.HostKeyAlgorithms = withExtra(supported.HostKeys, "ssh-rsa", "ssh-dss")
	cfg.KeyExchanges = withExtra(supported.KeyExchanges,
		"diffie-hellman-group14-sha1",
		"diffie-hellman-group1-sha1",
		"diffie-hellman-group-exchange-sha1",
	)
	cfg.Ciphers = withExtra(supported.Ciphers, "aes128-cbc", "aes192-cbc", "aes256-cbc", "3des-cbc")
}

func withExtra(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for _, e := range extra {
		dup := false
		for _, b := range out {
			if b == e {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, e)
		}
	}
	return out
}

// startKeepalive 定时发 keepalive 请求。
//
// 不保活的实际后果是：NAT/防火墙通常在 5 分钟无流量后静默丢弃映射，
// 用户回来看到一个「界面还在但敲什么都没反应」的死连接——比直接报错更难排查。
func (c *Client) startKeepalive(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	c.stopKeepalive = cancel
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// 测一次往返时延：keepalive 要 reply，顺手就能拿到 RTT。
				start := time.Now()
				if _, _, err := c.conn.SendRequest(keepaliveRequest, true, nil); err != nil {
					c.fireDisconnect(fmt.Errorf("sshx: 保活失败，连接已断开: %w", err))
					c.Close()
					return
				}
				c.lastRTT.Store(int64(time.Since(start)))
			}
		}
	}()
}

// SetOnDisconnect 注册断线回调（#12 的自动重连依赖它）。
// 回调在保活失败时触发一次，可能来自后台 goroutine，实现方需自行保证线程安全。
func (c *Client) SetOnDisconnect(fn func(error)) {
	c.mu.Lock()
	c.onDisconnect = fn
	c.mu.Unlock()
}

// AddOnDisconnect 追加一个断线监听器，可注册多个。
//
// 与 SetOnDisconnect 的区别：后者是「覆盖」，只能留一个。这里是「追加」——
// 多标签共享一条连接后，连接层要监听断连把连接从池里摘掉，同时每个会话又各自
// 需要收到通知（会话级断连走的是 sess.Wait()，两者时机不同）。
func (c *Client) AddOnDisconnect(fn func(error)) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	// 连接在这一刻之前就已经断了：hooks 里的老监听器已经收到过通知，
	// 而 fn 刚注册，永远不会再等来第二次 fireDisconnect。
	// 不补这一下的话，连接池的 dead 标记就会永久漏在这一条 entry 上。
	fired := c.disconnectFired
	if !fired {
		c.disconnectHooks = append(c.disconnectHooks, fn)
	}
	c.mu.Unlock()

	if fired {
		fn(c.lastDisconnectErr())
	}
}

// fireDisconnect 通知所有断线监听器，只触发一次。
//
// once 的必要性：连接可能同时被 keepalive 探测失败和底层读错误两条路径判定为断开，
// 两次触发会让上层把同一批会话重复清理一遍。
func (c *Client) fireDisconnect(err error) {
	c.mu.Lock()
	// 自己主动关的连接不算「意外断开」。
	//
	// Close() 会让阻塞中的 keepalive 探测（SendRequest）返回错误并走到这里；
	// 此时若不拦，上层会收到一次假的「连接已断开」，与 watchConn 里
	// 「localClosed → 直接 return」的意图正好相反。
	//
	// 这里直接读字段，绝不调用 isLocallyClosed()：本函数已持有 c.mu，而
	// sync.Mutex 不可重入，再走一次加锁就是自死锁。短路求值在
	// disconnectFired=false 时必然求值到后半段，于是「每次断线必死锁」——
	// 该 goroutine 永久持有 c.mu，之后 Close/SetOnDisconnect 全部挂死，
	// UI 的「重连」按钮再无响应。
	if c.disconnectFired || c.localClosed {
		c.mu.Unlock()
		return
	}
	c.disconnectFired = true
	c.disconnectErr = err
	hooks := append([]func(error){}, c.disconnectHooks...)
	primary := c.onDisconnect
	c.mu.Unlock()

	if primary != nil {
		primary(err)
	}
	for _, fn := range hooks {
		fn(err)
	}
}

// watchConn 监视底层 TCP 连接。
//
// 为什么不能只靠 keepalive：keepalive 默认 45s 一轮，拔网线后最长要等 45s 才发现。
// 单会话时这只是「界面卡住」，但多标签共享连接后，池必须立刻知道这条连接已死，
// 否则下一个新标签会拿到一条已死的连接去 NewSession，报一个语焉不详的
// "EOF" 而不是「连接已断开，请重连」。
func (c *Client) watchConn() {
	go func() {
		// Wait 在底层连接被对端关闭或发生不可恢复错误时返回；
		// 本地主动 Close 也会让它返回。
		err := c.conn.Wait()
		c.closed.Store(true)
		if c.isLocallyClosed() {
			return // 自己关的，不必通知
		}
		if err == nil {
			err = ErrClosed
		}
		c.fireDisconnect(fmt.Errorf("sshx: 连接已断开: %w", err))
	}()
}

func (c *Client) isLocallyClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localClosed
}

// SSH 返回底层连接，供 session / sftp 复用同一条 TCP。
// 多标签、SFTP 面板都建立在「一个 Client 多个 Channel」上，不是多次握手。
func (c *Client) SSH() *ssh.Client { return c.conn }

// Info 返回握手信息（只读）。
func (c *Client) Info() HandshakeInfo { return c.info }

// LastRTT 最近一次保活的往返时延；0 表示尚未测到（刚建连）。
func (c *Client) LastRTT() time.Duration { return time.Duration(c.lastRTT.Load()) }

// Alive 表示连接是否仍然可用。
func (c *Client) Alive() bool { return c != nil && !c.closed.Load() }

// Close 幂等关闭连接并停止保活。
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.stopKeepalive != nil {
			c.stopKeepalive()
		}
		// 先置位再关连接：watchConn 里的 Wait 会因为这次关闭而返回，
		// 靠这个标记把它和「意外断开」区分开，否则关窗口会误报一次断连。
		c.mu.Lock()
		c.localClosed = true
		c.mu.Unlock()
		c.closed.Store(true)
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}
