package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
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

// 便利功能（见 PtyOptions 的 PostLoginCommand / AutoReplySudo）的保守参数。
const (
	// postLoginReadyDelay 登录后自动命令前的等待。
	//
	// SSH 的 RequestPty / Shell 只是向服务端发起请求，协议层并没有
	// 「远端 shell 已就绪、可以收输入」的握手信号；现有 Start 也没把
	// ptyWriter 的首个数据到达暴露成就绪信号。这里只做一次很短的延时
	// 作为保守兜底（200ms，不超过 300ms），不假装有握手协议，因此
	// 无法 100% 保证命令被接收——写入失败一律忽略，绝不阻断建连。
	postLoginReadyDelay = 200 * time.Millisecond

	// sudoInputWindow 只有在这个时间窗内观测到过 "sudo " 输入，
	// 才允许对 sudo 密码提示做自动应答。
	sudoInputWindow = 2 * time.Second
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

	// PostLoginCommand 登录后自动送入 shell 的一行命令（可为空）。
	//
	// PTY 就绪后追加一个回车送进 shell，只送一次。命令前会补一个空格，
	// 借 bash/zsh 的 HISTCONTROL=ignorespace 约定尽量不写进 history；但服务端
	// 未必开启该选项，所以不承诺一定不记录。这是便利功能：写入失败只忽略，
	// 绝不能让整个会话启动失败、挡住用户连服务器。
	PostLoginCommand string

	// AutoReplySudo / SudoPassword：sudo 密码自动应答（保守版）。
	// 仅当 AutoReplySudo 为 true 且 SudoPassword 非空时启用；
	// 两者任一为零值即完全关闭，行为与新增这两个字段之前一致。
	AutoReplySudo bool
	SudoPassword  string

	// ShellIntegration 让远端 shell 每画一次提示符就上报当前目录（OSC 7）。
	//
	// 为什么必须注入：OSC 7 不是终端协议能力，而是 shell 自己打印的序列。
	// bash 默认不打印，zsh 也只有装了 oh-my-zsh 一类框架才打印，因此在裸
	// Debian / Ubuntu 上「跟随终端目录」永远等不到事件——功能看着像坏的。
	//
	// 注入的内容只是当前会话里的一行命令（改 PROMPT_COMMAND / 加 precmd
	// 钩子），不落盘、不改远端任何文件，重连即消失。代价是这行命令会被
	// tty 回显在会话顶部，且规避不了，所以调用方给了关掉的开关。
	ShellIntegration bool
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

	// ---- 便利功能状态（可选增量；零值时以下字段不参与任何逻辑）----

	// postLoginOnce 保证登录后自动命令只发送一次；回放 / 重连路径不会重发。
	postLoginOnce sync.Once

	// 自动 sudo 应答：只有 autoReplySudo 为真时才启用，此时 sudoPassword 才被拷入。
	// 密码仅存在于内存中，绝不写入日志、前端事件或回放缓冲。
	autoReplySudo bool
	sudoPassword  string
	// sudoReplyOnce 每会话最多应答一次：密码错误时 sudo 会反复提示，
	// 无限重发等同于拿同一个密码做暴力尝试，必须硬性封顶。
	sudoReplyOnce sync.Once
	// lastSudoInput 最近一次「写入包含 sudo 的输入」的时刻（UnixNano，0=从未）。
	// 用原子量：输入侧（Write / 自动命令）写，输出回调（maybeReplySudo）读，
	// 两者分处不同 goroutine，避免为此去争 s.mu。
	lastSudoInput atomic.Int64
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
	// 输出数据回调里识别 sudo 密码提示，命中则注入一次密码。
	// 注意：p 是服务端真实输出，照常进回放 / 事件；我们只往 stdin 注入密码，
	// 绝不往这里伪造任何输出。
	w.s.maybeReplySudo(p)
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

	// 便利功能配置：仅当显式开启且密码非空时启用；未启用时不把密码拷进
	// 会话，缩小敏感数据的留存面。零值（false / 空串）时下面两个变量为
	// false / ""，会话行为与新增字段之前完全一致。在构造 s 时一次性写死，
	// 之后只读，避免与输入 / 输出 goroutine 产生数据竞争。
	autoReplySudo := opts.AutoReplySudo && opts.SudoPassword != ""
	sudoPassword := ""
	if autoReplySudo {
		sudoPassword = opts.SudoPassword
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

		autoReplySudo: autoReplySudo,
		sudoPassword:  sudoPassword,
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

	// ---- 登录后注入（便利功能，尽力而为）----
	// 放在 Start 末尾：会话已登记、Wait 已起、PTY 已就绪且 s.stdin 可用。
	// 用独立 goroutine 做短延时，不阻塞 Start 返回。
	//
	// shell 集成与登录后命令合到同一个 goroutine 里按序写：两条都是往同一个
	// stdin 塞一行命令，若各起一个 goroutine 去抢同一个延时点，先后顺序就是
	// 随机的——用户在登录后命令里 cd 一次，目录上报可能恰好错过那一次提示符。
	if opts.PostLoginCommand != "" || opts.ShellIntegration {
		cmd := opts.PostLoginCommand
		integrate := opts.ShellIntegration
		go func() {
			// 保守兜底：见 postLoginReadyDelay 的说明。协议里没有 shell 就绪
			// 握手，现有 Start 也没有现成的就绪信号可复用，故只做一次很短的
			// 延时，不假装有握手协议；命令能否被接收不作保证。
			time.Sleep(postLoginReadyDelay)
			if integrate {
				if hook := shellIntegrationHook(client); hook != "" {
					// 开头先送一个 Ctrl-U（kill line）：注入点固定在连接后 200ms，
					// 手快的用户可能已经敲了半行命令，直接拼上去会把那半行和钩子
					// 粘成一条错误命令。Ctrl-U 在 bash / zsh / fish 的行编辑里
					// 都是「抹掉光标前整行」，代价只是丢掉那半行输入（250ms 内，
					// 实际几乎不存在）。
					_ = s.injectStdin([]byte("\x15 " + hook + "\r"))
				}
			}
			if cmd == "" {
				return
			}
			s.postLoginOnce.Do(func() {
				// 前导空格：借 bash/zsh 的 HISTCONTROL=ignorespace 约定尽量
				// 不进 history；服务端可能没开，不承诺。结尾回车让 shell 执行。
				payload := []byte(" " + cmd + "\r")
				// 自动命令本身若含 sudo，也记一次 sudo 输入痕迹，让它随后
				// 弹出的密码提示能通过自动应答的前置条件。
				if s.autoReplySudo && bytes.Contains(payload, []byte("sudo ")) {
					s.lastSudoInput.Store(time.Now().UnixNano())
				}
				// 走 injectStdin 直接写 stdin，不经过 ptyWriter（不进回放 / 事件）。
				// 写失败静默忽略：本包不引入 logger，且便利命令绝不能阻断会话。
				_ = s.injectStdin(payload)
			})
		}()
	}

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
// 关键：这里不把会话从管理器移除。用户可能只是网络抖了一下、
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
	// 记录 sudo 输入痕迹，供输出侧判断「最近是否真的敲过 sudo 命令」。
	// 只记时间戳、绝不记录输入内容（里面可能就是密码）。仅在功能开启时做，
	// 零值路径与新增字段之前完全一致（只多一次布尔短路判断）。
	if s.autoReplySudo && bytes.Contains(p, []byte("sudo ")) {
		s.lastSudoInput.Store(time.Now().UnixNano())
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.stdin.Write(p)
}

// shellIntegrationHook 返回「让远端 shell 每画一次提示符就上报 OSC 7」的一行命令。
//
// 认不出的 shell 一律返回空串：往 csh / tcsh 里塞 POSIX 语法只会在用户屏幕上
// 刷一行报错，比功能不生效更糟。已知能挂钩子的：
//   - bash：PROMPT_COMMAND 每次提示符前执行，直接追加（保留原有内容）；
//   - zsh：走 add-zsh-hook precmd，比直接覆盖 precmd 更能和用户的框架共存；
//   - fish：监听 fish_prompt 事件（fish 自己的 printf 支持 \e、\a）。
//
// 注意 OSC 7 的载荷是 file://<host><path>，host 部分前端不校验也不使用
// （远端本来就不可信，取到空 host 也照样只取路径），所以取不到 HOSTNAME 时
// 退化成 file:///path 即可，不必为了拼 host 再做一次探测。
func shellIntegrationHook(client *Client) string {
	switch sh := remoteShellName(client); {
	case strings.Contains(sh, "bash"):
		return `PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND;}printf '\033]7;file://%s%s\007' \"${HOSTNAME:-localhost}\" \"\$PWD\""`
	case strings.Contains(sh, "zsh"):
		return `autoload -Uz add-zsh-hook; __qd_osc7(){ printf '\033]7;file://%s%s\007' "${HOST:-localhost}" "$PWD" }; add-zsh-hook precmd __qd_osc7`
	case strings.Contains(sh, "fish"):
		return `function __qd_osc7 --on-event fish_prompt; printf '\e]7;file://%s%s\a' $hostname $PWD; end`
	default:
		return ""
	}
}

// remoteShellName 问远端「登录 shell 是哪个」，拿不到返回空串。
//
// 走一条独立的一次性 exec 通道，不碰交互会话：如果靠往终端里敲命令再解析
// 回显来判壳，既要处理回显擦除、又会把探测命令留在用户屏幕上。代价是开局
// 多一次往返（已认证连接上一次 exec，通常几毫秒），失败就静默放弃。
//
// 为什么信 $SHELL 而不是 /proc/self/...：sshd 就是按 passwd 里的登录 shell
// 起交互会话的，$SHELL 与它一致；而这条 exec 通道本身也是用它来执行命令
// 的（POSIX shell 由 sshd 用 `$SHELL -c` 跑），所以拿到的就是同一个壳。
func remoteShellName(client *Client) string {
	if client == nil || client.conn == nil {
		return ""
	}
	sess, err := client.conn.NewSession()
	if err != nil {
		return ""
	}
	defer func() { _ = sess.Close() }()
	out, err := sess.Output(`printf %s "$SHELL"`)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

// injectStdin 把内部生成的字节直接写进远端 stdin。
//
// 关键的安全边界：绝不走 ptyWriter —— 那个 writer 代表「服务器输出」，会把
// 内容写进回放缓冲和前端 data 事件。密码、自动命令都是我们注入的输入，
// 不能出现在这些缓冲里，否则等于把密码留存 / 回显了下来。
// （若服务端异常地没关 ECHO 而把密码回显回来，那是服务端行为，不在本层
// 可控范围；我们能保证的是「不主动把密码写进任何缓冲 / 日志」。）
func (s *PtySession) injectStdin(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// stdin.Write 返回 (n, error)。这里只关心「有没有写进去」，不把字节数
	// 暴露给调用方：注入是尽力而为，短写由调用方下一次输出帧再兜，
	// 让自动命令/应答的失败不影响会话本身。
	_, err := s.stdin.Write(p)
	return err
}

// maybeReplySudo 在服务器输出里识别 sudo 密码提示，命中则注入一次密码。
//
// 安全取舍（保守版，逐条对应需求）：
//   - 每会话最多应答一次（sudoReplyOnce）：密码错了 sudo 会反复提示，
//     无限重发就等于拿同一个密码做暴力尝试，必须封顶。
//   - 前置条件：只在最近 sudoInputWindow 内观测到用户写过含 "sudo " 的
//     输入时才应答。「Password:」也可能来自 ssh 登录提示或其它程序，
//     单看输出帧无法区分，用这个时间窗把 sudo 上下文补上。
//   - 局限：无法 100% 区分 sudo 提示与其它密码提示；一旦误判，靠
//     「每会话一次」+「密码不落任何缓冲 / 日志」把损害压到最小。
//     另外前置条件依赖输入都经过 Write / 自动命令这两条路径；若将来有
//     绕过它们直写 stdin 的输入，前置条件会失效，此时就只剩「每会话一次」
//     这一层保护，这也是保守版能给出的下限。
//   - 密码只经 injectStdin 写 stdin，绝不写日志 / 事件 / 回放缓冲；
//     sudo 提示本身是服务端真实输出，由 ptyWriter 原样转发，不伪造输出。
func (s *PtySession) maybeReplySudo(out []byte) {
	if !s.autoReplySudo {
		return
	}
	if !isSudoPasswordPrompt(out) {
		return
	}
	last := s.lastSudoInput.Load()
	if last == 0 || time.Since(time.Unix(0, last)) > sudoInputWindow {
		return
	}
	s.sudoReplyOnce.Do(func() {
		// 注入密码 + 回车；写失败静默忽略，不重试、不阻断会话。
		_ = s.injectStdin([]byte(s.sudoPassword + "\r"))
	})
}

// isSudoPasswordPrompt 判断一段服务器输出是否像 sudo 的密码提示。
//
// 覆盖常见形态：[sudo] password for ...、Password:、以及 sudo 的中文变体
// （「[sudo] xxx 的密码：」「请输入密码」等）。这里只认「提示长什么样」；
// 「是不是 sudo 上下文」交给调用方的前置条件（最近有 sudo 输入）判断，
// 因为裸 Password: / 密码：单看这一帧无法区分来源。
func isSudoPasswordPrompt(out []byte) bool {
	if len(out) == 0 {
		return false
	}
	lower := bytes.ToLower(out)
	// 英文 password 覆盖 password for / Password: / [sudo] password；
	// 中文「密码」覆盖 sudo 的中文提示变体。
	return bytes.Contains(lower, []byte("password")) ||
		bytes.Contains(out, []byte("密码"))
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
