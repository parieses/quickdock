// Command sshprobe 是 QuickDock SSH 功能开工前的前置验证工具（PoC），不进产品代码。
//
// 它只回答两个问题：
//  1. x/crypto/ssh 的三条认证路径（公钥 / keyboard-interactive / 密码）在真实环境是否都通；
//  2. Wails 的 Event 通道扛不扛得住终端数据量 —— 用真实的 base64 + JSON 序列化开销来量化，
//     而不是拍脑袋估。这是 docs/proposals/ssh-feasibility.md §3.3 里唯一的未验证点。
//
// 用法（Windows 下建议用 Git Bash 或 Windows Terminal，避免中文乱码）：
//
//	go run ./cmd/sshprobe -mode auth -host 1.2.3.4 -user root -pass 'xxx'
//	go run ./cmd/sshprobe -mode exec -host 1.2.3.4 -user root -key ~/.ssh/id_ed25519 -cmd uptime
//	go run ./cmd/sshprobe -mode stream -host 1.2.3.4 -user root -pass 'xxx' -cmd htop -secs 3
//	go run ./cmd/sshprobe -mode flood -host 1.2.3.4 -user root -pass 'xxx'
//	go run ./cmd/sshprobe -mode bench            # 不需要服务器，纯本地压合批+编码
//
// 密码也可以走环境变量 SSHPROBE_PASS，避免出现在进程命令行里被 ps 看到。
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

type config struct {
	host     string
	port     int
	user     string
	pass     string
	keyPath  string
	keyPass  string
	authMode string // all | publickey | keyboard-interactive | password
	mode     string // auth | exec | stream | flood | bench
	cmd      string
	secs     int
	rows     int
	cols     int
	timeout  int
	out      string
	authLog  bool
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.host, "host", "", "服务器地址")
	flag.IntVar(&cfg.port, "port", 22, "SSH 端口")
	flag.StringVar(&cfg.user, "user", "", "登录用户名")
	flag.StringVar(&cfg.pass, "pass", os.Getenv("SSHPROBE_PASS"), "登录密码（也可用 SSHPROBE_PASS 环境变量）")
	flag.StringVar(&cfg.keyPath, "key", "", "私钥文件路径，如 ~/.ssh/id_ed25519")
	flag.StringVar(&cfg.keyPass, "key-pass", "", "私钥口令（私钥加密时才需要）")
	flag.StringVar(&cfg.authMode, "auth", "all", "强制单一认证方式：all|publickey|keyboard-interactive|password")
	flag.StringVar(&cfg.mode, "mode", "auth", "运行模式：auth|exec|stream|flood|bench")
	flag.StringVar(&cfg.cmd, "cmd", "", "exec/stream 模式要执行的命令（留空用默认值）")
	flag.IntVar(&cfg.secs, "secs", 3, "stream/flood/bench 的采集时长（秒）")
	flag.IntVar(&cfg.rows, "rows", 40, "PTY 行数")
	flag.IntVar(&cfg.cols, "cols", 120, "PTY 列数")
	flag.IntVar(&cfg.timeout, "timeout", 10, "连接超时（秒）")
	flag.StringVar(&cfg.out, "out", "", "把 PTY 原始输出写到文件（查看 escape 序列用）")
	flag.BoolVar(&cfg.authLog, "authlog", false, "打印认证交互过程")
	flag.Parse()

	var err error
	switch cfg.mode {
	case "auth":
		err = runAuth(cfg)
	case "exec":
		err = runExec(cfg)
	case "stream":
		err = runStream(cfg)
	case "flood":
		err = runStream(cfg)
	case "bench":
		err = runBench(cfg)
	default:
		err = fmt.Errorf("未知模式: %s", cfg.mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[失败] %v\n", err)
		os.Exit(1)
	}
}

// ===== 连接 =====

type handshakeInfo struct {
	dur         time.Duration
	serverVer   string
	fingerprint string
	hostKeyAlgo string
}

// dial 建立连接。刻意不复用 ssh.Dial：它没有 context，断网时会一直挂着，
// 产品代码也要走 net.Dialer + NewClientConn 这条路，这里先验一遍。
func dial(cfg *config) (*ssh.Client, *handshakeInfo, error) {
	if cfg.host == "" || cfg.user == "" {
		return nil, nil, errors.New("必须提供 -host 与 -user")
	}
	auths, err := buildAuth(cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(auths) == 0 {
		return nil, nil, errors.New("没有可用的认证方式：请提供 -pass 或 -key")
	}

	info := &handshakeInfo{}
	clientCfg := &ssh.ClientConfig{
		User: cfg.user,
		Auth: auths,
		// PoC 不校验指纹（真机上第一次连本来就未知），但会把指纹打出来供人工核对。
		// 产品代码里必须走已知主机校验 + 首次信任确认，见 knownhosts.go 的设计。
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			info.fingerprint = ssh.FingerprintSHA256(key)
			info.hostKeyAlgo = key.Type()
			return nil
		},
		Timeout: time.Duration(cfg.timeout) * time.Second,
	}
	if cfg.authLog {
		clientCfg.BannerCallback = func(msg string) error {
			fmt.Printf("  [banner] %s\n", strings.TrimSpace(msg))
			return nil
		}
	}

	addr := net.JoinHostPort(cfg.host, fmt.Sprintf("%d", cfg.port))
	d := &net.Dialer{Timeout: time.Duration(cfg.timeout) * time.Second}
	start := time.Now()

	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("TCP 连不通（网络不通/端口被墙/sshd 没起）: %w", err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("SSH 握手或认证失败（密码错/未开该认证方式/算法不兼容）: %w", err)
	}
	info.dur = time.Since(start)
	info.serverVer = string(c.ServerVersion())

	fmt.Printf("已连接 %s@%s\n", cfg.user, addr)
	fmt.Printf("  服务器版本 : %s\n", info.serverVer)
	fmt.Printf("  主机指纹   : %s\n", info.fingerprint)
	fmt.Printf("  主机算法   : %s\n", info.hostKeyAlgo)
	fmt.Printf("  握手耗时   : %v\n", info.dur)
	fmt.Printf("  认证方式   : %s\n", cfg.authMode)
	return ssh.NewClient(c, chans, reqs), info, nil
}

func buildAuth(cfg *config) ([]ssh.AuthMethod, error) {
	var pub, ki, pw ssh.AuthMethod

	if cfg.keyPath != "" {
		path := cfg.keyPath
		if strings.HasPrefix(path, "~") {
			home, err := os.UserHomeDir()
			if err == nil {
				path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读私钥失败: %w", err)
		}
		var signer ssh.Signer
		if cfg.keyPass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(cfg.keyPass))
		} else {
			signer, err = ssh.ParsePrivateKey(raw)
		}
		if err != nil {
			return nil, fmt.Errorf("解析私钥失败（口令错或格式不支持）: %w", err)
		}
		pub = ssh.PublicKeys(signer)
	}

	if cfg.pass != "" {
		ki = ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			if cfg.authLog {
				fmt.Printf("  [kbd-interactive] %s / %s，问题 %d 个\n", name, instruction, len(questions))
			}
			out := make([]string, len(questions))
			for i := range questions {
				out[i] = cfg.pass
			}
			return out, nil
		})
		pw = ssh.Password(cfg.pass)
	}

	pick := func(m ssh.AuthMethod) []ssh.AuthMethod {
		if m == nil {
			return nil
		}
		return []ssh.AuthMethod{m}
	}
	switch cfg.authMode {
	case "publickey":
		return pick(pub), nil
	case "keyboard-interactive":
		return pick(ki), nil
	case "password":
		return pick(pw), nil
	default:
		// 顺序很重要：国内云主机大量只开 keyboard-interactive，只做 password 会「密码对但连不上」。
		var out []ssh.AuthMethod
		for _, m := range []ssh.AuthMethod{pub, ki, pw} {
			if m != nil {
				out = append(out, m)
			}
		}
		return out, nil
	}
}

// ===== 模式：auth =====

func runAuth(cfg *config) error {
	client, _, err := dial(cfg)
	if err != nil {
		return err
	}
	client.Close()
	fmt.Println("\n判定: 认证链路通过")
	return nil
}

// ===== 模式：exec =====

func runExec(cfg *config) error {
	cmd := cfg.cmd
	if cmd == "" {
		cmd = "uptime"
	}
	client, _, err := dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("新建会话失败: %w", err)
	}
	defer sess.Close()

	start := time.Now()
	out, err := sess.CombinedOutput(cmd)
	elapsed := time.Since(start)

	fmt.Printf("\n$ %s\n%s", cmd, string(out))
	fmt.Printf("\n  执行耗时 : %v\n", elapsed)
	fmt.Printf("  输出字节 : %d\n", len(out))

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		fmt.Printf("  退出码   : %d（非 0，会话本身是通的）\n", exitErr.ExitStatus())
		return nil
	}
	if err != nil {
		return fmt.Errorf("执行失败: %w", err)
	}
	fmt.Println("  退出码   : 0")
	fmt.Println("\n判定: 命令执行链路通过")
	return nil
}

// ===== 模式：stream / flood（PTY）=====

func runStream(cfg *config) error {
	cmd := cfg.cmd
	if cmd == "" {
		if cfg.mode == "flood" {
			cmd = "yes | head -1000000" // 约 2MB 满速输出
		} else {
			cmd = "htop" // TUI，验证 escape 序列流
		}
	}
	client, _, err := dial(cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("新建会话失败: %w", err)
	}
	defer sess.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("xterm-256color", cfg.rows, cfg.cols, modes); err != nil {
		return fmt.Errorf("请求 PTY 失败（服务器可能禁用了 pty）: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fmt.Errorf("取 stdout 失败: %w", err)
	}

	start := time.Now()
	if err := sess.Start(cmd); err != nil {
		return fmt.Errorf("启动命令失败: %w", err)
	}
	fmt.Printf("\n已启动 PTY 会话: %s (%dx%d)\n", cmd, cfg.cols, cfg.rows)

	st := &stats{}
	b := newBatcher(st)
	ctx, cancel := context.WithCancel(context.Background())
	var bWG sync.WaitGroup
	bWG.Add(1)
	go func() {
		defer bWG.Done()
		b.run(ctx, 16*time.Millisecond)
	}()

	var file *os.File
	if cfg.out != "" {
		file, err = os.Create(cfg.out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 无法写入 %s: %v\n", cfg.out, err)
		} else {
			defer file.Close()
		}
	}

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		buf := make([]byte, 32<<10)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				st.bytes.Add(int64(n))
				st.esc.Add(int64(strings.Count(string(chunk), "\x1b")))
				b.Write(chunk)
				if file != nil {
					file.Write(chunk)
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() { waitCh <- sess.Wait() }()

	select {
	case werr := <-waitCh:
		if werr != nil {
			var exitErr *ssh.ExitError
			if !errors.As(werr, &exitErr) {
				fmt.Fprintf(os.Stderr, "会话异常结束: %v\n", werr)
			}
		}
	case <-time.After(time.Duration(cfg.secs) * time.Second):
		// TUI 不会自己退出，采集够时长就主动关掉
	}
	sess.Close()
	select {
	case <-pumpDone:
	case <-time.After(3 * time.Second):
	}
	cancel()
	bWG.Wait()
	elapsed := time.Since(start)

	report(st, elapsed, cfg.mode)
	return nil
}

// ===== 模式：bench（无需服务器）=====

func runBench(cfg *config) error {
	st := &stats{}
	b := newBatcher(st)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.run(ctx, 16*time.Millisecond)
	}()

	// 5ms 一块 20KB ≈ 4MB/s，约等于 2 万行/秒，远超真实终端负载
	const perTick = 20 << 10
	chunk := make([]byte, perTick)
	rand.Read(chunk)

	start := time.Now()
	deadline := start.Add(time.Duration(cfg.secs) * time.Second)
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for time.Now().Before(deadline) {
		<-t.C
		b.Write(chunk)
		st.bytes.Add(int64(len(chunk)))
	}
	elapsed := time.Since(start)
	cancel()
	wg.Wait()

	fmt.Printf("\n本地合成负载压测（不涉及网络，仅验证合批 + base64 + JSON 的开销）\n")
	report(st, elapsed, "bench")
	return nil
}

// ===== 统计与模拟 Event 通道 =====

type stats struct {
	bytes    atomic.Int64
	esc      atomic.Int64
	frames   atomic.Int64
	wire     atomic.Int64
	maxFrame atomic.Int64
	encodeNs atomic.Int64
}

// batcher 用真实的 base64 + json.Marshal 复刻 Event.Emit 的序列化开销。
// 目的是量出「Go 侧为推流付出的 CPU 代价」，这是 Wails 桥接里唯一能在纯 Go 里测准的部分。
type batcher struct {
	mu       sync.Mutex
	pending  []byte
	st       *stats
	frameCap int
}

func newBatcher(st *stats) *batcher {
	return &batcher{st: st, frameCap: 64 << 10}
}

func (b *batcher) Write(p []byte) {
	b.mu.Lock()
	b.pending = append(b.pending, p...)
	b.mu.Unlock()
}

func (b *batcher) run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.flush()
		case <-ctx.Done():
			b.flush()
			return
		}
	}
}

func (b *batcher) flush() {
	b.mu.Lock()
	data := b.pending
	b.pending = nil
	b.mu.Unlock()
	if len(data) == 0 {
		return
	}

	start := time.Now()
	frames, wire, maxFrame := 0, 0, 0
	for len(data) > 0 {
		n := min(len(data), b.frameCap)
		payload, err := json.Marshal(struct {
			SessionID string `json:"sessionId"`
			Data      string `json:"data"`
		}{"probe", base64.StdEncoding.EncodeToString(data[:n])})
		if err == nil {
			frames++
			wire += len(payload)
			maxFrame = max(maxFrame, len(payload))
		}
		data = data[n:]
	}
	dur := time.Since(start)

	b.st.frames.Add(int64(frames))
	b.st.wire.Add(int64(wire))
	b.st.encodeNs.Add(int64(dur))
	for {
		cur := b.st.maxFrame.Load()
		if int64(maxFrame) <= cur || b.st.maxFrame.CompareAndSwap(cur, int64(maxFrame)) {
			break
		}
	}
}

func report(st *stats, elapsed time.Duration, mode string) {
	raw := st.bytes.Load()
	wire := st.wire.Load()
	frames := st.frames.Load()
	encode := time.Duration(st.encodeNs.Load())
	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = 1e-9
	}

	avgFrame := int64(0)
	if frames > 0 {
		avgFrame = wire / frames
	}
	overhead := 0.0
	if raw > 0 {
		overhead = float64(wire-raw) / float64(raw) * 100
	}
	encodePct := float64(encode.Nanoseconds()) / float64(elapsed.Nanoseconds()) * 100

	fmt.Printf("\n===== 结果（%s）=====\n", mode)
	fmt.Printf("  原始数据   : %.2f MB (%.2f MB/s)\n", float64(raw)/(1<<20), float64(raw)/(1<<20)/secs)
	if mode != "bench" {
		fmt.Printf("  ESC 序列   : %d 个（证明 TUI 数据流正常）\n", st.esc.Load())
	}
	fmt.Printf("  合批帧数   : %d 帧（%.1f 帧/秒）\n", frames, float64(frames)/secs)
	fmt.Printf("  帧大小     : 平均 %.1f KB，最大 %.1f KB\n", float64(avgFrame)/1024, float64(st.maxFrame.Load())/1024)
	fmt.Printf("  上桥字节   : %.2f MB（base64+JSON 膨胀 %.1f%%）\n", float64(wire)/(1<<20), overhead)
	fmt.Printf("  序列化耗时 : %v（占总时长 %.2f%%）\n", encode.Round(time.Microsecond), encodePct)

	fmt.Println()
	switch {
	case encodePct < 10 && st.maxFrame.Load() < 128<<10:
		fmt.Println("  判定: 通过 —— 序列化开销可忽略，单帧在安全范围内，P1 走 Event 通道即可。")
	case encodePct < 30:
		fmt.Println("  判定: 边缘 —— 能跑但编码开销偏高，建议降低单帧上限或提高合批间隔后再压一次。")
	default:
		fmt.Println("  判定: 不通过 —— 必须切到内置 localhost WebSocket 通道（见可行性分析 §3.3）。")
	}
	if mode == "bench" {
		fmt.Println("  说明: 这是 4MB/s 的合成本地负载，真实终端通常远低于此；此结果代表上限。")
	}
}
