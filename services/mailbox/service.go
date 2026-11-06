// Package mailbox 是「邮件验证码卡片」面向前端的薄门面。
//
// 为什么常驻循环留在这里而不是 internal：它持有进程级单例状态（唯一快照、
// 正在跑的长连接），且要往宿主的通知中心 / Wails 事件总线推消息 —— 这正是
// AGENTS.md 决策树第 3 条「留在 services 子包并在文件头写明理由」的情形。
// 协议细节全在 internal/mailbox，本文件只做参数校验、凭据加解密封装与调度。
package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/logger"
	internalmailbox "quickdock/internal/mailbox"
	"quickdock/internal/platform"
	"quickdock/services"
)

const (
	configKey      = "qd_mailbox_config" // 后端内部写，不走 SetValue 白名单
	pollFallback   = 5 * time.Minute     // IDLE 之外的兜底轮询
	reconnectDelay = 15 * time.Second
	maxCodes       = 40
	lookbackDays   = 2
)

// storedConfig 是落库形态：授权码单独加密，其余字段明文便于诊断。
type storedConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Email   string `json:"email"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

type MailboxService struct {
	app *services.AppService

	mu        sync.RWMutex
	cfg       internalmailbox.Config
	codes     []internalmailbox.Message
	dismissed map[string]bool
	connected bool
	lastErr   string

	inflight  chan struct{} // 非 nil 表示正有一次抓取在跑，合并并发刷新
	quit      chan struct{}
	watchStop chan struct{}
	stopOnce  sync.Once
	reload    chan struct{}
}

func NewMailboxService(app *services.AppService) *MailboxService {
	return &MailboxService{
		app:       app,
		dismissed: make(map[string]bool),
		quit:      make(chan struct{}),
		watchStop: make(chan struct{}),
		reload:    make(chan struct{}, 1),
	}
}

// ServiceStartup 由 Wails 框架按注册顺序调用（排在 AppService 之后，DB 已就绪）。
func (s *MailboxService) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	s.loadConfig()
	go s.loop()
	return nil
}

func (s *MailboxService) ServiceShutdown() error {
	s.stopOnce.Do(func() {
		close(s.quit)
		s.mu.Lock()
		if s.watchStop != nil {
			close(s.watchStop)
		}
		s.mu.Unlock()
	})
	return nil
}

// ---- 配置 ----

func (s *MailboxService) loadConfig() {
	raw, err := s.app.DB.GetSetting(configKey)
	if err != nil || raw == "" {
		return
	}
	var st storedConfig
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		logger.W("[WRN] mailbox config: %v", err)
		return
	}
	cfg := internalmailbox.Config{
		Host: st.Host, Port: st.Port, Email: st.Email, Enabled: st.Enabled,
	}
	if st.Secret != "" {
		if dec, e := platform.DecryptSecret(st.Secret); e == nil {
			cfg.Password = dec
		} else {
			logger.W("[WRN] mailbox secret decrypt failed (换机/换账号后需重填授权码): %v", e)
		}
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *MailboxService) saveConfig(cfg internalmailbox.Config) error {
	st := storedConfig{Host: cfg.Host, Port: cfg.Port, Email: cfg.Email, Enabled: cfg.Enabled}
	if cfg.Password != "" {
		enc, err := platform.EncryptSecret(cfg.Password)
		if err != nil {
			return fmt.Errorf("加密授权码失败: %w", err)
		}
		st.Secret = enc
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.app.DB.SetSetting(configKey, string(b))
}

// GetConfig 返回不含密码的账号信息；前端永远拿不到授权码。
func (s *MailboxService) GetConfig() *services.ApiResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return services.Ok(internalmailbox.PublicConfig{
		Host:      s.cfg.Host,
		Port:      s.cfg.Port,
		Email:     s.cfg.Email,
		Enabled:   s.cfg.Enabled,
		Connected: s.connected,
	})
}

// SetConfig 保存账号并立刻重连。password 传空串表示「沿用已保存的授权码」
// （设置面板不回显密码，这是刻意的：连自己都别把授权码渲染到 DOM 里）。
func (s *MailboxService) SetConfig(host string, port int, email, password string, enabled bool) *services.ApiResult {
	select {
	case <-s.quit:
		return services.FailMsg("服务已停止")
	default:
	}
	host = strings.TrimSpace(host)
	email = strings.TrimSpace(email)
	// 服务商主机让后端按域名补：前端设置面板不必再抄一份预设表，
	// 也避免用户填了邮箱却忘了填服务器导致「明明配置好了却连不上」。
	if host == "" {
		host = internalmailbox.GuessHost(email)
	}
	if email != "" && !strings.Contains(email, "@") {
		return services.FailMsg("邮箱地址不合法")
	}
	if enabled && (host == "" || email == "") {
		return services.FailMsg("启用前请填完整邮箱与 IMAP 服务器")
	}
	if port <= 0 || port > 65535 {
		port = 993
	}

	s.mu.RLock()
	old := s.cfg
	s.mu.RUnlock()
	cfg := internalmailbox.Config{Host: host, Port: port, Email: email, Enabled: enabled}
	if password == "" {
		cfg.Password = old.Password
	} else {
		cfg.Password = password
	}
	if enabled && cfg.Password == "" {
		return services.FailMsg("请先填写授权码（不是网页登录密码）")
	}
	if err := s.saveConfig(cfg); err != nil {
		return services.Fail(err)
	}

	s.mu.Lock()
	s.cfg = cfg
	prevStop := s.watchStop
	s.watchStop = make(chan struct{})
	newStop := s.watchStop
	if !enabled {
		s.codes = nil
		s.connected = false
	}
	s.mu.Unlock()

	// 关掉旧账号的长连接，换新账号重连；启用状态变化也走同一条路。
	close(prevStop)
	select {
	case s.reload <- struct{}{}:
	default:
	}
	if enabled {
		go s.watchLoop(newStop)
		go s.fetchNow()
	} else {
		// 关掉时也要广播：前端卡片应立即清空，而不是等下一次 60 秒轮询。
		s.emitChanged()
	}
	return services.Ok(true)
}

// TestConnection 用当前（或传入的）凭据试连一次，不写入配置、不抓正文。
func (s *MailboxService) TestConnection(host string, port int, email, password string) *services.ApiResult {
	if port <= 0 || port > 65535 {
		port = 993
	}
	cfg := internalmailbox.Config{
		Host: strings.TrimSpace(host), Port: port,
		Email: strings.TrimSpace(email), Password: password,
	}
	if cfg.Password == "" {
		s.mu.RLock()
		cfg.Password = s.cfg.Password
		s.mu.RUnlock()
	}
	return services.Wrap(true, internalmailbox.TestConnection(cfg))
}

// ---- 卡片数据 ----

// GetCodes 读内存快照：前端挂载/定时调用都是纯本地 IPC，不触发外网请求。
func (s *MailboxService) GetCodes() *services.ApiResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]internalmailbox.Message, len(s.codes))
	copy(out, s.codes)
	return services.Ok(out)
}

// Dismiss 手动忽略一条，之后不再出现在卡片里。
func (s *MailboxService) Dismiss(id string) *services.ApiResult {
	s.mu.Lock()
	s.dismissed[id] = true
	kept := s.codes[:0]
	for _, m := range s.codes {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	s.codes = kept
	s.mu.Unlock()
	s.emitChanged()
	return services.Ok(true)
}

func (s *MailboxService) Clear() *services.ApiResult {
	s.mu.Lock()
	for _, m := range s.codes {
		s.dismissed[m.ID] = true
	}
	s.codes = nil
	s.mu.Unlock()
	s.emitChanged()
	return services.Ok(true)
}

// Refresh 手动强制抓取一次（刷新按钮 / 重试按钮）。
func (s *MailboxService) Refresh() *services.ApiResult {
	return services.Wrap(s.fetchNow())
}

// ---- 后台循环 ----

func (s *MailboxService) loop() {
	// 没配账号时空转也不发请求；配置变更通过 reload 唤醒。
	ticker := time.NewTicker(pollFallback)
	defer ticker.Stop()
	if s.configReady() {
		go s.watchLoop(s.currentWatchStop())
		go s.fetchNow()
	}
	for {
		select {
		case <-s.quit:
			return
		case <-s.reload:
			// SetConfig 已自行起 watcher 与首次抓取，这里只负责唤醒后的空转。
		case <-ticker.C:
			if s.configReady() {
				go s.fetchNow()
			}
		}
	}
}

func (s *MailboxService) configReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Enabled && s.cfg.Host != "" && s.cfg.Password != "" && s.cfg.Email != ""
}

func (s *MailboxService) currentWatchStop() <-chan struct{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.watchStop
}

// watchLoop 持有 IDLE 长连接；断线后按固定间隔重连，重连成功必补拉一次。
func (s *MailboxService) watchLoop(stop <-chan struct{}) {
	// 有些服务商（尤其 163）不支持 IDLE：此时 WatchRecent 会很快返回错误。
	// 固定 15 秒重连等于每 15 秒一次完整登录+搜索，太吵；指数退到 2 分钟后
	// 自然退化成「慢轮询」，既不会漏验证码也不会被服务器限流。
	backoff := reconnectDelay
	for {
		select {
		case <-s.quit:
			return
		case <-stop:
			return
		default:
		}
		if !s.configReady() {
			select {
			case <-s.quit:
				return
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		s.mu.RLock()
		cfg := s.cfg
		s.mu.RUnlock()
		err := internalmailbox.WatchRecent(cfg, stop, nil, func() { go s.fetchNow() })
		if err != nil {
			internalmailbox.LogFetchFailure(err)
			s.setConnected(false, err.Error())
			backoff *= 2
			if backoff > 2*time.Minute {
				backoff = 2 * time.Minute
			}
		} else {
			backoff = reconnectDelay
		}
		select {
		case <-s.quit:
			return
		case <-stop:
			return
		case <-time.After(backoff):
		}
		// 重连成功（或服务器不支持 IDLE）时补拉一次，避免静默漏掉验证码。
		go s.fetchNow()
	}
}

// fetchNow 单飞抓取：网络请求期间不持锁，并发调用合并成一次结果。
func (s *MailboxService) fetchNow() ([]internalmailbox.Message, error) {
	s.mu.Lock()
	if s.inflight != nil {
		wait := s.inflight
		s.mu.Unlock()
		<-wait
		s.mu.RLock()
		defer s.mu.RUnlock()
		out := make([]internalmailbox.Message, len(s.codes))
		copy(out, s.codes)
		return out, nil
	}
	wait := make(chan struct{})
	s.inflight = wait
	cfg := s.cfg
	s.mu.Unlock()

	var (
		msgs []internalmailbox.Message
		err  error
	)
	if !cfg.Enabled || cfg.Host == "" || cfg.Password == "" {
		err = errors.New("邮箱验证码未启用")
	} else {
		msgs, err = internalmailbox.FetchRecent(cfg, time.Now().AddDate(0, 0, -lookbackDays), 20)
	}

	s.mu.Lock()
	if err == nil {
		s.connected = true
		s.lastErr = ""
		known := make(map[string]bool, len(s.codes))
		for _, m := range s.codes {
			known[m.ID] = true
		}
		fresh := make([]internalmailbox.Message, 0, len(msgs))
		for _, m := range msgs {
			if s.dismissed[m.ID] {
				continue
			}
			fresh = append(fresh, m)
			if !known[m.ID] {
				s.pushNewCode(m)
			}
		}
		// 按接收时间倒序，只留最近 maxCodes 条
		sortByReceived(fresh)
		if len(fresh) > maxCodes {
			fresh = fresh[:maxCodes]
		}
		s.codes = fresh
	} else {
		s.connected = false
		s.lastErr = err.Error()
	}
	s.inflight = nil
	close(wait)
	s.mu.Unlock()

	if err != nil {
		internalmailbox.LogFetchFailure(err)
	} else {
		s.emitChanged()
	}
	return s.snapshot(), err
}

func (s *MailboxService) snapshot() []internalmailbox.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]internalmailbox.Message, len(s.codes))
	copy(out, s.codes)
	return out
}

func (s *MailboxService) setConnected(ok bool, errText string) {
	s.mu.Lock()
	s.connected = ok
	s.lastErr = errText
	s.mu.Unlock()
}

func (s *MailboxService) pushNewCode(m internalmailbox.Message) {
	// 持锁期间只做入队：PushNotification 会写库，别把它放进锁里。
	go func() {
		title := fmt.Sprintf("验证码 %s", m.Code)
		body := m.Subject
		if m.From != "" {
			body = m.From + " · " + m.Subject
		}
		s.app.PushNotification("imap", "info", title, body, "page:dashboard", m.ID)
	}()
}

func (s *MailboxService) emitChanged() {
	if app := s.app.App(); app != nil {
		app.Event.Emit("mailbox:changed")
	}
}

func sortByReceived(ms []internalmailbox.Message) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].Received > ms[j].Received })
}
