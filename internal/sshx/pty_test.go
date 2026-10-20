package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ptyProbe 记录服务端实际收到的终端参数与输入。
type ptyProbe struct {
	mu sync.Mutex

	hasPty bool
	term   string
	width  int
	height int

	winCount  int
	winWidth  int
	winHeight int

	input bytes.Buffer
}

func (p *ptyProbe) recordPty(term string, w, h int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hasPty, p.term, p.width, p.height = true, term, w, h
}

func (p *ptyProbe) recordWin(w, h int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.winCount++
	p.winWidth, p.winHeight = w, h
}

func (p *ptyProbe) appendInput(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input.Write(b)
}

func (p *ptyProbe) pty() (term string, w, h int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.term, p.width, p.height, p.hasPty
}

func (p *ptyProbe) win() (count, w, h int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.winCount, p.winWidth, p.winHeight
}

func (p *ptyProbe) inputBytes() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]byte, p.input.Len())
	copy(out, p.input.Bytes())
	return out
}

// startPtySSHServer 起一个支持 pty-req / shell / window-change 的服务端。
// 它不跑真实 shell，只记录参数并按测试注入的数据回写，用来验证客户端行为。
func startPtySSHServer(t *testing.T) (host string, port int, probe *ptyProbe, inject chan []byte) {
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

	probe = &ptyProbe{}
	inject = make(chan []byte, 16)
	t.Cleanup(func() {
		// 让所有 shell goroutine 退出，避免测试结束后 goroutine 泄漏。
		close(inject)
	})

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
					go servePtyChannel(newCh, probe, inject)
				}
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, probe, inject
}

func servePtyChannel(newCh ssh.NewChannel, p *ptyProbe, inject <-chan []byte) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	// 单独读客户端输入：reqs 循环必须留着继续收 window-change，
	// 否则 resize 永远到不了服务端。
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				p.appendInput(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var payload struct {
				Term   string
				Width  uint32
				Height uint32
				PixelW uint32
				PixelH uint32
				Modes  string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			p.recordPty(payload.Term, int(payload.Width), int(payload.Height))
			req.Reply(true, nil)

		case "shell":
			req.Reply(true, nil)
			go func() {
				for data := range inject {
					if _, err := ch.Write(data); err != nil {
						return
					}
				}
			}()

		case "window-change":
			// payload 里 width 在前、height 在后——这正是客户端容易传反的地方。
			var payload struct {
				Width  uint32
				Height uint32
				PixelW uint32
				PixelH uint32
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			p.recordWin(int(payload.Width), int(payload.Height))
			req.Reply(true, nil)

		default:
			req.Reply(false, nil)
		}
	}
}

func newTestPtyManager(t *testing.T, scrollback int) (*Manager, *fakeSink, *Batcher) {
	t.Helper()
	sink := &fakeSink{}
	b := NewBatcher(sink, 5*time.Millisecond)
	t.Cleanup(b.Close)
	m := NewManager(b, scrollback)
	t.Cleanup(m.CloseAll)
	return m, sink, b
}

// TestPtyStartSendsHeightBeforeWidth 是本组测试里最关键的一条。
//
// RequestPty 的参数是 (term, h, w)，而前端 FitAddon 给的是 (cols, rows)。
// 写反了服务端不会报错，只会按错误尺寸开终端：vim 一进来就花屏、htop 的行列错位。
// 这里通过服务端实际收到的宽高反推，能直接抓到顺序错误。
func TestPtyStartSendsHeightBeforeWidth(t *testing.T) {
	host, port, probe, _ := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, 1<<20)

	s, err := m.Start(c, PtyOptions{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	waitUntil(t, "服务端收到 pty-req", func() bool {
		_, _, _, ok := probe.pty()
		return ok
	})

	term, w, h, _ := probe.pty()
	if term != DefaultTerm {
		t.Fatalf("终端类型应为 %s，实际 %q", DefaultTerm, term)
	}
	if w != 100 || h != 30 {
		t.Fatalf("尺寸传反了：期望 width=100 height=30，服务端收到 width=%d height=%d", w, h)
	}

	if info := s.Info(); info.Cols != 100 || info.Rows != 30 || info.State != StateConnected {
		t.Fatalf("会话信息不对: %+v", info)
	}
}

func TestPtyBidirectionalData(t *testing.T) {
	host, port, probe, inject := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, sink, _ := newTestPtyManager(t, 1<<20)

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	// 服务端 → 客户端：既要进回放缓冲，也要经合批发给前端。
	inject <- []byte("hello\r\n")
	waitUntil(t, "回放包含服务端输出", func() bool {
		return bytes.Contains(s.Replay(), []byte("hello"))
	})
	waitUntil(t, "合批出口收到数据", func() bool {
		return bytes.Contains(sink.drain(s.ID), []byte("hello"))
	})

	// 客户端 → 服务端。
	if _, err := s.Write([]byte("ls -l\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	waitUntil(t, "服务端收到输入", func() bool {
		return bytes.Contains(probe.inputBytes(), []byte("ls -l"))
	})

	if s.Info().Received == 0 {
		t.Fatal("应统计已接收字节数")
	}
}

// TestPtyResizeSendsColsBeforeRows 验证 Resize 的参数顺序同样没传反。
func TestPtyResizeSendsColsBeforeRows(t *testing.T) {
	host, port, probe, _ := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, 1<<20)

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("resize 失败: %v", err)
	}

	waitUntil(t, "服务端收到 window-change", func() bool {
		n, _, _ := probe.win()
		return n > 0
	})
	_, w, h := probe.win()
	if w != 120 || h != 40 {
		t.Fatalf("resize 传反了：期望 width=120 height=40，服务端收到 width=%d height=%d", w, h)
	}

	if info := s.Info(); info.Cols != 120 || info.Rows != 40 {
		t.Fatalf("会话尺寸未更新: %+v", info)
	}
}

func TestPtyReplayTruncatesOldOutput(t *testing.T) {
	host, port, _, inject := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, 16) // 故意给一个很小的回放窗口

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	for i := 0; i < 5; i++ {
		inject <- []byte("0123456789")
	}
	waitUntil(t, "回放窗口被填满", func() bool {
		return s.ReplayTruncated()
	})

	if n := len(s.Replay()); n != 16 {
		t.Fatalf("回放应只保留 16 字节，实际 %d", n)
	}
	// 必须保留最新的内容：最后 16 字节 = 第 4 次的 "456789" + 第 5 次的全部。
	if got := string(s.Replay()); got != "4567890123456789" {
		t.Fatalf("应保留最新 16 字节，实际 %q", got)
	}
}

func TestPtyInvalidOptions(t *testing.T) {
	host, port, _, _ := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, DefaultScrollback)

	if _, err := m.Start(c, PtyOptions{Cols: 0, Rows: 24}); !errors.Is(err, ErrInvalidPtySize) {
		t.Fatalf("列数为 0 应报 ErrInvalidPtySize，实际 %v", err)
	}
	if _, err := m.Start(c, PtyOptions{Cols: 80, Rows: -1}); !errors.Is(err, ErrInvalidPtySize) {
		t.Fatalf("行数为负应报 ErrInvalidPtySize，实际 %v", err)
	}

	c.Close()
	if _, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24}); !errors.Is(err, ErrClosed) {
		t.Fatalf("连接已关闭应报 ErrClosed，实际 %v", err)
	}
}

func TestPtyCloseRemovesSession(t *testing.T) {
	host, port, _, _ := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, DefaultScrollback)

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	if len(m.List()) != 1 {
		t.Fatalf("应有 1 个会话，实际 %d", len(m.List()))
	}

	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("重复关闭应幂等，实际 %v", err)
	}

	if s.State() != StateClosed {
		t.Fatalf("状态应为 closed，实际 %s", s.State())
	}
	if len(m.List()) != 0 {
		t.Fatalf("关闭后应移出管理器，实际 %d 个", len(m.List()))
	}
	if err := m.Close(s.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("已关闭的会话再关应报 ErrSessionNotFound，实际 %v", err)
	}
}

// TestPtyDisconnectKeepsSessionForReplay 连接断了但会话要留着——
// 窗口重开还能看到最后的画面，这是 #12 回放功能的前提。
func TestPtyDisconnectKeepsSessionForReplay(t *testing.T) {
	host, port, _, inject := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, DefaultScrollback)

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	inject <- []byte("last-words\r\n")
	waitUntil(t, "回放有内容", func() bool {
		return bytes.Contains(s.Replay(), []byte("last-words"))
	})

	c.Close() // 模拟网络断开 / 服务器踢人

	waitUntil(t, "状态转为断开", func() bool {
		return s.State() == StateDisconnected
	})
	if _, ok := m.Get(s.ID); !ok {
		t.Fatal("断开后会话应保留在管理器里供回放")
	}
	if !bytes.Contains(s.Replay(), []byte("last-words")) {
		t.Fatal("断开后回放内容不应丢失")
	}
}

func TestPtyWriteAfterDisconnectFails(t *testing.T) {
	host, port, _, _ := startPtySSHServer(t)
	c := dialExec(t, host, port)
	m, _, _ := newTestPtyManager(t, DefaultScrollback)

	s, err := m.Start(c, PtyOptions{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("开会话失败: %v", err)
	}
	defer s.Close()

	c.Close()
	waitUntil(t, "状态转为断开", func() bool {
		return s.State() == StateDisconnected
	})

	if _, err := s.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("断开后写入应报 ErrClosed，实际 %v", err)
	}
	if err := s.Resize(100, 30); !errors.Is(err, ErrClosed) {
		t.Fatalf("断开后 resize 应报 ErrClosed，实际 %v", err)
	}
}
