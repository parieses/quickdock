package sshx

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"strconv"

	"golang.org/x/crypto/ssh"

	"quickdock/internal/db"
	sshcore "quickdock/internal/sshx"
	"quickdock/services"
)

// 终端事件名。前端在这两个事件上挂监听：
//   - data：终端输出帧（高频，已合批）
//   - state：会话状态变化（连接断开、主动关闭）
const (
	EventTerminalData  = "quickdock:ssh:data"
	EventTerminalState = "quickdock:ssh:state"
)

// SshDataEvent 终端输出帧。
//
// Data 是 base64 编码的原始字节，不是字符串。原因：SSH 输出里可能夹着
// 非 UTF-8 片段（比如服务端 locale 不对时的文件名），一旦在 Go 侧强制转成
// UTF-8 字符串就会产生替换符，终端画面出现「乱码块」。前端应解码成
// Uint8Array 直接喂给 term.write()，让 xterm 自己处理字节流。
type SshDataEvent struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// SshStateEvent 会话状态变化。
type SshStateEvent struct {
	SessionID string `json:"sessionId"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

// SshConnectResult 建立终端会话的返回值。
type SshConnectResult struct {
	SessionID        string `json:"sessionId"`
	HostID           string `json:"hostId"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	User             string `json:"user"`
	ServerVersion    string `json:"serverVersion"`
	HostKeyAlgorithm string `json:"hostKeyAlgorithm"`
	Fingerprint      string `json:"fingerprint"`
}

// SshReplayResult 重开窗口时恢复画面所需的历史输出。
type SshReplayResult struct {
	Data      string `json:"data"` // base64
	Truncated bool   `json:"truncated"`
	State     string `json:"state"`
}

// SshSessionInfo 会话摘要。
//
// 门面层单独定义而不是直接回引擎的 SessionInfo：internal 的类型是实现细节，
// 让它出现在前端 bindings 里，以后改引擎字段就会连带改前端契约。
//
// HostID 是这里最关键的一个字段：窗口重开后前端要靠它把会话认回主机，
// 否则 ListSessions 只能给出一堆没有主人的 sessionId，没法自动恢复。
type SshSessionInfo struct {
	ID       string `json:"id"`
	HostID   string `json:"hostId"`
	State    string `json:"state"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Received int64  `json:"received"`
	Label    string `json:"label"`
	Started  int64  `json:"started"`
	RTTMs    int64  `json:"rttMs"`
}

// eventSink 把合批后的终端数据推到前端。
//
// 每次调用都重新取 app.App()：App 是 ServiceStartup 阶段才注入的，
// 而 Batcher 在本服务构造时就启动了，缓存指针会拿到 nil。
type eventSink struct{ app *services.AppService }

func (e eventSink) Send(sessionID string, data []byte) error {
	if e.app == nil {
		return nil
	}
	a := e.app.App()
	if a == nil {
		return nil
	}
	a.Event.Emit(EventTerminalData, SshDataEvent{
		SessionID: sessionID,
		Data:      base64.StdEncoding.EncodeToString(data),
	})
	return nil
}

// Connect 打开一个交互终端会话。
//
// 这是 SSH 功能的主入口：拨号 → 指纹校验 → 申请 PTY → 起 shell。
// 返回后前端通过事件接收输出，用 Write 发送按键，用 Replay 恢复画面。
func (s *SshService) Connect(hostID string, cols, rows int) *services.ApiResult {
	host, secret, err := s.loadHost(hostID)
	if err != nil {
		return failSsh(err)
	}
	if cols <= 0 || rows <= 0 {
		return failSsh(sshcore.ErrInvalidPtySize)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	// 走连接池：同主机的第二个标签直接复用，不会再握手一次（#13）。
	// release 必须在这个函数里归还，且恰好一次 —— Start 失败也要还，
	// 否则会留下一条没人用的连接常驻池里（泄漏，且下一个标签会复用一条没人管的生命周期）。
	client, release, err := s.pool.Acquire(ctx, host.ID, func(ctx context.Context) (*sshcore.Client, error) {
		return s.dial(ctx, host, secret)
	})
	if err != nil {
		return failSsh(err)
	}

	sess, err := s.manager.Start(client, sshcore.PtyOptions{
		Cols:  cols,
		Rows:  rows,
		Label: host.User + "@" + host.Host + ":" + strconv.Itoa(host.Port),
	})
	if err != nil {
		release()
		return failSsh(err)
	}

	s.rememberRelease(sess.ID, release)
	s.rememberOwner(sess.ID, host.ID)

	// 操作流水：用主机显示名，没起名字的主机回落到地址。
	// 只在 Start 成功后记——握手就该失败、或 PTY 申请被拒的连接不算「用过这台机」。
	label := host.Name
	if label == "" {
		label = host.Host
	}
	s.App.NoteActivity(db.ActivitySshConnected, host.ID, label)

	// 先登记再挂回调：若会话在 Start 返回前就断了，
	// SetOnStateChange 会同步触发回调，此时 release 必须已经就位。
	sid := sess.ID
	sess.SetOnStateChange(func(state sshcore.SessionState, cause error) {
		ev := SshStateEvent{SessionID: sid, State: string(state)}
		if code, msg := sessionEndCode(cause); code != "" {
			ev.ErrorCode, ev.Error = code, msg
		}
		s.emitState(ev)

		if state == sshcore.StateDisconnected || state == sshcore.StateClosed {
			s.dropRelease(sid)
		}
		// 只有「用户主动关闭」才丢主人映射：网络断开后会话还要留着回放，
		// 映射得跟着活到那时，否则重开窗口认不出这条会话属于谁。
		if state == sshcore.StateClosed {
			s.dropOwner(sid)
		}
	})

	info := client.Info()
	return services.Ok(SshConnectResult{
		SessionID:        sess.ID,
		HostID:           host.ID,
		Host:             host.Host,
		Port:             host.Port,
		User:             host.User,
		ServerVersion:    info.ServerVersion,
		HostKeyAlgorithm: info.HostKeyAlgorithm,
		Fingerprint:      info.Fingerprint,
	})
}

// Write 把用户按键写入会话。
//
// dataBase64 是 base64 编码的原始字节：xterm 的 onData 给的是已经处理过
// 组合键（方向键、Ctrl+C、粘贴）的字符串，前端需要按 UTF-8 编码后再 base64，
// 不能直接把中文塞进 JSON 字符串——终端协议只认字节。
func (s *SshService) Write(sessionID, dataBase64 string) *services.ApiResult {
	if dataBase64 == "" {
		return services.Ok(nil)
	}
	raw, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return failSshWith(errInvalidInput, map[string]interface{}{"reason": "输入数据不是合法的 base64"})
	}

	sess, ok := s.manager.Get(sessionID)
	if !ok {
		return failSsh(sshcore.ErrSessionNotFound)
	}
	if _, err := sess.Write(raw); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// Resize 同步终端尺寸。
//
// 前端必须同时做两件事：FitAddon 重算尺寸（本地渲染）+ 调用本方法（远端 TUI）。
// 漏掉后者，vim/htop 会按旧尺寸重排，看起来像花屏。
func (s *SshService) Resize(sessionID string, cols, rows int) *services.ApiResult {
	sess, ok := s.manager.Get(sessionID)
	if !ok {
		return failSsh(sshcore.ErrSessionNotFound)
	}
	if err := sess.Resize(cols, rows); err != nil {
		return failSsh(err)
	}
	return services.Ok(nil)
}

// Disconnect 关闭会话。
//
// 与「网络断开」不同：这是用户主动关闭，会话会从管理器移除，回放也一并丢弃。
//
// 契约是「调用后这条会话一定不存在」，而不是「底层 close 一定成功」。
// 连接已经死掉时 sess.Close() 会返回 EOF / "use of closed network connection"，
// 但会话其实已经从管理器摘除、目标已达成——此时报失败会让前端以为还挂着一条连接，
// 重连逻辑也会跟着走错分支。所以按后置条件判定：摘掉了就算成功。
func (s *SshService) Disconnect(sessionID string) *services.ApiResult {
	if _, ok := s.manager.Get(sessionID); !ok {
		return failSsh(sshcore.ErrSessionNotFound)
	}

	// 映射必须无条件清掉：若把清理放在错误分支后面，连接已死时这条记录
	// 会永远留在 owners 里，而它对应的会话早被摘掉，成了查不到也删不掉的僵尸。
	defer s.dropOwner(sessionID)

	closeErr := s.manager.Close(sessionID)
	if _, still := s.manager.Get(sessionID); !still {
		return services.Ok(nil)
	}
	// 还在表里说明确实没关掉，这时候才有资格把错误报出去。
	if closeErr != nil {
		return failSsh(closeErr)
	}
	return failSsh(sshcore.ErrClosed)
}

// ListSessions 列出当前全部会话（窗口重开后前端据此恢复画面）。
func (s *SshService) ListSessions() *services.ApiResult {
	items := s.manager.List()
	out := make([]SshSessionInfo, 0, len(items))
	for _, it := range items {
		out = append(out, SshSessionInfo{
			ID:       it.ID,
			HostID:   s.ownerOf(it.ID),
			State:    string(it.State),
			Cols:     it.Cols,
			Rows:     it.Rows,
			Received: it.Received,
			Label:    it.Label,
			Started:  it.Started,
			RTTMs:    it.RTTMs,
		})
	}
	// 按建连时间正序：前端取「最后一条」当恢复目标，顺序必须稳定，
	// 否则每次重开窗口恢复到的会话都不一样。
	sort.Slice(out, func(i, j int) bool { return out[i].Started < out[j].Started })
	return services.Ok(out)
}

// Replay 取某会话的历史输出，用于窗口重开后恢复画面。
func (s *SshService) Replay(sessionID string) *services.ApiResult {
	sess, ok := s.manager.Get(sessionID)
	if !ok {
		return failSsh(sshcore.ErrSessionNotFound)
	}
	return services.Ok(SshReplayResult{
		Data:      base64.StdEncoding.EncodeToString(sess.Replay()),
		Truncated: sess.ReplayTruncated(),
		State:     string(sess.State()),
	})
}

// Shutdown 关闭全部会话与合批器（应用退出时调用）。
func (s *SshService) Shutdown() {
	s.shutdownOnce.Do(func() {
		// 批次要先停：它持有独立的连接，退出后还在拨号的话会把进程拖住。
		s.cancelBatches()
		if s.manager != nil {
			s.manager.CloseAll()
		}
		if s.batcher != nil {
			s.batcher.Close()
		}

		s.releaseMu.Lock()
		s.releases = make(map[string]func())
		s.releaseMu.Unlock()
		s.pool.CloseAll()

		s.ownerMu.Lock()
		s.owners = make(map[string]string)
		s.ownerMu.Unlock()
	})
}

// ---- 内部 ----

// rememberRelease 登记「会话 → 连接归还函数」。
//
// 存函数而不是存 *Client：归还的语义是「引用 -1，归零时才关连接」，
// 池内部的引用计数才是唯一权威，调用方不该也不能自己判断该不该关。
func (s *SshService) rememberRelease(sessionID string, release func()) {
	s.releaseMu.Lock()
	s.releases[sessionID] = release
	s.releaseMu.Unlock()
}

// dropRelease 归还该会话占用的连接引用（幂等）。
//
// 幂等在 connPool.releaserFor 里保证，这里只负责「取出并清掉记录」，
// 重复调用第二次拿到 nil，什么也不做。
func (s *SshService) dropRelease(sessionID string) {
	s.releaseMu.Lock()
	release := s.releases[sessionID]
	delete(s.releases, sessionID)
	s.releaseMu.Unlock()
	if release != nil {
		release()
	}
}

func (s *SshService) rememberOwner(sessionID, hostID string) {
	s.ownerMu.Lock()
	s.owners[sessionID] = hostID
	s.ownerMu.Unlock()
}

func (s *SshService) dropOwner(sessionID string) {
	s.ownerMu.Lock()
	delete(s.owners, sessionID)
	s.ownerMu.Unlock()
}

func (s *SshService) ownerOf(sessionID string) string {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	return s.owners[sessionID]
}

func (s *SshService) emitState(ev SshStateEvent) {
	if s.App == nil {
		return
	}
	a := s.App.App()
	if a == nil {
		return
	}
	a.Event.Emit(EventTerminalState, ev)
}

// sessionEndCode 把「会话为什么结束」翻译成前端可判断的错误码。
//
// shell 以非零码退出（用户敲了 exit 1、脚本失败）是正常结束，
// 不能报成错误，否则每次退出登录都会弹一次红色警告。
func sessionEndCode(cause error) (code, msg string) {
	if cause == nil {
		return "", ""
	}
	var exitErr *ssh.ExitError
	if errors.As(cause, &exitErr) {
		return "", ""
	}
	if errors.Is(cause, io.EOF) {
		return "", ""
	}
	code, _ = classifySshError(cause)
	return code, cause.Error()
}
