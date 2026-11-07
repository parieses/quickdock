package sshx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 端口转发引擎。
//
// 与终端会话的关键区别：转发是长生命周期、且没有用户可以「重连一下」的界面动作。
// 所以它**不进 connPool**（池的 refs 归零即 Close，随便关一个终端标签就能掐死转发），
// 每条规则自己拨一条独立连接，自己管断线重连。
//
// 拨号器由 services 层注入：internal 不能 import services（依赖方向 R10）。
// 注入的好处是跳板链自动生效——门面用的还是同一个 s.dial。

const (
	ForwardKindLocal  = "local"  // 本地监听 → 经 SSH 服务器拨目标
	ForwardKindRemote = "remote" // SSH 服务器监听 → 经本机拨目标
	ForwardKindSOCKS  = "socks"  // 本地监听 SOCKS5 → 经 SSH 服务器拨任意目标
)

const (
	ForwardStateStopped  = "stopped"
	ForwardStateStarting = "starting"
	ForwardStateRunning  = "running"
	ForwardStateRetrying = "retrying"
	ForwardStateError    = "error"
)

var (
	ErrForwardRunning = errors.New("sshx: 转发已在运行")
	ErrForwardKind    = errors.New("sshx: 不支持的转发类型")
	ErrForwardRule    = errors.New("sshx: 转发规则无效")
	ErrForwardSocks   = errors.New("sshx: SOCKS5 握手失败")
)

const (
	forwardDialLimit = 20 * time.Second
	// maxForwardRetries 连续意外断开后的最大重连次数。退避 1/2/4/8/16/30/30/30s，
	// 总窗口约两分钟；再连不上就落 error 让用户手动处理，避免无限静默重试。
	maxForwardRetries = 8
)

// ForwardRule 一条转发规则（持久化形态，不含运行时状态）。
type ForwardRule struct {
	ID         string `json:"id"`
	HostID     string `json:"hostId"`
	Kind       string `json:"kind"`
	BindHost   string `json:"bindHost"`
	BindPort   int    `json:"bindPort"`
	TargetHost string `json:"targetHost"`
	TargetPort int    `json:"targetPort"`
	AutoStart  bool   `json:"autoStart"`
}

// ForwardView 前端展示用：规则 + 运行时状态。
type ForwardView struct {
	ID          string `json:"id"`
	HostID      string `json:"hostId"`
	HostName    string `json:"hostName"`
	Kind        string `json:"kind"`
	BindHost    string `json:"bindHost"`
	BindPort    int    `json:"bindPort"`
	TargetHost  string `json:"targetHost"`
	TargetPort  int    `json:"targetPort"`
	AutoStart   bool   `json:"autoStart"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	BytesIn     int64  `json:"bytesIn"`
	BytesOut    int64  `json:"bytesOut"`
	Connections int64  `json:"connections"`
	Active      int64  `json:"active"`
	Retries     int    `json:"retries"`
	// Reconnected 表示当前这段 running 是断线重连来的。前端只在事件里看到它时
	// 提示一次「已重连，期间的连接断了」；轮询列表不重复弹。
	Reconnected bool   `json:"reconnected"`
	StartedAt   string `json:"startedAt"`
}

// View 把规则转成「未启动」状态的展示对象。
func (r ForwardRule) View() ForwardView {
	return ForwardView{
		ID: r.ID, HostID: r.HostID, Kind: r.Kind,
		BindHost: r.BindHost, BindPort: r.BindPort,
		TargetHost: r.TargetHost, TargetPort: r.TargetPort,
		AutoStart: r.AutoStart, Status: ForwardStateStopped,
	}
}

// ValidateForwardRule 校验规则。只做「能安全拿去拨号」的静态检查，不查库。
//
// bindHost 收字面 IP 或回环别名，不收域名：解析结果会随 DNS 变化，
// 一条转发规则不该在某天悄悄绑到另一个网卡上。非回环地址的二次确认由
// 前端在保存前做（后端只保证拿到的值能安全进 net.Listen）。
func ValidateForwardRule(r ForwardRule) error {
	switch r.Kind {
	case ForwardKindLocal, ForwardKindRemote, ForwardKindSOCKS:
	default:
		return fmt.Errorf("%w: %q", ErrForwardKind, r.Kind)
	}
	if strings.TrimSpace(r.HostID) == "" {
		return fmt.Errorf("%w: 未指定主机", ErrForwardRule)
	}
	if r.BindPort < 1 || r.BindPort > 65535 {
		return fmt.Errorf("%w: 监听端口必须在 1..65535", ErrForwardRule)
	}
	switch r.BindHost {
	case "", "127.0.0.1", "localhost", "0.0.0.0", "::1", "::":
	default:
		if net.ParseIP(r.BindHost) == nil {
			return fmt.Errorf("%w: 监听地址只能是回环或通配地址", ErrForwardRule)
		}
	}
	if r.Kind == ForwardKindSOCKS {
		return nil // 目标由 SOCKS 客户端每次自己给
	}
	if strings.TrimSpace(r.TargetHost) == "" || r.TargetPort < 1 || r.TargetPort > 65535 {
		return fmt.Errorf("%w: 目标地址或端口无效", ErrForwardRule)
	}
	if strings.ContainsAny(r.TargetHost, "\r\n\x00") {
		return fmt.Errorf("%w: 目标地址含非法字符", ErrForwardRule)
	}
	return nil
}

// ForwardDialer 按 hostID 建一条独立连接。返回值由 Forward 负责关闭。
type ForwardDialer func(ctx context.Context, hostID string) (*Client, error)

// Forward 一条规则的运行时实例。
type Forward struct {
	rule ForwardRule
	dial ForwardDialer
	emit func(ForwardView)

	mu      sync.Mutex
	view    ForwardView
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	bytesIn  atomic.Int64
	bytesOut atomic.Int64
	conns    atomic.Int64
	active   atomic.Int64
}

// NewForward 创建实例；emit 可为 nil（比如测试）。
func NewForward(rule ForwardRule, dial ForwardDialer, emit func(ForwardView)) *Forward {
	v := rule.View()
	return &Forward{rule: rule, dial: dial, emit: emit, view: v}
}

// Rule 返回规则副本。
func (f *Forward) Rule() ForwardRule { return f.rule }

// Snapshot 返回带实时流量的状态快照。
func (f *Forward) Snapshot() ForwardView {
	f.mu.Lock()
	v := f.view
	f.mu.Unlock()
	v.BytesIn = f.bytesIn.Load()
	v.BytesOut = f.bytesOut.Load()
	v.Connections = f.conns.Load()
	v.Active = f.active.Load()
	return v
}

func (f *Forward) set(status, errText string, reconnected bool) {
	f.mu.Lock()
	f.view.Status = status
	f.view.Error = errText
	f.view.Reconnected = reconnected
	if status == ForwardStateRunning && f.view.StartedAt == "" {
		f.view.StartedAt = time.Now().Format(time.RFC3339)
	}
	if status == ForwardStateStopped {
		f.view.StartedAt = ""
		f.view.Retries = 0
	}
	v := f.SnapshotLocked()
	f.mu.Unlock()
	if f.emit != nil {
		f.emit(v)
	}
}

// SnapshotLocked 在已持有 f.mu 时取快照，避免 set 里重复加锁。
func (f *Forward) SnapshotLocked() ForwardView {
	v := f.view
	v.BytesIn = f.bytesIn.Load()
	v.BytesOut = f.bytesOut.Load()
	v.Connections = f.conns.Load()
	v.Active = f.active.Load()
	return v
}

func (f *Forward) setRetries(n int) {
	f.mu.Lock()
	f.view.Retries = n
	f.mu.Unlock()
}

// Start 启动转发（幂等：已在运行返回 ErrForwardRunning）。
func (f *Forward) Start() error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return ErrForwardRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	f.started = true
	f.view.Status = ForwardStateStarting
	f.view.Error = ""
	f.view.StartedAt = ""
	f.view.Reconnected = false
	f.mu.Unlock()

	go f.supervise(ctx)
	return nil
}

// Stop 主动停止。等 supervise 完全退出再返回，调用方可以立刻删除规则而不留 goroutine。
func (f *Forward) Stop() {
	f.mu.Lock()
	if !f.started {
		f.mu.Unlock()
		return
	}
	cancel, done := f.cancel, f.done
	f.started = false
	f.cancel = nil
	f.mu.Unlock()

	cancel()
	<-done
	f.set(ForwardStateStopped, "", false)
}

func (f *Forward) supervise(ctx context.Context) {
	defer close(f.done)
	retries := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := f.runOnce(ctx, retries > 0)
		if ctx.Err() != nil {
			return // 主动 Stop
		}
		// 走到这里一定是「意外断开 / 拨号失败 / 监听失败」。仅非主动关闭才重连。
		retries++
		f.setRetries(retries)
		if retries > maxForwardRetries {
			f.set(ForwardStateError, errText(err), false)
			return
		}
		f.set(ForwardStateRetrying, errText(err), false)
		if !sleepCtx(ctx, forwardBackoff(retries)) {
			return
		}
	}
}

func forwardBackoff(retries int) time.Duration {
	d := time.Duration(1<<min(retries-1, 5)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func errText(err error) string {
	if err == nil {
		return "连接已断开"
	}
	return err.Error()
}

// runOnce 拨号 → 起监听 → accept 循环，直到连接断、监听出错或主动停止。
func (f *Forward) runOnce(ctx context.Context, isReconnect bool) error {
	dialCtx, cancel := context.WithTimeout(ctx, forwardDialLimit)
	client, err := f.dial(dialCtx, f.rule.HostID)
	cancel()
	if err != nil {
		return fmt.Errorf("建立转发连接失败: %w", err)
	}
	defer client.Close()

	ln, err := f.listen(client)
	if err != nil {
		return fmt.Errorf("监听转发端口失败: %w", err)
	}
	defer ln.Close()

	// ctx 取消时主动关监听，否则 Accept 会一直阻塞、Stop 等不到 done。
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
			_ = client.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	disconnected := make(chan error, 1)
	client.AddOnDisconnect(func(e error) {
		select {
		case disconnected <- e:
		default:
		}
		_ = ln.Close()
	})

	f.set(ForwardStateRunning, "", isReconnect)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case e := <-disconnected:
				if e == nil {
					e = ErrClosed
				}
				return e
			default:
				return err
			}
		}
		f.conns.Add(1)
		f.active.Add(1)
		go f.handle(ctx, client, conn)
	}
}

func (f *Forward) listen(client *Client) (net.Listener, error) {
	bind := net.JoinHostPort(f.rule.BindHost, strconv.Itoa(f.rule.BindPort))
	if f.rule.Kind == ForwardKindRemote {
		// x/crypto 的 Client.Listen 会发 tcpip-forward 全局请求，并自动注册
		// forwarded-tcpip channel handler；服务端 GatewayPorts 关闭时可能只肯绑回环。
		return client.SSH().Listen("tcp", bind)
	}
	lc := net.ListenConfig{}
	return lc.Listen(context.Background(), "tcp", bind)
}

func (f *Forward) handle(ctx context.Context, client *Client, inbound net.Conn) {
	defer func() {
		_ = inbound.Close()
		f.active.Add(-1)
	}()

	var outbound net.Conn
	var err error
	switch f.rule.Kind {
	case ForwardKindSOCKS:
		var addr string
		addr, err = socks5Handshake(inbound)
		if err == nil {
			outbound, err = client.SSH().DialContext(ctx, "tcp", addr)
		}
	case ForwardKindLocal:
		addr := net.JoinHostPort(f.rule.TargetHost, strconv.Itoa(f.rule.TargetPort))
		outbound, err = client.SSH().DialContext(ctx, "tcp", addr)
	case ForwardKindRemote:
		addr := net.JoinHostPort(f.rule.TargetHost, strconv.Itoa(f.rule.TargetPort))
		outbound, err = (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return
	}
	defer outbound.Close()
	bridge(inbound, outbound, &f.bytesIn, &f.bytesOut)
}

// countedWriter 在转发路径上累加字节数，不额外缓冲、不改变 io.Copy 的行为。
type countedWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countedWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// bridge 双向搬运。任一方向结束就关掉两边：SSH channel 没有可靠的半关闭语义，
// 留着另一半只会让已死的连接挂在 active 计数里。
func bridge(a, b net.Conn, in, out *atomic.Int64) {
	var once sync.Once
	done := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	go func() {
		_, _ = io.Copy(countedWriter{w: b, n: out}, a)
		done()
	}()
	_, _ = io.Copy(countedWriter{w: a, n: in}, b)
	done()
}

// socks5Handshake 极简 SOCKS5 服务端（RFC 1928 子集）：
// 只支持无认证 + CONNECT，地址类型 IPv4 / 域名 / IPv6。不支持 BIND 与 UDP
// ASSOCIATE —— 浏览器与绝大多数客户端只用 CONNECT，多写的那两种没有实际用户，
// 却要各自维护一套生命周期。
//
// 返回值是「让 SSH 服务器去拨」的 host:port。回复在握手里直接写回。
func socks5Handshake(c net.Conn) (string, error) {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = c.SetDeadline(time.Time{}) }()

	greeting := make([]byte, 2)
	if _, err := io.ReadFull(c, greeting); err != nil {
		return "", err
	}
	if greeting[0] != 5 {
		return "", fmt.Errorf("%w: 版本号 %d", ErrForwardSocks, greeting[0])
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = c.Write([]byte{5, 0xff})
		return "", fmt.Errorf("%w: 不支持认证的客户端", ErrForwardSocks)
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", err
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[0] != 5 || req[1] != 1 {
		socksReply(c, 7) // command not supported
		return "", fmt.Errorf("%w: 只支持 CONNECT", ErrForwardSocks)
	}

	var host string
	switch req[3] {
	case 1: // IPv4
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3: // 域名
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	case 4: // IPv6
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		socksReply(c, 8) // address type not supported
		return "", fmt.Errorf("%w: 地址类型 %d", ErrForwardSocks, req[3])
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(c, portBuf); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf)
	if host == "" || port == 0 {
		socksReply(c, 5) // connection refused
		return "", fmt.Errorf("%w: 空目标", ErrForwardSocks)
	}
	socksReply(c, 0)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func socksReply(c net.Conn, rep byte) {
	// VER REP RSV ATYP BND.ADDR(4) BND.PORT(2)：地址一律填 0，
	// 客户端只关心 REP，不需要知道真正绑定的地址。
	_, _ = c.Write([]byte{5, rep, 0, 1, 0, 0, 0, 0, 0, 0})
}
