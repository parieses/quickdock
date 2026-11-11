// Package mailbox 是「邮件验证码 + 账号安全提醒卡片」面向前端的薄门面。
//
// 为什么常驻循环留在这里而不是 internal：它持有进程级单例状态（账号配置、
// 内存快照、正在跑的长连接），且要往宿主的通知中心 / Wails 事件总线推消息 ——
// 这正是 AGENTS.md 决策树第 3 条「留在 services 子包并在文件头写明理由」的情形。
// 协议细节全在 internal/mailbox，本文件只做参数校验、凭据加解密封装与调度。
//
// 多账号约定：每个账号独立持有连接状态、抓取单飞和 IDLE 长连接；验证码快照按
// Account 字段分槽合并。一个账号授权码填错、IDLE 断线或服务器挂起，都不能让
// 其它账号跟着变红或停止轮询。
package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/logger"
	internalmailbox "quickdock/internal/mailbox"
	"quickdock/internal/platform"
	"quickdock/services"
)

const (
	accountsKey    = "qd_mailbox_accounts" // 新版：账号数组
	configKey      = "qd_mailbox_config"   // 旧版单账号，只为迁移保留（后端内部写，不走 SetValue 白名单）
	pollFallback   = 5 * time.Minute       // IDLE 之外的兜底轮询
	reconnectDelay = 15 * time.Second
	maxCodes       = 40
	lookbackDays   = 2
)

// storedAccount 是落库形态：授权码单独加密，其余字段明文便于诊断。
type storedAccount struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Email   string `json:"email"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

// storedConfig 是旧版单账号落库形态，只为迁移而保留。
type storedConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Email   string `json:"email"`
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
}

// account 是一个账号的内存状态。cfg.Password 是明文，只在内存里，永不进日志。
// IDLE 长连接的 stop channel 统一放在 MailboxService.watchers，账号本身不持有，
// 避免「账号级 + 服务级」两套开关互相抢着 close。
type account struct {
	id         string
	secret     string // 已加密的授权码，落库用
	cfg        internalmailbox.Config
	connected  bool
	lastErr    string
	silentNext bool          // 首轮只填卡片、不推历史通知
	inflight   chan struct{} // 非 nil 表示正有一次抓取在跑
}

func newAccount(id string, cfg internalmailbox.Config, secret string) *account {
	return &account{id: id, secret: secret, cfg: cfg, silentNext: true}
}

type MailboxService struct {
	app *services.AppService

	mu        sync.RWMutex
	accounts  []*account
	watchers  map[string]chan struct{} // 正在跑的 IDLE 长连接，按账号 id 去重
	codes     []internalmailbox.Message
	dismissed map[string]bool

	quit     chan struct{}
	reload   chan struct{}
	stopOnce sync.Once
}

func NewMailboxService(app *services.AppService) *MailboxService {
	return &MailboxService{
		app:       app,
		watchers:  make(map[string]chan struct{}),
		dismissed: make(map[string]bool),
		quit:      make(chan struct{}),
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
		for id, ch := range s.watchers {
			delete(s.watchers, id)
			close(ch)
		}
		s.mu.Unlock()
	})
	return nil
}

// ---- 配置 ----

func (s *MailboxService) loadConfig() {
	raw, err := s.app.DB.GetSetting(accountsKey)
	if err != nil || raw == "" {
		raw = s.migrateLegacyConfig()
	}
	if raw == "" {
		return
	}
	var list []storedAccount
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		logger.W("[WRN] mailbox accounts config: %v", err)
		return
	}
	accounts := make([]*account, 0, len(list))
	for i, st := range list {
		id := st.ID
		if id == "" {
			id = fmt.Sprintf("mb-%d", i+1)
		}
		cfg := internalmailbox.Config{Host: st.Host, Port: st.Port, Email: st.Email, Enabled: st.Enabled}
		if st.Secret != "" {
			if dec, e := platform.DecryptSecret(st.Secret); e == nil {
				cfg.Password = dec
			} else {
				// 换机/换 Windows 账号后 DPAPI 解不开：保留账号但标成需重填授权码，
				// 不要静默丢掉整个配置让用户以为功能没了。
				logger.W("[WRN] mailbox secret decrypt failed account=%s (需重填授权码): %v", st.Email, e)
			}
		}
		accounts = append(accounts, newAccount(id, cfg, st.Secret))
	}
	s.mu.Lock()
	s.accounts = accounts
	s.mu.Unlock()
}

// migrateLegacyConfig 把旧版单账号配置转成数组写入新 key。旧 key 刻意保留：
// 万一新版有问题，回滚到旧构建时配置还在。写库失败也照样返回内容供本次启动使用。
func (s *MailboxService) migrateLegacyConfig() string {
	old, err := s.app.DB.GetSetting(configKey)
	if err != nil || old == "" {
		return ""
	}
	var st storedConfig
	if err := json.Unmarshal([]byte(old), &st); err != nil || st.Email == "" {
		return ""
	}
	list := []storedAccount{{
		ID: "mb-1", Host: st.Host, Port: st.Port, Email: st.Email,
		Secret: st.Secret, Enabled: st.Enabled,
	}}
	b, err := json.Marshal(list)
	if err != nil {
		return ""
	}
	if err := s.app.DB.SetSetting(accountsKey, string(b)); err != nil {
		logger.W("[WRN] mailbox migrate legacy config: %v", err)
	}
	logger.I("[mailbox] 已迁移旧版单账号配置 account=%s", st.Email)
	return string(b)
}

// saveAccounts 只做序列化：加密在 SetConfig 里已完成，避免持锁做 DPAPI。
func (s *MailboxService) saveAccounts() error {
	s.mu.RLock()
	list := make([]storedAccount, 0, len(s.accounts))
	for _, a := range s.accounts {
		list = append(list, storedAccount{
			ID: a.id, Host: a.cfg.Host, Port: a.cfg.Port, Email: a.cfg.Email,
			Secret: a.secret, Enabled: a.cfg.Enabled,
		})
	}
	s.mu.RUnlock()
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.app.DB.SetSetting(accountsKey, string(b))
}

// resolveHost 是「IMAP 服务器留空就按邮箱域名补」的唯一口径。
// SetConfig 与 TestConnection 都必须走它：两条路径曾经各自实现，测试连接漏了
// 补全，于是「按提示留空服务器 → 点测试 → 报服务器地址为空」，被误读成服务商
// 连不上。函数只补不报错，因为未启用时允许保存一份空配置，报错由调用方按场景给。
func resolveHost(host, email string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return internalmailbox.GuessHost(strings.TrimSpace(email))
	}
	return host
}

func (s *MailboxService) findAccountLocked(id string) *account {
	for _, a := range s.accounts {
		if a.id == id {
			return a
		}
	}
	return nil
}

func (s *MailboxService) accountCfg(id string) (internalmailbox.Config, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a := s.findAccountLocked(id)
	if a == nil {
		return internalmailbox.Config{}, false
	}
	return a.cfg, true
}

func (s *MailboxService) enabledIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.accounts))
	for _, a := range s.accounts {
		if a.cfg.Enabled && a.cfg.Host != "" && a.cfg.Password != "" {
			ids = append(ids, a.id)
		}
	}
	return ids
}

// GetConfig 返回不含密码的账号信息；前端永远拿不到授权码。
// 前六个聚合字段沿用旧版语义，Accounts 才是多账号下逐账号的状态。
func (s *MailboxService) GetConfig() *services.ApiResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := internalmailbox.PublicConfig{
		Accounts: make([]internalmailbox.PublicAccount, 0, len(s.accounts)),
	}
	enabled, allOK := 0, true
	for _, a := range s.accounts {
		out.Accounts = append(out.Accounts, internalmailbox.PublicAccount{
			ID: a.id, Host: a.cfg.Host, Port: a.cfg.Port, Email: a.cfg.Email,
			Enabled: a.cfg.Enabled, Connected: a.connected, LastErr: a.lastErr,
		})
		if !a.cfg.Enabled {
			continue
		}
		enabled++
		if !a.connected {
			allOK = false
		}
		if out.LastErr == "" && a.lastErr != "" {
			out.LastErr = a.lastErr
		}
	}
	if len(s.accounts) > 0 {
		first := s.accounts[0]
		out.Host, out.Port, out.Email = first.cfg.Host, first.cfg.Port, first.cfg.Email
	}
	out.Enabled = enabled > 0
	out.Connected = enabled > 0 && allOK
	return services.Ok(out)
}

// SetConfig 新增或更新一个账号并立刻重连。id 为空表示新增（邮箱重复会被拒），
// 非空表示编辑该账号（允许改邮箱地址）。password 传空串表示「沿用已保存的授权码」
// ——设置面板不回显密码，这是刻意的：连自己都别把授权码渲染到 DOM 里。
func (s *MailboxService) SetConfig(id, host string, port int, email, password string, enabled bool) *services.ApiResult {
	select {
	case <-s.quit:
		return services.FailMsg("服务已停止")
	default:
	}
	host = strings.TrimSpace(host)
	email = strings.TrimSpace(email)
	// 服务商主机让后端按域名补：前端设置面板不必再抄一份预设表。
	host = resolveHost(host, email)
	if email == "" || !strings.Contains(email, "@") {
		return services.FailMsg("邮箱地址不合法")
	}
	if enabled && host == "" {
		return services.FailMsg("无法自动识别该邮箱的 IMAP 服务器，请手动填写服务器地址")
	}
	if port <= 0 || port > 65535 {
		port = 993
	}
	var enc string
	if password != "" {
		var err error
		enc, err = platform.EncryptSecret(password)
		if err != nil {
			return services.Fail(err)
		}
	}

	s.mu.Lock()
	var a *account
	if id != "" {
		a = s.findAccountLocked(id)
		if a == nil {
			s.mu.Unlock()
			return services.FailMsg("账号不存在，请重新选择")
		}
	} else {
		for _, x := range s.accounts {
			if strings.EqualFold(strings.TrimSpace(x.cfg.Email), email) {
				s.mu.Unlock()
				return services.FailMsg("这个邮箱已经添加过了")
			}
		}
	}
	// 先算好最终密码并通过校验，再动 accounts：否则「新增但没填授权码」会在
	// 内存里留下一个空账号，前端列表凭空多一行。
	pwd := password
	if pwd == "" && a != nil {
		pwd = a.cfg.Password
	}
	if enabled && pwd == "" {
		s.mu.Unlock()
		return services.FailMsg("请先填写授权码（不是网页登录密码）")
	}
	if a == nil {
		a = newAccount(uuid.NewString(), internalmailbox.Config{}, "")
		s.accounts = append(s.accounts, a)
	}
	if password != "" {
		a.secret = enc
	}
	oldEmail := a.cfg.Email
	a.cfg = internalmailbox.Config{Host: host, Port: port, Email: email, Password: pwd, Enabled: enabled}
	// 凭据变了，旧 IDLE 连接必须换掉，否则它还拿旧授权码在后台重连。
	if ch, ok := s.watchers[a.id]; ok {
		delete(s.watchers, a.id)
		close(ch)
	}
	savedID := a.id
	s.mu.Unlock()

	if err := s.saveAccounts(); err != nil {
		return services.Fail(err)
	}
	// 改了邮箱地址时，旧账号名下残留的验证码要清掉，否则会永远挂在卡片里。
	if oldEmail != "" && !strings.EqualFold(oldEmail, email) {
		s.dropCodesOf(oldEmail)
	}
	s.signalReload()
	if enabled {
		s.ensureWatchers()
		go s.fetchAccount(savedID)
	} else {
		s.dropCodesOf(email)
		s.ensureWatchers()
		s.emitChanged()
	}
	return services.Ok(true)
}

// RemoveAccount 删除一个账号，停掉它的 IDLE 连接并清掉它名下的验证码。
func (s *MailboxService) RemoveAccount(id string) *services.ApiResult {
	select {
	case <-s.quit:
		return services.FailMsg("服务已停止")
	default:
	}
	s.mu.Lock()
	idx, email := -1, ""
	for i, a := range s.accounts {
		if a.id == id {
			idx, email = i, a.cfg.Email
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return services.FailMsg("账号不存在")
	}
	s.accounts = append(s.accounts[:idx], s.accounts[idx+1:]...)
	if ch, ok := s.watchers[id]; ok {
		delete(s.watchers, id)
		close(ch)
	}
	kept := s.codes[:0]
	for _, m := range s.codes {
		if !strings.EqualFold(m.Account, email) {
			kept = append(kept, m)
		}
	}
	s.codes = kept
	s.mu.Unlock()

	if err := s.saveAccounts(); err != nil {
		return services.Fail(err)
	}
	s.signalReload()
	s.emitChanged()
	return services.Ok(true)
}

func (s *MailboxService) dropCodesOf(email string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.codes[:0]
	for _, m := range s.codes {
		if !strings.EqualFold(m.Account, email) {
			kept = append(kept, m)
		}
	}
	s.codes = kept
}

func (s *MailboxService) signalReload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

// TestConnection 用传入的凭据试连一次，不写入配置、不抓正文。
func (s *MailboxService) TestConnection(host string, port int, email, password string) *services.ApiResult {
	if port <= 0 || port > 65535 {
		port = 993
	}
	host = resolveHost(strings.TrimSpace(host), strings.TrimSpace(email))
	// 设置面板的 IMAP 服务器允许留空（placeholder 就写着「留空自动识别常见服务商」），
	// 保存时 SetConfig 会按域名补全。测试连接曾经漏了这一步，用户只填邮箱 + 授权码
	// 点「测试连接」会得到「IMAP 服务器地址为空」，被误读成「imap.qq.com 不通」。
	// 现在两条路径共用 resolveHost，口径不可能再分叉。
	if host == "" {
		return services.FailMsg("无法自动识别该邮箱的 IMAP 服务器，请手动填写服务器地址")
	}
	cfg := internalmailbox.Config{Host: host, Port: port, Email: email, Password: password}
	if cfg.Password == "" {
		// 编辑已有账号时授权码输入框是空的：「不改密码」也要能测连接。
		s.mu.RLock()
		for _, a := range s.accounts {
			if strings.EqualFold(a.cfg.Email, email) {
				cfg.Password = a.cfg.Password
				break
			}
		}
		s.mu.RUnlock()
		if cfg.Password == "" {
			return services.FailMsg("请先填写授权码（不是网页登录密码）")
		}
	}
	return services.Wrap(true, internalmailbox.TestConnection(cfg))
}

// ---- 卡片数据 ----

// GetCodes 读内存快照：前端挂载/定时调用都是纯本地 IPC，不触发外网请求。
func (s *MailboxService) GetCodes() *services.ApiResult {
	return services.Ok(s.snapshot())
}

// GetMessage 按卡片里的 id 按需读取一封邮件原文（独立短连接 + BODY.PEEK，
// 不标已读），拉完即弃：结果不进快照、不落库、不缓存。
// id 形如 "邮箱/UID"，且必须仍在当前快照里 —— 只允许查看卡片上这封，
// 不接受前端拼任意 UID 去遍历收件箱。
func (s *MailboxService) GetMessage(id string) *services.ApiResult {
	at := strings.LastIndex(id, "/")
	if at <= 0 || at == len(id)-1 {
		return services.FailMsg("邮件编号不合法")
	}
	email := id[:at]
	uid, err := strconv.ParseUint(id[at+1:], 10, 32)
	if err != nil || uid == 0 {
		return services.FailMsg("邮件编号不合法")
	}
	s.mu.RLock()
	inSnapshot := false
	var cfg internalmailbox.Config
	for _, m := range s.codes {
		if m.ID == id {
			inSnapshot = true
			break
		}
	}
	found := false
	for _, a := range s.accounts {
		if strings.EqualFold(a.cfg.Email, email) {
			cfg, found = a.cfg, true
			break
		}
	}
	s.mu.RUnlock()
	if !inSnapshot {
		return services.FailMsg("这封邮件已不在最近列表中，请刷新后再试")
	}
	if !found || cfg.Host == "" || cfg.Password == "" {
		return services.FailMsg("该邮箱未配置或已停用，无法读取原文")
	}
	return services.Wrap(internalmailbox.FetchMessage(cfg, uint32(uid)))
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

// Refresh 手动强制抓取一次（刷新按钮 / 重试按钮），并发刷所有启用账号。
func (s *MailboxService) Refresh() *services.ApiResult {
	return services.Wrap(s.fetchAll())
}

// ---- 后台循环 ----

func (s *MailboxService) loop() {
	// 启动就记一条：配置有没有读全（尤其授权码能否解密）决定了会不会发请求，
	// 以前这里静默，抓取卡住时日志里连一点线索都没有。
	ids := s.enabledIDs()
	s.mu.RLock()
	total := len(s.accounts)
	s.mu.RUnlock()
	logger.I("[mailbox] 后台循环启动 账号数=%d 可用=%d", total, len(ids))
	ticker := time.NewTicker(pollFallback)
	defer ticker.Stop()
	s.ensureWatchers()
	for _, id := range ids {
		go s.fetchAccount(id)
	}
	for {
		select {
		case <-s.quit:
			return
		case <-s.reload:
			// SetConfig / RemoveAccount 已自行起停 watcher 与首次抓取，这里只负责唤醒后空转。
		case <-ticker.C:
			go s.fetchAll()
		}
	}
}

// ensureWatchers 保证「每个可用账号恰好一条 IDLE 长连接」。
// 用 map 去重而不是每次配置变更都盲起一条，否则反复保存会叠出一堆长连接，
// 触发 QQ 邮箱的并发连接限制，反而谁都收不到。
func (s *MailboxService) ensureWatchers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.watchers {
		a := s.findAccountLocked(id)
		if a == nil || !a.cfg.Enabled || a.cfg.Host == "" || a.cfg.Password == "" {
			delete(s.watchers, id)
			close(ch)
		}
	}
	for _, a := range s.accounts {
		if !a.cfg.Enabled || a.cfg.Host == "" || a.cfg.Password == "" {
			continue
		}
		if _, ok := s.watchers[a.id]; ok {
			continue
		}
		ch := make(chan struct{})
		s.watchers[a.id] = ch
		go s.watchLoop(a.id, ch)
	}
}

// watchLoop 持有某账号的 IDLE 长连接；断线后退避重连，重连成功必补拉一次。
func (s *MailboxService) watchLoop(id string, stop chan struct{}) {
	defer func() {
		s.mu.Lock()
		// 只有还指向自己时才删：ensureWatchers 可能已经起了新 watcher。
		if s.watchers[id] == stop {
			delete(s.watchers, id)
		}
		s.mu.Unlock()
	}()
	backoff := reconnectDelay
	for {
		select {
		case <-s.quit:
			return
		case <-stop:
			return
		default:
		}
		cfg, ok := s.accountCfg(id)
		if !ok || !cfg.Enabled || cfg.Host == "" || cfg.Password == "" {
			return
		}
		err := internalmailbox.WatchRecent(cfg, stop, nil, func() { go s.fetchAccount(id) })
		if err != nil {
			// IDLE 只是「新邮件立刻抓」的加速通道，抓取本身走独立连接。
			// 它断开不代表账号连不上，绝不能把 connected 打回 false。
			internalmailbox.LogFetchFailure(err)
			backoff = min(backoff*2, 2*time.Minute)
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
		go s.fetchAccount(id)
	}
}

// fetchAll 并发刷所有启用账号，返回合并后的快照与第一个错误（完整错误已逐条记日志）。
func (s *MailboxService) fetchAll() ([]internalmailbox.Message, error) {
	ids := s.enabledIDs()
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := s.fetchAccount(id); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	var first error
	for e := range errs {
		if first == nil {
			first = e
		}
	}
	return s.snapshot(), first
}

// fetchAccount 抓取单个账号：单飞合并在账号粒度，结果按 Account 分槽合并进全局快照。
// 全程用 id 而不是 *account 传参：抓取期间账号可能被删，重查不到就丢弃结果。
func (s *MailboxService) fetchAccount(id string) ([]internalmailbox.Message, error) {
	s.mu.Lock()
	a := s.findAccountLocked(id)
	if a == nil {
		s.mu.Unlock()
		return nil, errors.New("账号已删除")
	}
	if a.inflight != nil {
		wait := a.inflight
		s.mu.Unlock()
		<-wait
		return s.snapshot(), nil
	}
	wait := make(chan struct{})
	a.inflight = wait
	cfg := a.cfg
	silent := a.silentNext
	a.silentNext = false
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
	a = s.findAccountLocked(id)
	if a == nil {
		// 抓取期间账号被删：结果作废，别把已删账号的码塞回卡片。
		s.mu.Unlock()
		close(wait)
		return s.snapshot(), err
	}
	if err == nil {
		a.connected = true
		a.lastErr = ""
		known := make(map[string]bool, len(s.codes))
		for _, m := range s.codes {
			if m.Account == cfg.Email {
				known[m.ID] = true
			}
		}
		fresh := make([]internalmailbox.Message, 0, len(msgs))
		for _, m := range msgs {
			if s.dismissed[m.ID] {
				continue
			}
			fresh = append(fresh, m)
			if !known[m.ID] && !silent {
				s.pushNew(m)
			}
		}
		// 保留其它账号的码，只替换本账号这一槽，再整体按时间裁剪。
		combined := make([]internalmailbox.Message, 0, len(s.codes)+len(fresh))
		for _, m := range s.codes {
			if m.Account != cfg.Email {
				combined = append(combined, m)
			}
		}
		combined = append(combined, fresh...)
		sortByReceived(combined)
		if len(combined) > maxCodes {
			combined = combined[:maxCodes]
		}
		s.codes = combined
	} else {
		a.connected = false
		a.lastErr = err.Error()
		// 这轮什么都没读到：把「首轮静默」还回去，下次真同步成功时仍然不推历史码。
		a.silentNext = a.silentNext || silent
	}
	a.inflight = nil
	close(wait)
	s.mu.Unlock()

	if err != nil {
		internalmailbox.LogFetchFailure(err)
	}
	// 成败都广播：失败时前端要立刻显示具体错误，别等下一次 5 分钟轮询。
	s.emitChanged()
	return s.snapshot(), err
}

func (s *MailboxService) snapshot() []internalmailbox.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]internalmailbox.Message, len(s.codes))
	copy(out, s.codes)
	return out
}

// pushNew 把新识别出的邮件推进通知中心。安全提醒没有码，用 warn 级让通知条
// 的竖条变橙——它比「可以复制的码」更需要被看见，但不该冒充错误。
// 通知体只带发件人 + 主题 + 分类结论，不带正文（包约定：正文不落库）。
func (s *MailboxService) pushNew(m internalmailbox.Message) {
	// 持锁期间只做入队：PushNotification 会写库，别把它放进锁里。
	go func() {
		title := fmt.Sprintf("验证码 %s", m.Code)
		level := "info"
		switch m.Kind {
		case "password":
			title, level = "安全提醒：账号密码已变更", "warn"
		case "login":
			title, level = "安全提醒：账号在新设备或新地点登录", "warn"
		}
		body := m.Subject
		if m.From != "" {
			body = m.From + " · " + m.Subject
		}
		s.app.PushNotification("imap", level, title, body, "page:dashboard", m.ID)
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
