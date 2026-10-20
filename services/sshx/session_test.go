package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"quickdock/internal/db"
)

// ptyRecorder 记录服务端实际收到的终端参数，用来反推客户端有没有把
// (rows, cols) 传成 (cols, rows)。
type ptyRecorder struct {
	mu sync.Mutex

	hasPty bool
	term   string
	width  int
	height int

	winCount int
	winW     int
	winH     int

	input bytes.Buffer

	// tcpConns 统计服务端接受到的 TCP 连接数，channelCount 统计会话（channel）数。
	// #13 的验收标准是「同主机开 3 个标签只建立 1 条 TCP」，只能靠这两个计数器证伪。
	tcpConns     int
	channelCount int
	openChannels int

	// conns 保留已接受的连接，供 killConns 掐断——模拟拔网线。
	conns []net.Conn
}

// killConns 从服务端侧掐断所有连接。
//
// 为什么必须走真实断连而不是在测试里手动改标志位：池把连接摘出表是靠
// Client 的断连回调驱动的，手动标 dead 绕过了回调，等于没测到那条路径。
func (r *ptyRecorder) killConns() {
	r.mu.Lock()
	list := append([]net.Conn{}, r.conns...)
	r.conns = nil
	r.mu.Unlock()
	for _, c := range list {
		_ = c.Close()
	}
}

func (r *ptyRecorder) trackConn(c net.Conn) {
	r.mu.Lock()
	r.conns = append(r.conns, c)
	r.mu.Unlock()
}

func (r *ptyRecorder) connOpened() {
	r.mu.Lock()
	r.tcpConns++
	r.mu.Unlock()
}

func (r *ptyRecorder) channelOpened() {
	r.mu.Lock()
	r.channelCount++
	r.openChannels++
	r.mu.Unlock()
}

func (r *ptyRecorder) channelClosed() {
	r.mu.Lock()
	if r.openChannels > 0 {
		r.openChannels--
	}
	r.mu.Unlock()
}

func (r *ptyRecorder) counts() (tcp, channels, open int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tcpConns, r.channelCount, r.openChannels
}

func (r *ptyRecorder) setPty(term string, w, h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hasPty, r.term, r.width, r.height = true, term, w, h
}

func (r *ptyRecorder) setWin(w, h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.winCount++
	r.winW, r.winH = w, h
}

func (r *ptyRecorder) addInput(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.input.Write(b)
}

func (r *ptyRecorder) pty() (term string, w, h int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.term, r.width, r.height, r.hasPty
}

func (r *ptyRecorder) win() (n, w, h int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.winCount, r.winW, r.winH
}

func (r *ptyRecorder) inputBytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.input.Len())
	copy(out, r.input.Bytes())
	return out
}

// startPtySvcServer 起一个支持 pty-req / shell / window-change 的测试服务端。
func startPtySvcServer(t *testing.T) (host string, port int, rec *ptyRecorder, inject chan []byte) {
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

	rec = &ptyRecorder{}
	inject = make(chan []byte, 16)
	t.Cleanup(func() { close(inject) })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			rec.connOpened()
			rec.trackConn(conn)
			go func() {
				defer conn.Close()
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					rec.channelOpened()
					go func(ch ssh.NewChannel) {
						defer rec.channelClosed()
						servePtySvcChannel(ch, rec, inject)
					}(newCh)
				}
			}()
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, rec, inject
}

func servePtySvcChannel(newCh ssh.NewChannel, rec *ptyRecorder, inject <-chan []byte) {
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				rec.addInput(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p struct {
				Term          string
				Width, Height uint32
				PixW, PixH    uint32
				Modes         string
			}
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			rec.setPty(p.Term, int(p.Width), int(p.Height))
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
			var p struct{ Width, Height, PixW, PixH uint32 }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			rec.setWin(int(p.Width), int(p.Height))
			req.Reply(true, nil)

		default:
			req.Reply(false, nil)
		}
	}
}

func waitSvcUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时等待: %s", what)
}

// saveTrustedHost 建一台主机并预先信任服务端指纹（模拟「以前连过」）。
func saveTrustedHost(t *testing.T, svc *SshService, host string, port int) *db.SshHost {
	t.Helper()

	h := svc.SaveHost(SshHostInput{
		Name: "测试机", Host: host, Port: port, User: "root",
		AuthType: "password", Password: "pw", Env: "test",
	}).Data.(*db.SshHost)

	// 先连一次拿到指纹并信任，否则 Connect 会停在「待确认」。
	r := svc.TestHost(h.ID)
	res, ok := r.Data.(TestHostResult)
	if !ok || !res.NeedsTrust {
		t.Fatalf("期望首次连接返回 needsTrust，实际 %+v", r)
	}
	if tr := svc.TrustFingerprint(host, port, res.Fingerprint); tr.Code != 0 {
		t.Fatalf("信任指纹失败: %+v", tr)
	}
	return h
}

// TestConnectTerminalLifecycle 覆盖终端的完整生命周期。
func TestConnectTerminalLifecycle(t *testing.T) {
	svc := newTestService(t)
	host, port, rec, inject := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	r := svc.Connect(h.ID, 100, 30)
	if r.Code != 0 {
		t.Fatalf("建立终端失败: %+v", r)
	}
	cr := r.Data.(SshConnectResult)
	if cr.SessionID == "" {
		t.Fatal("应返回会话 ID")
	}
	if cr.ServerVersion == "" {
		t.Fatal("应带回服务器版本，前端状态条要展示")
	}

	// pty-req 的宽高顺序：写反了 vim 会花屏。
	waitSvcUntil(t, "服务端收到 pty-req", func() bool {
		_, _, _, ok := rec.pty()
		return ok
	})
	term, w, hgt, _ := rec.pty()
	if term != "xterm-256color" {
		t.Fatalf("终端类型不对: %q", term)
	}
	if w != 100 || hgt != 30 {
		t.Fatalf("尺寸传反了：期望 width=100 height=30，实际 width=%d height=%d", w, hgt)
	}

	// 服务端输出 → Replay 能取到（窗口重开靠这个恢复画面）。
	inject <- []byte("hello-terminal\r\n")
	waitSvcUntil(t, "回放包含服务端输出", func() bool {
		rr := svc.Replay(cr.SessionID)
		if rr.Code != 0 {
			return false
		}
		raw, err := base64.StdEncoding.DecodeString(rr.Data.(SshReplayResult).Data)
		return err == nil && bytes.Contains(raw, []byte("hello-terminal"))
	})

	// 按键 → 服务端收到。
	keys := base64.StdEncoding.EncodeToString([]byte("ls -l\n"))
	if r := svc.Write(cr.SessionID, keys); r.Code != 0 {
		t.Fatalf("写入失败: %+v", r)
	}
	waitSvcUntil(t, "服务端收到按键", func() bool {
		return bytes.Contains(rec.inputBytes(), []byte("ls -l"))
	})

	// resize：同样验证宽高顺序。
	if r := svc.Resize(cr.SessionID, 120, 40); r.Code != 0 {
		t.Fatalf("resize 失败: %+v", r)
	}
	waitSvcUntil(t, "服务端收到 window-change", func() bool {
		n, _, _ := rec.win()
		return n > 0
	})
	if _, w, hgt := rec.win(); w != 120 || hgt != 40 {
		t.Fatalf("resize 传反了：期望 width=120 height=40，实际 width=%d height=%d", w, hgt)
	}

	// 会话列表。
	lr := svc.ListSessions()
	if list := lr.Data.([]SshSessionInfo); len(list) != 1 || list[0].ID != cr.SessionID {
		t.Fatalf("会话列表不对: %+v", list)
	}

	// 主动断开 → 移除，且后续操作报「会话不存在」。
	if r := svc.Disconnect(cr.SessionID); r.Code != 0 {
		t.Fatalf("断开失败: %+v", r)
	}
	if list := svc.ListSessions().Data.([]SshSessionInfo); len(list) != 0 {
		t.Fatalf("断开后应为空，实际 %d 个", len(list))
	}
	if got := errorCode(svc.Replay(cr.SessionID)); got != CodeSessionNotFound {
		t.Fatalf("已断开的会话取回放应报 %s，实际 %s", CodeSessionNotFound, got)
	}
}

// TestConnectUntrustedHostNeedsTrust 终端入口也要能识别「主机未信任」。
func TestConnectUntrustedHostNeedsTrust(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)

	h := svc.SaveHost(SshHostInput{
		Name: "新机", Host: host, Port: port, User: "root",
		AuthType: "password", Password: "pw",
	}).Data.(*db.SshHost)

	r := svc.Connect(h.ID, 80, 24)
	if r.Code != 1 {
		t.Fatalf("未信任主机应先失败: %+v", r)
	}
	if got := errorCode(r); got != CodeHostKeyUnknown {
		t.Fatalf("期望 %s，实际 %s", CodeHostKeyUnknown, got)
	}
	if fp, _ := r.Data.(map[string]interface{})["fingerprint"].(string); !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("应带回指纹供前端确认: %+v", r.Data)
	}
}

func TestTerminalValidation(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	// 文件夹不能连。
	folder := svc.SaveHost(SshHostInput{Name: "目录", IsFolder: true}).Data.(*db.SshHost)
	if got := errorCode(svc.Connect(folder.ID, 80, 24)); got != CodeFolderNotConnectable {
		t.Fatalf("文件夹应报 %s，实际 %s", CodeFolderNotConnectable, got)
	}

	// 尺寸非法。
	if got := errorCode(svc.Connect(h.ID, 0, 24)); got != CodeInvalidPtySize {
		t.Fatalf("列数 0 应报 %s，实际 %s", CodeInvalidPtySize, got)
	}

	// 未知会话 ID。
	if got := errorCode(svc.Write("nope", base64.StdEncoding.EncodeToString([]byte("x")))); got != CodeSessionNotFound {
		t.Fatalf("未知会话写入应报 %s，实际 %s", CodeSessionNotFound, got)
	}
	if got := errorCode(svc.Resize("nope", 80, 24)); got != CodeSessionNotFound {
		t.Fatalf("未知会话 resize 应报 %s，实际 %s", CodeSessionNotFound, got)
	}
	if got := errorCode(svc.Disconnect("nope")); got != CodeSessionNotFound {
		t.Fatalf("未知会话断开应报 %s，实际 %s", CodeSessionNotFound, got)
	}

	// 非法 base64 输入。
	sess := svc.Connect(h.ID, 80, 24)
	if sess.Code != 0 {
		t.Fatalf("建立终端失败: %+v", sess)
	}
	sid := sess.Data.(SshConnectResult).SessionID
	defer svc.Disconnect(sid)

	if got := errorCode(svc.Write(sid, "not-base64!!")); got != CodeInvalidInput {
		t.Fatalf("非法 base64 应报 %s，实际 %s", CodeInvalidInput, got)
	}
}

// TestListSessionsCarriesHostIdentity 会话摘要必须能认回主机。
//
// 窗口重开后前端靠 ListSessions 决定「接回哪条会话」。缺了 hostId/label/started，
// 前端拿到一堆没主人的 sessionId，只能重新连一次——那用户关一次窗就白连一次。
func TestListSessionsCarriesHostIdentity(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	r := svc.Connect(h.ID, 100, 30)
	if r.Code != 0 {
		t.Fatalf("建立终端失败: %+v", r)
	}
	sid := r.Data.(SshConnectResult).SessionID

	list := svc.ListSessions().Data.([]SshSessionInfo)
	if len(list) != 1 {
		t.Fatalf("应有 1 条会话，实际 %d", len(list))
	}
	got := list[0]
	if got.ID != sid {
		t.Fatalf("会话 ID 不匹配: %s != %s", got.ID, sid)
	}
	if got.HostID != h.ID {
		t.Fatalf("认不回主机：期望 hostId=%s，实际 %q", h.ID, got.HostID)
	}
	if want := "root@" + host + ":" + itoa(port); got.Label != want {
		t.Fatalf("标签不对：期望 %q，实际 %q", want, got.Label)
	}
	if got.Started <= 0 {
		t.Fatalf("应带回建连时刻供算连接时长，实际 %d", got.Started)
	}
	if got.Cols != 100 || got.Rows != 30 {
		t.Fatalf("尺寸不对: %dx%d", got.Cols, got.Rows)
	}
	// RTT 还没测到（保活默认 45s），必须是 0 而不是负数或垃圾值。
	if got.RTTMs != 0 {
		t.Fatalf("未保活时 RTT 应为 0，实际 %d", got.RTTMs)
	}
}

// TestListSessionsOrderedByStart 多条会话的顺序必须稳定。
//
// 前端取「最后一条」当恢复目标；Manager.List 走 map 遍历，顺序随机。
// 不排序的话每次重开窗口恢复到的会话都不一样。
func TestListSessionsOrderedByStart(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	var ids []string
	for i := 0; i < 3; i++ {
		r := svc.Connect(h.ID, 80, 24)
		if r.Code != 0 {
			t.Fatalf("第 %d 次建立终端失败: %+v", i, r)
		}
		ids = append(ids, r.Data.(SshConnectResult).SessionID)
	}
	defer func() {
		for _, id := range ids {
			svc.Disconnect(id)
		}
	}()

	for round := 0; round < 5; round++ {
		list := svc.ListSessions().Data.([]SshSessionInfo)
		if len(list) != 3 {
			t.Fatalf("应有 3 条会话，实际 %d", len(list))
		}
		for i, info := range list {
			if info.ID != ids[i] {
				t.Fatalf("第 %d 轮顺序不稳定：位置 %d 期望 %s，实际 %s", round, i, ids[i], info.ID)
			}
		}
	}
}

// TestOwnerMappingOutlivesDisconnect 断网后主人映射要留着。
//
// 断开的会话仍留在 Manager 里供回放；如果这时把映射删了，窗口重开后
// ListSessions 就认不出它属于哪台主机，恢复功能等于没有。
func TestOwnerMappingOutlivesDisconnect(t *testing.T) {
	svc := newTestService(t)
	host, port, _, _ := startPtySvcServer(t)
	h := saveTrustedHost(t, svc, host, port)

	r := svc.Connect(h.ID, 80, 24)
	if r.Code != 0 {
		t.Fatalf("建立终端失败: %+v", r)
	}
	sid := r.Data.(SshConnectResult).SessionID

	svc.rememberOwner(sid, h.ID)
	svc.dropRelease(sid) // 模拟连接断开（归还连接引用）

	list := svc.ListSessions().Data.([]SshSessionInfo)
	var found *SshSessionInfo
	for i := range list {
		if list[i].ID == sid {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("断连后会话应仍在列表里（供回放）")
	}
	if found.HostID != h.ID {
		t.Fatalf("断连后丢失主人映射：期望 %s，实际 %q", h.ID, found.HostID)
	}

	// 用户主动关闭才清映射。
	if r := svc.Disconnect(sid); r.Code != 0 {
		t.Fatalf("断开失败: %+v", r)
	}
	if got := svc.ownerOf(sid); got != "" {
		t.Fatalf("主动关闭后应清掉主人映射，实际 %q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
