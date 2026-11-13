package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// proxyCommandStderrLimit 只保留外部命令最近 4KB 的 stderr。
// cloudflared / nc 起不来时，这几行几乎是唯一的线索；但外部程序可能疯狂刷屏，
// 不设上限会把内存吃满。排障只需要最后几行，所以超出的部分从头部丢掉。
const proxyCommandStderrLimit = 4 * 1024

// ProxyCommandDialer 返回一个 Connector：每次拨号启动一次外部命令，
// 用它的 stdin/stdout 承载 SSH 连接（等价 OpenSSH 的 ProxyCommand）。
//
// 模板支持 %h（主机）、%p（端口）、%r（用户）、%%（字面 %）占位符。
// 返回的 func 签名与 DialConfig.Connector 一致，直接赋给该字段即可。
//
// 安全约定：命令绝不经过 shell。占位符先替换、再按本文件自有规则分词，最后
// exec.Command 直接 execve argv[0]；因此模板里的 ; | $() 等都只是普通字符，
// 不会被解释成另一条命令。
//
// 进程生命周期刻意**不绑定传入的 ctx**：Dial 里那个 dialCtx 是
// context.WithTimeout 且 `defer cancel()`，握手一返回就被取消、15 秒后还会
// 自己到期。若用 exec.CommandContext 绑它，cloudflared 这类进程会在
// 「连接刚建好」或「会话跑到第 15 秒」时被 Kill，表现为莫名其妙断线。
// 这与 DialConfig.Connector 的既有契约一致——ctx 只约束建连那一段。
// 真正管进程的是 net.Conn 自己：握手超时靠 SetDeadline 触发 Kill，
// 会话结束靠 Close 收尾。
func ProxyCommandDialer(template, host string, port int, user string) (func(context.Context, string, string) (net.Conn, error), error) {
	// 空模板必须硬报错：静默回退到直连等于「用户以为走了代理，其实裸连出去」，
	// 在跳板 / 零信任场景里这是安全事故，不是能用默认值兜底的小事。
	if strings.TrimSpace(template) == "" {
		return nil, errors.New("sshx: ProxyCommand 模板为空，拒绝静默直连")
	}

	expanded, err := expandProxyTemplate(template, host, port, user)
	if err != nil {
		return nil, err
	}
	argv, err := splitProxyFields(expanded)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("sshx: ProxyCommand 模板 %q 里没有可执行的命令", template)
	}

	addr := proxyCommandAddr{host: host, port: port}
	// argv 在构造时就固定：一条连接 = 一个进程，Close 时把那个进程收掉，
	// 多次拨号之间互不串扰。
	return func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		// Dial 会传 network、addr 两个参数，这里刻意忽略：ProxyCommand 连到哪里
		// 已经由模板里的 %h/%p 决定，再把 addr 拼回去等于绕过模板校验，
		// 给外部程序塞进一个没检查过的参数。
		return startProxyCommand(ctx, argv, addr)
	}, nil
}

// expandProxyTemplate 在分词之前替换占位符。
//
// 为什么必须「先替换、后分词」：%h/%p/%r 的值可能带空格或引号字符，若先分词，
// 一个带空格的值会把模板切成错误的片段，替换就落不到同一个 argv 元素上；
// 反过来先替换，分词器看到的就是最终命令串，配合引号规则才能得到正确 argv。
//
// 用单趟扫描而不是四次 strings.ReplaceAll：%% 与 %h 混排时，先替换 %% 会把
// "%%h" 变成 "%h" 再被 %h 规则吃掉，得到主机名而不是字面 "%h"。两字符一组
// 消费，彼此不干扰。
func expandProxyTemplate(template, host string, port int, user string) (string, error) {
	portStr := strconv.Itoa(port)

	// 替换值若以 '-' 开头，外部程序会把它当成选项而不是目标
	// （-oProxyCommand=... 这类选项注入，参考 Motty 对导入配置的拒绝对策）。
	// 必须在替换前就拒绝，不能指望后面 argv 校验兜底。
	for _, kv := range []struct{ name, val string }{
		{"%h", host},
		{"%p", portStr},
		{"%r", user},
	} {
		if strings.HasPrefix(kv.val, "-") {
			return "", fmt.Errorf("sshx: ProxyCommand 占位符 %s 的值 %q 以 '-' 开头，拒绝以防选项注入", kv.name, kv.val)
		}
	}

	var b strings.Builder
	for i := 0; i < len(template); {
		if template[i] != '%' {
			b.WriteByte(template[i])
			i++
			continue
		}
		if i+1 >= len(template) {
			// 末尾孤立的 % 原样保留，不替用户猜意图。
			b.WriteByte('%')
			i++
			continue
		}
		switch template[i+1] {
		case 'h':
			b.WriteString(host)
		case 'p':
			b.WriteString(portStr)
		case 'r':
			b.WriteString(user)
		case '%':
			b.WriteByte('%')
		default:
			// 未识别的 %x 原样写出：绝不悄悄改写用户命令。
			b.WriteByte('%')
			b.WriteByte(template[i+1])
		}
		i += 2
	}
	return b.String(), nil
}

// splitProxyFields 按空白把命令串切成 argv，支持单引号 / 双引号包裹的参数。
//
// 刻意不做反斜杠转义：Windows 路径里的 \ 是普通字符，照搬 shell 的转义规则
// 会把 C:\Users\foo 吃掉。需要参数里含引号时，用另一种引号把整段包起来即可
// （例如 'say "hi"'）。引号本身不进 argv。
//
// 未闭合的引号直接报错：宁可让用户改模板，也不要猜出一条语义完全不同的命令。
func splitProxyFields(s string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	var quote rune // 0 表示当前不在引号内
	started := false

	flush := func() {
		if started {
			fields = append(fields, cur.String())
			cur.Reset()
			started = false
		}
	}

	for _, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			// 引号（哪怕是空引号 ""）本身就标志一个参数的存在。
			started = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			started = true
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("sshx: ProxyCommand 模板有未闭合的引号 %q", string(quote))
	}
	flush()
	return fields, nil
}

// startProxyCommand 每次拨号启动一个新进程，把两条管道包装成 net.Conn。
//
// ctx 只在启动前用一次（用户已取消就不必再拉起外部程序）；启动成功后
// 不再持有它，理由见 ProxyCommandDialer 的头注释。
func startProxyCommand(ctx context.Context, argv []string, addr proxyCommandAddr) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sshx: 拨号已取消，未启动 ProxyCommand %q: %w", argv[0], err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)

	stderr := &cappedBuffer{max: proxyCommandStderrLimit}
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("sshx: 创建 ProxyCommand %q 的 stdin 管道失败: %w", argv[0], err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("sshx: 创建 ProxyCommand %q 的 stdout 管道失败: %w", argv[0], err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		// 启动失败最需要 stderr（找不到程序、权限不足、参数被拒），
		// 且错误里必须带 argv[0]，否则用户不知道是哪条命令没起来。
		return nil, fmt.Errorf("sshx: 启动 ProxyCommand %q 失败: %w%s", argv[0], err, stderrNote(stderr))
	}

	return &proxyCommandConn{
		stdin:  stdin,
		stdout: stdout,
		cmd:    cmd,
		stderr: stderr,
		argv0:  argv[0],
		addr:   addr,
	}, nil
}

// stderrNote 把收集到的 stderr 拼成错误后缀；没有内容时不拼，
// 免得错误里挂一个空的「stderr: 」占位。
func stderrNote(b *cappedBuffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	return "（stderr: " + s + "）"
}

// proxyCommandConn 把外部命令的 stdin/stdout 包装成 net.Conn。
// Read 走 stdout（命令 → SSH），Write 走 stdin（SSH → 命令）。
type proxyCommandConn struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
	stderr *cappedBuffer
	argv0  string
	addr   proxyCommandAddr

	closeOnce sync.Once
	// deadline 用定时器 + Kill 实现，见下面 SetDeadline 的注释。
	deadlineMu sync.Mutex
	deadline   *time.Timer
}

func (c *proxyCommandConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	return n, c.decorate(err)
}

func (c *proxyCommandConn) Write(p []byte) (int, error) {
	n, err := c.stdin.Write(p)
	return n, c.decorate(err)
}

// decorate 把外部命令最近 4KB 的 stderr 附到 I/O 错误上，方便排查
// cloudflared / nc 的报错。用 %w 包住原错误，errors.Is(err, io.EOF) 之类
// 的判断仍然成立，不会因为我们加了上下文就把「对端正常关流」误判成故障。
// stderr 为空时原样返回，别把一次干净的 EOF 也包一层。
func (c *proxyCommandConn) decorate(err error) error {
	if err == nil {
		return nil
	}
	note := stderrNote(c.stderr)
	if note == "" {
		return err
	}
	return fmt.Errorf("sshx: ProxyCommand %q 的 I/O 失败: %w%s", c.argv0, err, note)
}

// Close 关 stdin、关管道、等进程退出，且只做一次。
func (c *proxyCommandConn) Close() error {
	c.closeOnce.Do(func() {
		c.stopDeadline()
		// 顺序有讲究：先关 stdin，给命令一个 EOF，让它自己收尾退出；
		// 再关 stdout，把可能阻塞在 Read 上的握手 goroutine 唤醒。
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		// 命令若忽略 stdin EOF 不肯退，Wait 会一直挂着把 Close 拖死。
		// Kill 兜底；进程可能已被 ctx 取消杀掉，此时 Kill 报错一律忽略。
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		// Wait 只在这里调用一次。Start() 早在本 Conn 交出去之前就同步完成，
		// 因此不存在「Wait 抢在 Start 之前」的竞态；反过来若在启动处另起
		// goroutine 调 Wait，就会和 Start 抢 cmd 内部状态，那是必须避免的。
		// Wait 的返回值只反映进程退出方式（多半是 signal: killed），不是 I/O
		// 故障，绝不能当 Read/Write 错误往上传，这里直接丢弃。
		_ = c.cmd.Wait()
	})
	return nil
}

func (c *proxyCommandConn) LocalAddr() net.Addr  { return c.addr }
func (c *proxyCommandConn) RemoteAddr() net.Addr { return c.addr }

// SetDeadline / SetReadDeadline / SetWriteDeadline 用「到点 Kill 进程」实现。
//
// stdin/stdout 是 os.Pipe，Windows 匿名管道不支持 deadline，没别的办法让
// 一次卡住的 Read 醒来——只能杀掉写对端，让管道返回 EOF。Dial 在握手前
// 调 SetDeadline(now+Timeout)、握手成功后立刻调 SetDeadline(time.Time{})
// 清掉，所以这个定时器只覆盖握手阶段，不会误杀长会话。
//
// 三个方法不区分读写：进程只有一个，任何一侧超时都只能整体终止。
// 返回 nil 表示「已接受这个约束」，不是「和 socket 一样精确」。
func (c *proxyCommandConn) SetDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.stopDeadlineLocked()
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		// 已经过期：直接杀，别再起一个立刻触发的定时器。
		c.kill()
		return nil
	}
	c.deadline = time.AfterFunc(d, c.kill)
	return nil
}

func (c *proxyCommandConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *proxyCommandConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

// stopDeadline 只停定时器，不影响进程。Close 与重设 deadline 共用。
func (c *proxyCommandConn) stopDeadline() {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.stopDeadlineLocked()
}

func (c *proxyCommandConn) stopDeadlineLocked() {
	if c.deadline != nil {
		c.deadline.Stop()
		c.deadline = nil
	}
}

func (c *proxyCommandConn) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// proxyCommandAddr 是 net.Addr 的最小实现。
// 外部命令没有真实的 socket 对端，硬造 TCPAddr 反而会误导上层（比如拿去做
// 指纹校验的匹配）；这里用 Network="proxycmd" 明确标出「这条连接由命令承载」，
// String() 仍是 host:port，保持与 Dial 传入地址的展示一致。
type proxyCommandAddr struct {
	host string
	port int
}

func (a proxyCommandAddr) Network() string { return "proxycmd" }
func (a proxyCommandAddr) String() string {
	return net.JoinHostPort(a.host, strconv.Itoa(a.port))
}

// cappedBuffer 保留最近 max 字节的写入，供 cmd.Stderr 使用。
// exec 会为「非 *os.File 的 Stderr」另起 goroutine 拷贝，所以写入与错误里
// 读取 String() 可能并发，必须加锁。
type cappedBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= b.max {
		b.buf = append(b.buf[:0], p[n-b.max:]...)
		return n, nil
	}
	b.buf = append(b.buf, p...)
	if overflow := len(b.buf) - b.max; overflow > 0 {
		// 只保留末尾 max 字节：命令失败前刷的日志里，最后几行最有价值。
		copy(b.buf, b.buf[overflow:])
		b.buf = b.buf[:b.max]
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
