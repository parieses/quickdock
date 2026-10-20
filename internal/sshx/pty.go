package sshx

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// PTY 交互会话：分配伪终端、跑一个 shell、双向转发数据。
//
// 这是「终端」的核心。和 Exec 的关键区别在于：Exec 是一次性的
// （请求 → 收集输出 → 结束），PTY 是长连接、全双工、服务器会在其中跑
// vim/htop 这类需要终端尺寸和行编辑的程序。两者不能合并实现。

const (
	// DefaultTerm 终端类型。
	//
	// 必须是 xterm-256color：写成 xterm 会让 vim / htop / ls 退化成 8 色，
	// 看起来像「颜色丢了」。绝大多数现代终端上报的都是这个值。
	DefaultTerm = "xterm-256color"

	// DefaultScrollback 回放缓冲容量。
	//
	// 256KB 约等于满屏输出 4000 行的 ANSI 序列，足够覆盖「关窗倒杯水再回来」的时间跨度。
	DefaultScrollback = 256 << 10
)

// SessionState 会话状态。
type SessionState string

const (
	StateConnected    SessionState = "connected"
	StateDisconnected SessionState = "disconnected" // 连接断了，但会话对象还在（可回放）
	StateClosed       SessionState = "closed"       // 用户主动关闭
)

var (
	// ErrSessionNotFound 会话 ID 不存在或已被清理。
	ErrSessionNotFound = errors.New("sshx: 会话不存在")
	// ErrInvalidPtySize 终端尺寸非法（列/行必须为正）。
	ErrInvalidPtySize = errors.New("sshx: 终端尺寸无效")
)

// PtyOptions 打开会话的参数。
type PtyOptions struct {
	Term  string            // 留空用 DefaultTerm
	Cols  int               // 列数（宽度）
	Rows  int               // 行数（高度）
	Label string            // 会话标签（一般是 user@host:port），仅用于展示与恢复
	Modes ssh.TerminalModes // 留空用默认（回显开、波特率 14400）
}

// SessionInfo 会话摘要（前端标签栏用）。
type SessionInfo struct {
	ID       string       `json:"id"`
	State    SessionState `json:"state"`
	Cols     int          `json:"cols"`
	Rows     int          `json:"rows"`
	Received int64        `json:"received"` // 累计收到的字节数
	Label    string       `json:"label"`    // 建连时的标签（一般是 user@host:port）
	Started  int64        `json:"started"`  // 建连时刻（Unix 毫秒），用于算连接时长
	RTTMs    int64        `json:"rttMs"`    // 最近一次保活往返时延（毫秒）；0 = 还没测到
}

// PtySession 一个运行中的交互终端。
type PtySession struct {
	ID     string
	client *Client

	sess  *ssh.Session
	stdin io.WriteCloser

	ring    *RingBuffer
	batcher *Batcher

	writeMu sync.Mutex // stdin 不保证并发安全

	mu       sync.Mutex
	state    SessionState
	cols     int
	rows     int
	closeErr error
	label    string    // 建连时的标签，供 ListSessions 回给前端做恢复匹配
	started  time.Time // 建连时刻，用于状态条显示连接时长
	// onClose 只在用户主动 Close 时调用（用于从管理器摘除）。
	// 远端断开不会触发它——断开后会话要留着供回放。
	onClose func(*PtySession)
	// onStateChange 状态变化通知（断开/关闭），前端据此停掉 loading、
	// 显示「连接已断开，点击重连」。断开时也走这里，所以能覆盖 #12 的重连提示。
	onStateChange func(SessionState, error)

	received atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}
}

// ptyWriter 把服务器发来的字节同时送进回放缓冲和合批队列。
//
// 用 io.Writer 而不是 StdoutPipe：ssh 内部会保证所有数据在 Wait 返回前写完，
// 少一个手动读循环，也就少一处「忘记处理 n>0 但 err!=nil」的坑。
type ptyWriter struct{ s *PtySession }

func (w ptyWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.s.ring.Write(p)
	w.s.received.Add(int64(len(p)))
	w.s.batcher.Push(w.s.ID, p)
	return len(p), nil
}

// Manager 管理全部 PTY 会话（进程内单例）。
//
// 会话必须常驻在 Go 侧：关掉窗口不能断连，否则「边看文档边敲命令」这个
// 最基本的用法就没法用（窗口一失焦就断线，回来还得重新 cd 到原来的目录）。
type Manager struct {
	mu         sync.RWMutex
	sessions   map[string]*PtySession
	batcher    *Batcher
	scrollback int
}

// NewManager 创建会话管理器。scrollback <= 0 时使用 DefaultScrollback。
func NewManager(batcher *Batcher, scrollback int) *Manager {
	if scrollback <= 0 {
		scrollback = DefaultScrollback
	}
	return &Manager{
		sessions:   make(map[string]*PtySession),
		batcher:    batcher,
		scrollback: scrollback,
	}
}

// Start 在已建立的连接上开一个交互会话。
//
// client 必须已经认证完成（Dial 返回的那个）。同一个 client 可以开多个会话
// （多标签共享一条 TCP），这也是为什么连接层从一开始就持 *ssh.Client 而不是 *ssh.Session。
func (m *Manager) Start(client *Client, opts PtyOptions) (*PtySession, error) {
	if client == nil || !client.Alive() {
		return nil, ErrClosed
	}
	if opts.Cols <= 0 || opts.Rows <= 0 {
		return nil, ErrInvalidPtySize
	}

	term := opts.Term
	if term == "" {
		term = DefaultTerm
	}
	modes := opts.Modes
	if modes == nil {
		modes = defaultTerminalModes()
	}

	sess, err := client.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("%w: 创建会话失败: %v", ErrClosed, err)
	}

	s := &PtySession{
		ID:      newSessionID(),
		client:  client,
		sess:    sess,
		ring:    NewRingBuffer(m.scrollback),
		batcher: m.batcher,
		state:   StateConnected,
		cols:    opts.Cols,
		rows:    opts.Rows,
		label:   opts.Label,
		started: time.Now(),
		closed:  make(chan struct{}),
	}
	s.onClose = m.remove

	w := ptyWriter{s: s}
	sess.Stdout = w
	// PTY 模式下服务器把 stderr 也合并进同一个数据通道，
	// 但不同 sshd 实现不一致，显式接上避免丢失。
	sess.Stderr = w

	// 参数顺序是 (term, h, w)：高度在前。
	// 与前端 FitAddon 给的 (cols, rows) 相反，写反了表现为 vim 一打开就花屏。
	if err := sess.RequestPty(term, opts.Rows, opts.Cols, modes); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("sshx: 申请终端失败: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("sshx: 获取输入管道失败: %w", err)
	}
	s.stdin = stdin

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("sshx: 启动 shell 失败: %w", err)
	}

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	// Wait 必须在 Shell 之后立刻起：它负责回收会话资源，
	// 也是唯一能感知「远端断开」的地方。
	go func() {
		waitErr := sess.Wait()
		s.finish(waitErr)
	}()

	return s, nil
}

// SetOnStateChange 注册状态变化回调。
//
// 如果注册时会话已经断开，会立即回调一次——否则会出现「连接在 Start 返回前
// 就断了，前端永远收不到通知、一直转圈」的竞态。
func (s *PtySession) SetOnStateChange(fn func(SessionState, error)) {
	s.mu.Lock()
	s.onStateChange = fn
	st, err := s.state, s.closeErr
	s.mu.Unlock()

	if fn != nil && st == StateDisconnected {
		fn(st, err)
	}
}

// finish 处理会话结束（远端断开 / shell 退出 / 连接中断）。
//
// 关键：这里**不**把会话从管理器移除。用户可能只是网络抖了一下、
// 或者服务器踢了连接，此时最后一段画面仍有价值——窗口重开要能回放出来（#12）。
// 真正移除只发生在用户点「关闭」时（Close）。代价是断开的会话会留在内存里，
// 由 CloseAll（应用退出）或用户关标签来回收。
func (s *PtySession) finish(waitErr error) {
	s.mu.Lock()
	if s.state == StateClosed {
		s.mu.Unlock()
		return
	}
	s.state = StateDisconnected
	s.closeErr = waitErr
	cb := s.onStateChange
	s.mu.Unlock()

	// 把最后几帧冲刷出去，否则用户会看到「命令输出到一半就没了」。
	s.batcher.Flush(s.ID)

	if cb != nil {
		cb(StateDisconnected, waitErr)
	}
}

// Write 把用户输入写进远端 shell 的 stdin。
func (s *PtySession) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.State() != StateConnected {
		return 0, ErrClosed
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.stdin.Write(p)
}

// Resize 调整远端终端尺寸（用户拖窗口 / 打开侧栏时调用）。
func (s *PtySession) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return ErrInvalidPtySize
	}

	s.mu.Lock()
	if s.state != StateConnected {
		s.mu.Unlock()
		return ErrClosed
	}
	s.cols, s.rows = cols, rows
	s.mu.Unlock()

	// WindowChange 的参数是 (h, w)，同样是高度在前。
	// 传反了不会报错，只会让 TUI 程序按错误的宽高重排，看起来像「花屏」。
	return s.sess.WindowChange(rows, cols)
}

// Replay 返回用于恢复画面的历史输出。
//
// 只给最近的一段（环形缓冲容量），早期内容已经丢弃过就由 Truncated 告知。
func (s *PtySession) Replay() []byte { return s.ring.Bytes() }

// ReplayTruncated 历史输出是否已被截断。
func (s *PtySession) ReplayTruncated() bool { return s.ring.Truncated() }

// State 当前状态。
func (s *PtySession) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Info 会话摘要。
func (s *PtySession) Info() SessionInfo {
	s.mu.Lock()
	info := SessionInfo{
		ID:      s.ID,
		State:   s.state,
		Cols:    s.cols,
		Rows:    s.rows,
		Label:   s.label,
		Started: s.started.UnixMilli(),
	}
	s.mu.Unlock()
	info.Received = s.received.Load()
	if s.client != nil {
		info.RTTMs = s.client.LastRTT().Milliseconds()
	}
	return info
}

// Close 主动结束会话（用户点关闭标签 / 关闭窗口后清理）。
//
// 幂等：重复调用不会重复关闭，也不会重复触发回调。
func (s *PtySession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = StateClosed
		cb := s.onStateChange
		onClose := s.onClose
		s.mu.Unlock()

		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		err = s.sess.Close()

		s.batcher.Flush(s.ID)

		if onClose != nil {
			onClose(s)
		}
		// 主动关闭也要通知：前端靠这个事件清掉标签和 loading 态。
		if cb != nil {
			cb(StateClosed, nil)
		}
		close(s.closed)
	})
	return err
}

// ---- Manager ----

// Get 按 ID 取会话。
func (m *Manager) Get(id string) (*PtySession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// List 返回全部会话摘要（按创建顺序无保证，前端自己排）。
func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	m.mu.RUnlock()
	return out
}

// Close 关闭指定会话；已断开但仍保留（供回放）的会话也能关。
func (m *Manager) Close(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrSessionNotFound
	}
	return s.Close()
}

// CloseAll 关闭全部会话（应用退出时调用）。
func (m *Manager) CloseAll() {
	m.mu.RLock()
	list := make([]*PtySession, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.RUnlock()

	for _, s := range list {
		_ = s.Close()
	}
}

// remove 从管理器移除会话（由会话自己在关闭时回调）。
func (m *Manager) remove(s *PtySession) {
	m.mu.Lock()
	// 只有在表里还是同一个指针时才删：ID 不会重复，但这样写能防住
	// 「旧会话的延迟回调把新会话挤掉」这种理论上存在的竞态。
	if cur, ok := m.sessions[s.ID]; ok && cur == s {
		delete(m.sessions, s.ID)
	}
	m.mu.Unlock()
}

func defaultTerminalModes() ssh.TerminalModes {
	return ssh.TerminalModes{
		ssh.ECHO:          1, // 本地回显交给服务器（终端模拟器的标准分工）
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
}

// newSessionID 生成会话 ID。
//
// 不用 UUID 是为了日志好读；碰撞概率靠时间戳 + 递增序号压到可忽略。
var sessionSeq atomic.Uint64

func newSessionID() string {
	return fmt.Sprintf("ssh-%d-%d", time.Now().UnixMilli(), sessionSeq.Add(1))
}
