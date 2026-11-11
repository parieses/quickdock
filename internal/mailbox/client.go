// Package mailbox 提供「只读 IMAP 收件箱 + 验证码抽取」引擎。
//
// 边界（产品约定，不要顺手扩大）：
//   - 只连收件箱、只 SELECT READ-ONLY、只用 BODY.PEEK —— 绝不把邮件标成已读；
//   - 不发信、不删信、不归档、不改任何 flag；
//   - 正文只在本次调用的内存里用于识别验证码 / 账号安全事件，不落库、不外传；
//     推送到通知中心的只有发件人、主题和分类结果，绝不带正文；
//   - 「点开看原文」是唯一按需再拉一次全文的入口（FetchMessage）：仍是
//     READ-ONLY + BODY.PEEK，拉完即弃，不进快照、不缓存；只允许读快照里那封；
//     HTML 正文只经一次 IPC 交给前端，在 sandbox iframe 里清洗后渲染，不落地；
//   - 不读垃圾箱。
//
// 为什么放 internal：与外部邮件服务器打交道的协议引擎，不直接面向前端，
// 也不允许 import quickdock/services（AGENTS.md R10）。凭据加解密由 services 层做，
// 这里拿到的永远是明文授权码。
package mailbox

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"quickdock/internal/logger"
)

const (
	// 单封邮件最多读前 64KB：验证码永远在开头，没必要为一张卡片拉附件。
	maxMessageBytes int64 = 64 << 10
	// 点开看原文时放宽到 256KB：HTML 邮件 + 头 + 小附件常常超过 64KB。
	// 仍是硬上限，避免一封带大附件的邮件把 IPC 撑爆；超了就告诉前端「已截断」。
	maxDetailBytes int64 = 256 << 10
	dialTimeout          = 8 * time.Second
	// 单次抓取 / 测试连接（登录 + 搜索 + 拉 20 封）的总时限。imapclient 的
	// Wait()/Collect() 没有内置超时：服务器不回完整响应时会永久挂住，而 service
	// 层的 inflight 单飞会把后续所有抓取堵死在 <-wait —— 表现为「凭据没问题、
	// 993 连接也建着，卡片却永远空、状态永远连不上」，日志里连一条成败都没有。
	fetchTimeout = 30 * time.Second
)

// Config 是一个 IMAP 账号。Password 在 services 层落库前已加密，这里是明文。
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Enabled  bool   `json:"enabled"`
}

// Message 是一封识别出的候选邮件。卡片只收两类：
//   - kind=code：Code 非空，可直接复制的验证码；
//   - kind=password / login：账号安全事件，没有码，只作提醒，Code 恒为空。
//
// Kind 为空串按 code 处理：内存快照可能来自旧构建，前端不必区分版本。
type Message struct {
	ID       string `json:"id"`
	UID      uint32 `json:"uid"`
	Account  string `json:"account"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Code     string `json:"code"`
	Kind     string `json:"kind"`
	Preview  string `json:"preview"`
	Received int64  `json:"received"`
}

// MessageDetail 是「点开看原文」时按需拉取的一封邮件全文。
// 只存在于这次 IPC 的返回值里：不进内存快照、不落库、不缓存、不进日志。
// Text 是解码去标签后的纯文本，Raw 是含头的原始源码，Html 是保留标签的
// text/html 原文（没有 HTML 备选时为空）——前端在 sandbox iframe 里隔离渲染，
// 清洗掉脚本、事件、外链和默认远程图片后才交给 srcdoc。
type MessageDetail struct {
	ID        string `json:"id"`
	UID       uint32 `json:"uid"`
	Account   string `json:"account"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Date      string `json:"date"`
	Text      string `json:"text"`
	Html      string `json:"html"`
	Raw       string `json:"raw"`
	Truncated bool   `json:"truncated"`
}

// PublicAccount 是一个邮箱账号的展示副本：绝不含密码/授权码。
// 每个账号独立记连接状态与错误，一个账号填错授权码不该让其它账号一起显示红点。
type PublicAccount struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Email     string `json:"email"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	LastErr   string `json:"lastErr"`
}

// PublicConfig 是 GetConfig 的返回值。前六个字段是旧版单账号口径，保留给
// 「有没有启用」「能不能显示状态点」这类聚合判断；Accounts 才是多账号下的真相。
type PublicConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Email     string `json:"email"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	// 最近一次抓取失败的原因，成功时为空。前端据此显示具体错误，而不是拿一个
	// 瞬时布尔值猜「是不是授权码填错了」——connected=false 也可能只是首次同步未完成。
	LastErr string `json:"lastErr"`
	// 全部已配置账号（含禁用）。旧构建没有这个字段，前端要按 ? 处理。
	Accounts []PublicAccount `json:"accounts"`
}

// GuessHost 按邮箱域名给常见服务商的 IMAP 主机；未知域名返回空串，让用户手填。
// 这些服务商要的是「授权码 / App 专用密码」，不是网页登录密码，设置页必须写明。
func GuessHost(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	hosts := map[string]string{
		"qq.com":        "imap.qq.com",
		"foxmail.com":   "imap.qq.com",
		"163.com":       "imap.163.com",
		"126.com":       "imap.126.com",
		"yeah.net":      "imap.yeah.net",
		"exmail.qq.com": "imap.exmail.qq.com",
		"icloud.com":    "imap.mail.me.com",
		"me.com":        "imap.mail.me.com",
		"gmail.com":     "imap.gmail.com",
		"outlook.com":   "outlook.office365.com",
		"hotmail.com":   "outlook.office365.com",
		"live.com":      "outlook.office365.com",
		"office365.com": "outlook.office365.com",
	}
	return hosts[domain]
}

func (c Config) address() string {
	port := c.Port
	if port <= 0 || port > 65535 {
		port = 993
	}
	return net.JoinHostPort(strings.TrimSpace(c.Host), fmt.Sprintf("%d", port))
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("IMAP 服务器地址为空")
	}
	if strings.TrimSpace(c.Email) == "" || !strings.Contains(c.Email, "@") {
		return errors.New("邮箱地址不合法")
	}
	if c.Password == "" {
		return errors.New("授权码为空")
	}
	return nil
}

// dialReady 建立 TLS 连接并等服务器 greeting 到齐，之后才允许发任何命令。
//
// go-imap v2 的 DialTLS 是异步读 greeting 的：拿到 *Client 就发 LOGIN，命令会
// 抢在 greeting 之前到达。QQ 邮箱的 XMIMAP4Server 会直接丢掉这种命令，只回一句
// greeting，之后永远不回 tagged OK —— Login().Wait() 就此挂死。现场原始 IO：
//
//	> T1 LOGIN "..." "..."
//	< * OK [CAPABILITY ...] QQMail XMIMAP4Server ready
//	（再无 T1 OK）
//
// 而「先读 greeting 再发 LOGIN」0.5 秒即 Success login ok。库提供了 WaitGreeting()
// 正是为此；它没有显式超时，但 greeting 阶段库自带 30s 读超时兜底。
func dialReady(cfg Config, opts *imapclient.Options) (*imapclient.Client, error) {
	c, err := imapclient.DialTLS(cfg.address(), opts)
	if err != nil {
		return nil, fmt.Errorf("连接 IMAP 服务器失败: %w", err)
	}
	if err := c.WaitGreeting(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("读取 IMAP 服务器问候失败: %w", err)
	}
	return c, nil
}

// fetchGuard 是「到点强制断开底层连接」的看门狗，用来打断挂起的 Wait()/Collect()。
// hit 记录是否真的超时，让上层把 io.ErrUnexpectedEOF 这类底层噪音翻译成人话——
// 否则用户会看到「IMAP 登录失败（请确认用的是授权码）: unexpected EOF」，
// 被误导去反复重填完全正确的授权码。
type fetchGuard struct {
	stop func()
	hit  *atomic.Bool
}

func newFetchGuard(c *imapclient.Client, d time.Duration) *fetchGuard {
	done := make(chan struct{})
	var once sync.Once
	hit := &atomic.Bool{}
	go func() {
		select {
		case <-time.After(d):
			hit.Store(true)
			_ = c.Close()
		case <-done:
		}
	}()
	return &fetchGuard{stop: func() { once.Do(func() { close(done) }) }, hit: hit}
}

// FetchRecent 拉取 since 之后最近 limit 封邮件的摘要与验证码。
// 每次调用都是一条独立连接：不持有长连接，断线重连的成本就是一次 TLS 握手，
// 而「连接已死却以为还活着」才是这类功能最致命的失败。IDLE 长连接在 service 层。
func FetchRecent(cfg Config, since time.Time, limit int) (out []Message, retErr error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 60 {
		limit = 20
	}

	// IMAP 是 TCP 长连接，不走 HTTP 代理：WebView2 跟随 WinINET 代理导致前端
	// 外网全超时的问题在 Go 侧不存在。显式给 Dialer 超时，避免死端口拖住整个轮询。
	opts := &imapclient.Options{
		Dialer: &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second},
		TLSConfig: &tls.Config{
			ServerName: strings.TrimSpace(cfg.Host),
			MinVersion: tls.VersionTLS12,
		},
	}
	c, err := dialReady(cfg, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Logout 可能因为连接已断而卡住；不等它，直接关底层连接更可靠。
		_ = c.Close()
	}()

	// 总时限看门狗：到点强制断开底层连接，让卡在 Wait()/Collect() 的调用立刻带错
	// 返回，inflight 得以释放、下一轮能重试。defer 后进先出：先判超时、再停看门狗、
	// 最后关连接，正常完成时不留 goroutine。
	guard := newFetchGuard(c, fetchTimeout)
	defer guard.stop()
	defer func() {
		if retErr != nil && guard.hit.Load() {
			retErr = fmt.Errorf("等待 IMAP 服务器响应超时（%s）: %w", fetchTimeout, retErr)
		}
	}()

	if err := c.Login(cfg.Email, cfg.Password).Wait(); err != nil {
		return nil, fmt.Errorf("IMAP 登录失败（请确认用的是授权码而非登录密码）: %w", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return nil, fmt.Errorf("打开收件箱失败: %w", err)
	}

	// UID SEARCH 而非 SEARCH：序号会在邮件删除后重排，用它做去重会重复弹同一封。
	criteria := &imap.SearchCriteria{}
	if !since.IsZero() {
		criteria.Since = since
	}
	search, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("检索新邮件失败: %w", err)
	}
	uids := search.AllUIDs()
	if len(uids) == 0 {
		// 成功路径原来一个字都不记：用户看到空卡片只能怀疑「没连上」，
		// 而实际上只是近两日没有邮件。这条日志让「连上了但没码」可被自证。
		logger.I("[mailbox] 抓取完成 account=%s 近两日无新邮件", cfg.Email)
		return nil, nil
	}
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}

	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: maxMessageBytes}}
	cmd := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{
		UID:          true,
		Envelope:     true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{section},
	})
	bufs, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("读取邮件失败: %w", err)
	}

	out = make([]Message, 0, len(bufs))
	keywordMiss, securityCnt := 0, 0
	for _, b := range bufs {
		var raw []byte
		if len(b.BodySection) > 0 {
			raw = b.BodySection[0].Bytes
		}
		subject, from, body := parseMessage(raw)
		if b.Envelope != nil {
			if b.Envelope.Subject != "" {
				subject = b.Envelope.Subject
			}
			if len(b.Envelope.From) > 0 {
				a := b.Envelope.From[0]
				if a.Name != "" {
					from = a.Name
				} else if a.Mailbox != "" && a.Host != "" {
					from = a.Mailbox + "@" + a.Host
				}
			}
		}
		received := time.Now().UnixMilli()
		if !b.InternalDate.IsZero() {
			received = b.InternalDate.UnixMilli()
		}
		// 有码优先当验证码：异地登录邮件常同时带 security code，用户真正要
		// 复制的是那串码，不要把它降格成一条只读提醒。
		kind := "code"
		code := ExtractCode(subject, body)
		if code == "" {
			kind = ClassifySecurity(subject, body)
			if kind == "" {
				// 只记数量、不记正文/主题（包约定：正文不落日志）。这个数非 0 说明
				// 邮件里确实验证码关键词、但模板没被判据覆盖，是「读不到」的排障入口。
				if hasKeyword(subject) || hasKeyword(flattenText(body)) {
					keywordMiss++
				}
				continue // 既不是验证码也不是安全告警，不进卡片
			}
			securityCnt++
		}
		out = append(out, Message{
			ID:       fmt.Sprintf("%s/%d", cfg.Email, b.UID),
			UID:      uint32(b.UID),
			Account:  cfg.Email,
			From:     from,
			Subject:  subject,
			Code:     code,
			Kind:     kind,
			Preview:  previewOf(body, 80),
			Received: received,
		})
	}
	// 前四个数字回答：有没有搜到邮件、FETCH 有没有真的拿到正文、抽码有没有命中、
	// 无码的安全告警识别了几条；最后一个是「看得出是验证码邮件却没抽出码」的漏网模板。
	// 「读到=0 但候选UID>0」说明服务器没回 BODY.PEEK 段（静默失败，不报错），
	// 「含验证码=0 且安全提醒=0 且关键词未命中=0」才是正常的「最近没有相关邮件」。
	logger.I("[mailbox] 抓取完成 account=%s 候选UID=%d 读到=%d 含验证码=%d 安全提醒=%d 关键词未命中=%d",
		cfg.Email, len(uids), len(bufs), len(out)-securityCnt, securityCnt, keywordMiss)
	return out, nil
}

// FetchMessage 按 UID 按需重新抓取一封邮件的全文，供「点开看原文」用。
// 与 FetchRecent 一样是独立短连接 + READ-ONLY + BODY.PEEK，不标已读、不改 flag；
// 拉完即弃，结果不写回内存快照。绝不读垃圾箱，也只允许读当前选中的 INBOX。
func FetchMessage(cfg Config, uid uint32) (detail MessageDetail, retErr error) {
	if err := cfg.validate(); err != nil {
		return detail, err
	}
	if uid == 0 {
		return detail, errors.New("邮件 UID 无效")
	}
	opts := &imapclient.Options{
		Dialer: &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second},
		TLSConfig: &tls.Config{
			ServerName: strings.TrimSpace(cfg.Host),
			MinVersion: tls.VersionTLS12,
		},
	}
	c, err := dialReady(cfg, opts)
	if err != nil {
		return detail, err
	}
	defer func() { _ = c.Close() }()
	guard := newFetchGuard(c, fetchTimeout)
	defer guard.stop()
	defer func() {
		if retErr != nil && guard.hit.Load() {
			retErr = fmt.Errorf("等待 IMAP 服务器响应超时（%s）: %w", fetchTimeout, retErr)
		}
	}()
	if err := c.Login(cfg.Email, cfg.Password).Wait(); err != nil {
		return detail, fmt.Errorf("IMAP 登录失败（请确认用的是授权码而非登录密码）: %w", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return detail, fmt.Errorf("打开收件箱失败: %w", err)
	}

	section := &imap.FetchItemBodySection{Peek: true, Partial: &imap.SectionPartial{Offset: 0, Size: maxDetailBytes}}
	bufs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID:          true,
		Envelope:     true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		return detail, fmt.Errorf("读取邮件原文失败: %w", err)
	}
	if len(bufs) == 0 || len(bufs[0].BodySection) == 0 {
		return detail, errors.New("服务器未返回邮件正文（邮件可能已被移动或删除）")
	}
	b := bufs[0]
	raw := b.BodySection[0].Bytes
	subject, from, body := parseMessageWith(raw, true)
	if b.Envelope != nil {
		if b.Envelope.Subject != "" {
			subject = b.Envelope.Subject
		}
		if len(b.Envelope.From) > 0 {
			a := b.Envelope.From[0]
			if a.Name != "" {
				from = a.Name
			} else if a.Mailbox != "" && a.Host != "" {
				from = a.Mailbox + "@" + a.Host
			}
		}
	}
	detail = MessageDetail{
		ID:        fmt.Sprintf("%s/%d", cfg.Email, b.UID),
		UID:       uint32(b.UID),
		Account:   cfg.Email,
		From:      from,
		To:        headerValue(raw, "To"),
		Subject:   subject,
		Date:      headerValue(raw, "Date"),
		Text:      body,
		Html:      strings.ToValidUTF8(htmlBodyOf(raw), "\uFFFD"),
		Raw:       strings.ToValidUTF8(string(raw), "\uFFFD"),
		Truncated: int64(len(raw)) >= maxDetailBytes,
	}
	return detail, nil
}

// TestConnection 只验证账号能否登录与打开收件箱，不抓取任何正文。
func TestConnection(cfg Config) (retErr error) {
	if err := cfg.validate(); err != nil {
		return err
	}
	opts := &imapclient.Options{
		Dialer:    &net.Dialer{Timeout: dialTimeout},
		TLSConfig: &tls.Config{ServerName: strings.TrimSpace(cfg.Host), MinVersion: tls.VersionTLS12},
	}
	c, err := dialReady(cfg, opts)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	// 测试连接同样要有总时限：否则设置面板点一下「测试连接」可能永远转圈。
	guard := newFetchGuard(c, fetchTimeout)
	defer guard.stop()
	defer func() {
		if retErr != nil && guard.hit.Load() {
			retErr = fmt.Errorf("等待 IMAP 服务器响应超时（%s）: %w", fetchTimeout, retErr)
		}
	}()
	if err := c.Login(cfg.Email, cfg.Password).Wait(); err != nil {
		return fmt.Errorf("IMAP 登录失败（请确认用的是授权码而非登录密码）: %w", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("打开收件箱失败: %w", err)
	}
	return nil
}

// Idle 期间有新邮件到达时，imapclient 会回调 UnilateralDataHandler.Mailbox。
// WatchRecent 在一个已登录连接上开 IDLE，收到 EXISTS 或超时就返回，由 service 层
// 决定何时重新抓取（IDLE 期间不允许发其它命令，必须先 Close）。
func WatchRecent(cfg Config, stop <-chan struct{}, wake <-chan struct{}, onMail func()) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	arrived := make(chan struct{}, 1)
	handler := &imapclient.UnilateralDataHandler{
		Mailbox: func(*imapclient.UnilateralDataMailbox) {
			select {
			case arrived <- struct{}{}:
			default:
			}
		},
	}
	opts := &imapclient.Options{
		Dialer:                &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second},
		TLSConfig:             &tls.Config{ServerName: strings.TrimSpace(cfg.Host), MinVersion: tls.VersionTLS12},
		UnilateralDataHandler: handler,
	}
	c, err := dialReady(cfg, opts)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(cfg.Email, cfg.Password).Wait(); err != nil {
		return fmt.Errorf("IMAP 登录失败: %w", err)
	}
	if _, err := c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("打开收件箱失败: %w", err)
	}

	// IDLE 最长 28 分钟由库自动重启；这里另加 5 分钟兜底，防止服务器静默丢推送。
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		idle, err := c.Idle()
		if err != nil {
			return fmt.Errorf("IDLE 启用失败（服务器可能不支持）: %w", err)
		}
		// IdleCommand 没有 Done()：用 Wait() 的结果当「连接是否还活着」的信号。
		// Close() 会让 Wait() 返回，所以退出分支回收 goroutine 不会泄漏。
		waitErr := make(chan error, 1)
		go func() { waitErr <- idle.Wait() }()
		select {
		case <-stop:
			_ = idle.Close()
			<-waitErr
			return nil
		case e := <-waitErr:
			_ = idle.Close()
			if e != nil {
				return fmt.Errorf("IDLE 连接已断开: %w", e)
			}
			return errors.New("IDLE 已结束")
		case <-arrived:
		case <-wake:
		case <-ticker.C:
		}
		_ = idle.Close() // 结束 IDLE 后 service 才能安全地重新 FETCH
		<-waitErr
		onMail()
	}
}

// ---- 邮件解析 ----

// Go 的 regexp 是 RE2，不支持反向引用 \1（MustCompile 会在 init 时直接 panic）。
// 脚本/样式块单独一条、其余标签一条，宁可多匹配一点也别把整个应用拖崩。
var scriptRE = regexp.MustCompile(`(?is)<(?:script|style)[^>]*>.*?</(?:script|style)>`)
var tagRE = regexp.MustCompile(`(?s)<[^>]+>`)

// 块级标签必须换成换行而不是空格。早期 stripTags 把所有标签一律换成空格，
// HTML 邮件（没有 text/plain 备选时）会塌成**一整行**：「Your security code:」
// 和真正的码之间只隔一个空格，而同一行更前面还有 Account ID、日期、IP，
// 按「整行第一个数字」抽码必然拿到账号 ID。恢复行结构后按行扫描才成立。
var blockTagRE = regexp.MustCompile(`(?i)</?(?:br|p|div|tr|table|li|ul|ol|h[1-6]|blockquote|section|article|header|footer|hr|pre|center)[^>]*>`)

func parseMessage(raw []byte) (subject, from, body string) {
	return parseMessageWith(raw, false)
}

// plainPreferred 只给「点开看原文」用：multipart/alternative 常同时带
// text/plain 与 text/html 两份，抽码时拼在一起无所谓（关键词与码都在开头），
// 但给人看会把同一段正文重复两遍。此时只取第一份非空纯文本，没有纯文本才
// 退回第一份 HTML。抽码路径保持原行为不变，避免动到已验证的模板判据。
func parseMessageWith(raw []byte, plainPreferred bool) (subject, from, body string) {
	if len(raw) == 0 {
		return "", "", ""
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", decodeCharset("", string(raw))
	}
	dec := new(mime.WordDecoder)
	dec.CharsetReader = charsetReader
	subject, _ = dec.DecodeHeader(msg.Header.Get("Subject"))
	from, _ = dec.DecodeHeader(msg.Header.Get("From"))
	if a, err := mail.ParseAddress(from); err == nil {
		from = a.Name
		if from == "" {
			from = a.Address
		}
	}
	mediaType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	limit := maxMessageBytes
	if plainPreferred {
		limit = maxDetailBytes
	}
	body = readPart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"), params["charset"], mediaType, params["boundary"], 0, limit, plainPreferred)
	return subject, from, body
}

// headerValue 解码一个 MIME 头字段（To / Date）。Date 本身是 ASCII，
// 走同一个解码器不必单独分支，失败时退回原值而不是丢掉整行。
func headerValue(raw []byte, name string) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	dec := new(mime.WordDecoder)
	dec.CharsetReader = charsetReader
	v, err := dec.DecodeHeader(msg.Header.Get(name))
	if err != nil {
		v = msg.Header.Get(name)
	}
	return strings.TrimSpace(v)
}

func readPart(r io.Reader, encoding, charset, mediaType, boundary string, depth int, limit int64, plainPreferred bool) string {
	if depth > 4 {
		return ""
	}
	if strings.HasPrefix(mediaType, "multipart/") && boundary != "" {
		mr := multipart.NewReader(r, boundary)
		var sb strings.Builder
		htmlFallback := ""
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			ct, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if ct == "" {
				ct = "text/plain"
			}
			// 附件不要：验证码不会放在附件里，读进来只会撑大内存、制造误报。
			if strings.Contains(p.Header.Get("Content-Disposition"), "attachment") {
				_ = p.Close()
				continue
			}
			if ct == "text/plain" || ct == "text/html" || strings.HasPrefix(ct, "multipart/") {
				text := readPart(p, p.Header.Get("Content-Transfer-Encoding"), params["charset"], ct, params["boundary"], depth+1, limit, plainPreferred)
				_ = p.Close()
				if plainPreferred && strings.HasPrefix(mediaType, "multipart/alternative") {
					if ct == "text/plain" && strings.TrimSpace(text) != "" {
						return strings.TrimSpace(text)
					}
					// alternative 里也可能套着 multipart/related（HTML + 内嵌图片），
					// 不能只认 text/html，否则这类邮件的正文会被整段丢掉。
					if htmlFallback == "" && strings.TrimSpace(text) != "" {
						htmlFallback = strings.TrimSpace(text)
					}
					continue
				}
				sb.WriteString(text)
				sb.WriteString("\n")
				continue
			}
			_ = p.Close()
		}
		if htmlFallback != "" {
			return htmlFallback
		}
		return stripTags(sb.String())
	}
	text := decodePartBytes(r, encoding, charset, limit)
	if strings.Contains(mediaType, "html") {
		text = stripTags(text)
	}
	return text
}

// decodePartBytes 解一个 MIME part 的传输编码（base64 / QP）与字符集。
// readPart（抽码 / 纯文本）与 htmlPart（渲染用 HTML）共用同一套解码，避免两边
// 对同一封邮件解出不同字符集。
func decodePartBytes(r io.Reader, encoding, charset string, limit int64) string {
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		if d, e := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(data))); e == nil {
			data = d
		}
	case "quoted-printable":
		if d, e := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data))); e == nil {
			data = d
		}
	}
	return decodeCharset(charset, string(data))
}

// htmlBodyOf 取邮件里第一份 text/html 的**原始 HTML**（保留标签），供查看器
// 隔离渲染。抽码和 Text 字段继续走 readPart（会去标签），两条路径互不干扰：
// 新增渲染只影响「点开看原文」，不动任何已验证的验证码判据。
func htmlBodyOf(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	mediaType, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	return htmlPart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"), params["charset"], mediaType, params["boundary"], 0)
}

// htmlPart 沿 MIME 树找第一份非空 text/html。alternative（plain + html）、
// related（html + 内嵌图）都要能穿透；附件和带 Content-ID 的内嵌图片不是正文。
func htmlPart(r io.Reader, encoding, charset, mediaType, boundary string, depth int) string {
	if depth > 4 {
		return ""
	}
	if strings.HasPrefix(mediaType, "multipart/") && boundary != "" {
		mr := multipart.NewReader(r, boundary)
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			ct, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if ct == "" {
				ct = "text/plain"
			}
			if strings.Contains(p.Header.Get("Content-Disposition"), "attachment") || p.Header.Get("Content-ID") != "" {
				_ = p.Close()
				continue
			}
			var out string
			switch {
			case ct == "text/html":
				out = decodePartBytes(p, p.Header.Get("Content-Transfer-Encoding"), params["charset"], maxDetailBytes)
			case strings.HasPrefix(ct, "multipart/"):
				out = htmlPart(p, p.Header.Get("Content-Transfer-Encoding"), params["charset"], ct, params["boundary"], depth+1)
			}
			_ = p.Close()
			if strings.TrimSpace(out) != "" {
				return out
			}
		}
		return ""
	}
	if !strings.Contains(mediaType, "html") {
		return ""
	}
	return decodePartBytes(r, encoding, charset, maxDetailBytes)
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "gbk", "gb2312", "gb18030", "x-gbk":
		return transform.NewReader(input, simplifiedchinese.GBK.NewDecoder()), nil
	case "", "utf-8", "utf8", "us-ascii", "iso-8859-1", "latin1":
		return input, nil
	default:
		return input, nil
	}
}

func decodeCharset(charset, s string) string {
	if !utf8.ValidString(s) {
		r, err := charsetReader("gbk", strings.NewReader(s))
		if err == nil {
			if b, e := io.ReadAll(r); e == nil && utf8.Valid(b) {
				return string(b)
			}
		}
		return strings.ToValidUTF8(s, "")
	}
	if strings.EqualFold(charset, "gbk") || strings.EqualFold(charset, "gb2312") || strings.EqualFold(charset, "gb18030") {
		r, err := charsetReader(charset, strings.NewReader(s))
		if err == nil {
			if b, e := io.ReadAll(r); e == nil {
				return string(b)
			}
		}
	}
	return s
}

func stripTags(s string) string {
	s = scriptRE.ReplaceAllString(s, " ")
	s = blockTagRE.ReplaceAllString(s, "\n")
	s = tagRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	return s
}

func previewOf(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---- 验证码抽取 ----
//
// 误报比漏报更烦：订单号、账号 ID、金额、时间戳、IP 段都会撞上数字正则。
// 判据是「关键词 + 独立短码」双条件，并且**以关键词为锚向后找**，而不是
// 「整行第一个数字」——HTML 邮件被压成一行时，后者会稳定抽到 Account ID。
// 宁可漏也不乱弹：没有关键词一律返回空串。

var codeKeywords = []string{
	"验证码", "校验码", "动态密码", "动态口令", "确认码", "安全码", "一次性密码", "短信验证码",
	"verification code", "verify code", "security code", "one-time", "one time", "otp", "passcode", "pin code",
	// 各家用词差异很大：Apple 写 "Apple ID code is"，Steam 写 "Guard code is"，
	// GitHub 写 "sign-in code"。少了这些，正文里有码也会因为没关键词被整封丢掉。
	"confirmation code", "login code", "sign-in code", "sign in code", "authentication code", "auth code",
	"access code", "two-factor", "2fa", "6-digit", "6 digit", "email code", "your code",
	"code is", "id code", "guard code",
}

var (
	// Go 的 regexp 是 RE2，不支持 lookahead。混合码只能先用宽松边界抓候选，
	// 再用代码判断「必须同时含数字和字母」。
	alnumRE       = regexp.MustCompile(`(?:^|[^0-9A-Za-z])([0-9A-Za-z]{4,8})(?:[^0-9A-Za-z]|$)`)
	numericCodeRE = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4,8})(?:[^0-9]|$)`)
	// 「123-456」「902 118」这类分组码：拼成 6 位。刻意只认 3+3，
	// 免得把 2026-11-11 这种日期粘成 8 位数字当验证码。
	groupedCodeRE = regexp.MustCompile(`(?:^|[^0-9])([0-9]{3})[- ]([0-9]{3})(?:[^0-9]|$)`)
	// 候选前 24 字节内若以「Account ID / Order / Invoice / user」等结尾，
	// 这个数字属于业务编号，不是码。id 前要求非字母边界，否则 "valid 482913"
	// 会被当成 "...id " 误杀。
	idPrefixRE = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:account|order|user|customer|invoice|ticket|case|ref|reference|id|number|phone|tel|zip|postal|date|version)\s*[:#.]?\s*$`)
)

type codeHit struct {
	code  string
	score int
}

func hasKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, k := range codeKeywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// normalizeMailText 统一换行、压掉行内多余空白（HTML 标签会把 "security code"
// 拆成 "security  code"），并把被 <span> 拆开的数字粘回去（0 7 7 6 2 2 → 077622）。
func normalizeMailText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = squeezeDigitSpaces(strings.Join(strings.Fields(l), " "))
	}
	return strings.Join(lines, "\n")
}

func squeezeDigitSpaces(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' && i > 0 && i+1 < len(s) && isASCIIDigit(s[i-1]) && isASCIIDigit(s[i+1]) {
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

// flattenText 把换行和多余空白压成单个空格，只用于关键词判定与全文兜底：
// "Your security<br>code" 经块级标签转行后会被拆成两行，压平后仍是完整短语。
// 按行扫描继续用 normalizeMailText 的结果，两者分工不同，不要合并。
func flattenText(s string) string {
	return squeezeDigitSpaces(strings.Join(strings.Fields(s), " "))
}

// afterKeyword 返回**最后一个**关键词之后的文本：码几乎总在提示语后面。
func afterKeyword(s string) string {
	lower := strings.ToLower(s)
	end := 0
	for _, k := range codeKeywords {
		if i := strings.LastIndex(lower, k); i >= 0 && i+len(k) > end {
			end = i + len(k)
		}
	}
	if end > len(s) {
		end = len(s) // 极少数 Unicode 折叠会让 ToLower 改变字节长度，别切越界
	}
	return s[end:]
}

// bestCode 在一段文本里挑「最像验证码」的候选，而不是取第一个数字。
// 6 位纯数字最高分（最常见形态），离锚点越远分越低；混合码略降权。
func bestCode(s string) codeHit {
	// 起始分必须极低：codeScore 带距离衰减，码离关键词很远时会是负数，
	// 用 -1 起步会把唯一的合格候选误判成「没有码」。
	best := codeHit{score: -1 << 30}
	for _, m := range numericCodeRE.FindAllStringSubmatchIndex(s, -1) {
		c := s[m[2]:m[3]]
		if looksLikeNoise(c) || idLikeContext(s, m[2]) {
			continue
		}
		if sc := codeScore(c, m[2]); sc > best.score {
			best = codeHit{code: c, score: sc}
		}
	}
	for _, m := range groupedCodeRE.FindAllStringSubmatchIndex(s, -1) {
		c := s[m[2]:m[3]] + s[m[4]:m[5]]
		if looksLikeNoise(c) || idLikeContext(s, m[2]) {
			continue
		}
		if sc := codeScore(c, m[2]) - 2; sc > best.score {
			best = codeHit{code: c, score: sc}
		}
	}
	for _, m := range alnumRE.FindAllStringSubmatchIndex(s, -1) {
		c := s[m[2]:m[3]]
		if !isMixedCode(c) || looksLikeNoise(c) || idLikeContext(s, m[2]) {
			continue
		}
		if sc := codeScore(c, m[2]) - 5; sc > best.score {
			best = codeHit{code: c, score: sc}
		}
	}
	return best
}

func codeScore(c string, pos int) int {
	sc := 8
	if isAllDigits(c) {
		sc = 14
		if len(c) == 6 {
			sc = 40
		}
	}
	d := pos / 3
	if d > 25 {
		d = 25
	}
	return sc - d
}

func idLikeContext(s string, pos int) bool {
	if pos <= 0 {
		return false
	}
	from := pos - 24
	if from < 0 {
		from = 0
	}
	return idPrefixRE.MatchString(s[from:pos])
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}

func isMixedCode(s string) bool {
	digit, letter := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isASCIIDigit(c):
			digit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			letter = true
		default:
			return false
		}
	}
	return digit && letter
}

// standaloneCode 认「独占一行」的码：077622 / 902 118 / **482913**。
// 关键词与码分在相邻两行是最常见的模板，比拼窗口再去抽数字更准。
func standaloneCode(s string) string {
	s = strings.Trim(s, " \t*·:：.。()[]【】\"'")
	if c := strings.ReplaceAll(s, " ", ""); isCodeToken(c) {
		return c
	}
	if isCodeToken(s) {
		return s
	}
	return ""
}

func isCodeToken(s string) bool {
	if len(s) < 4 || len(s) > 8 {
		return false
	}
	if isAllDigits(s) {
		return !looksLikeNoise(s)
	}
	return isMixedCode(s) && !looksLikeNoise(s)
}

// ExtractCode 从主题 + 正文里抽一个最像验证码的短码；找不到返回空串。
func ExtractCode(subject, body string) string {
	text := normalizeMailText(body)
	lines := strings.Split(text, "\n")

	// 1) 主题里直接带码（"Your code is 482913"）。主题命中关键词却没码时
	//    必须继续看正文；早期实现会把主题里的账号 ID 当码直接返回，整封废掉。
	subj := normalizeMailText(subject)
	if hasKeyword(subj) {
		if c := bestCode(afterKeyword(subj)).code; c != "" {
			return c
		}
		if c := bestCode(subj).code; c != "" {
			return c
		}
	}

	// 2) 按行：关键词所在行向后找 → 后续几行里的纯码行 → 整行放宽。
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !hasKeyword(line) {
			continue
		}
		if c := bestCode(afterKeyword(line)).code; c != "" {
			return c
		}
		prev := ""
		for j := i + 1; j < len(lines) && j <= i+5; j++ {
			nxt := strings.TrimSpace(lines[j])
			if nxt == "" {
				continue
			}
			// "Account ID" 独占一行、下一行是 ID 的情形不能当码。
			if idPrefixRE.MatchString(prev) {
				prev = nxt
				continue
			}
			if c := standaloneCode(nxt); c != "" {
				return c
			}
			if hasKeyword(nxt) { // 到下一段提示语了，别跨段抓数字
				break
			}
			prev = nxt
		}
		if c := bestCode(line).code; c != "" {
			return c
		}
	}

	// 3) 全文兜底：压平后遍历**所有**关键词位置，各取其后 120 字窗口，选分最高的。
	//    早期只试 codeKeywords 里第一个命中的词，且无论成败都 break —— 页脚
	//    一句 "one-time payment" 就能让真正的码被跳过。
	flat := flattenText(text)
	lower := strings.ToLower(flat)
	best := codeHit{score: -1 << 30}
	for _, k := range codeKeywords {
		for from := 0; ; {
			rel := strings.Index(lower[from:], k)
			if rel < 0 {
				break
			}
			start := from + rel + len(k)
			from = start
			if start >= len(flat) {
				break
			}
			end := start + 120
			if end > len(flat) {
				end = len(flat)
			}
			if hit := bestCode(flat[start:end]); hit.code != "" && hit.score > best.score {
				best = hit
			}
		}
	}
	return best.code
}

// ---- 账号安全告警识别 ----
//
// 「密码已变更」「异地 / 新设备登录」这两类通知通常没有验证码，但属于用户需要
// 立刻看到的安全事件。它们与验证码共用一张卡片、一条通知链路，只是不显示
// 可复制的码（Code 恒为空）。绝不把 Account ID / IP 这类编号硬凑成码。
//
// 判定刻意保守：只认「已发生」的事件句，且只看主题与正文开头。验证码邮件
// 页脚几乎都写着 "If you did not request this, change your password"，
// 营销邮件写着 "reset your password" —— 这些都不是事件，必须排除，否则
// 一张只该放几条有效信息的卡片会被安全提醒淹没。

type securityRule struct {
	kind string
	re   *regexp.Regexp
}

var securityRules = []securityRule{
	// "The password for your MaxMind account was changed" / "password has been reset"
	{"password", regexp.MustCompile(`(?i)\bpassword\b[^.!?\n]{0,48}?\b(?:changed|reset|updated|modified)\b`)},
	{"password", regexp.MustCompile(`(?i)\b(?:changed|reset|updated|modified)\s+(?:your\s+|the\s+|account\s+)?password\b`)},
	{"password", regexp.MustCompile(`(?:登录密码|账号密码|帐户密码|账户密码|邮箱密码|密码)[^。！？\n]{0,12}?(?:已|被)?\s*(?:修改|重置|变更|重设|更新)`)},
	// "New sign-in from ..." / "We detected a login from an unfamiliar device"
	{"login", regexp.MustCompile(`(?i)\b(?:new|unusual|unrecognized|unfamiliar|suspicious|unknown)\s+(?:sign[- ]?in|log[- ]?in|login|device|location|activity)\b`)},
	{"login", regexp.MustCompile(`(?i)\b(?:signed\s+in|logged\s+in|sign[- ]?in|log[- ]?in|login|access)\b[^.!?\n]{0,60}?\bfrom\s+(?:an?\s+)?(?:new|unknown|unrecognized|unfamiliar|different|unusual)\b`)},
	{"login", regexp.MustCompile(`(?:异地登录|异地登陆|异常登录|新设备登录|陌生设备|非常用设备|可疑登录|登录提醒|登录设备|登录地点)`)},
}

// hypotheticalRE 匹配「假设句 / 祈使句」引导语。命中位置之前出现这些词，
// 说明后面的 password / sign-in 只是善后建议或功能入口，不是已发生的事件。
var hypotheticalRE = regexp.MustCompile(`(?i)(?:if\s+(?:you|this|it)\s+(?:did|was|were)|did\s*not\s+request|didn'?t\s+request|to\s+(?:reset|change)\s+your\s+password|please\s+(?:reset|change)|click\s+here|如非本人|若非本人|非本人操作|如果不是本人|请(?:点击|前往)?(?:重置|修改))`)

// nonEventRE 作用在**匹配到的短语本身**：句首的 "Reset your password" 是功能
// 入口，"password will be reset" 是未来时，两者都不是「密码已经被改了」。
var nonEventRE = regexp.MustCompile(`(?i)^(?:please\s+)?(?:reset|change|update|modify)\s+(?:your\s+|the\s+|account\s+)?password\b|\b(?:will|would|can|may|must|should|need\s+to)\s+(?:be\s+)?(?:reset|change|changed|updated|modified)\b`)

// ClassifySecurity 返回 "password" / "login"，都不是则返回空串。
// 正文只看开头 800 字节：页脚的安全建议与法律声明离事件最远、误报最多。
func ClassifySecurity(subject, body string) string {
	flat := flattenText(body)
	if len(flat) > 800 {
		flat = flat[:800]
		// 截断可能落在中文字符中间，回退到完整 UTF-8 边界。
		for !utf8.ValidString(flat) {
			flat = flat[:len(flat)-1]
		}
	}
	for _, scope := range []string{normalizeMailText(subject), flat} {
		if scope == "" {
			continue
		}
		for _, rule := range securityRules {
			for _, m := range rule.re.FindAllStringIndex(scope, -1) {
				from := m[0] - 90
				if from < 0 {
					from = 0
				}
				// scope 可能含中文，别把 UTF-8 序列从中间切开。
				for from > 0 && !utf8.RuneStart(scope[from]) {
					from--
				}
				if hypotheticalRE.MatchString(scope[from:m[0]]) || nonEventRE.MatchString(scope[m[0]:m[1]]) {
					continue
				}
				return rule.kind
			}
		}
	}
	return ""
}

// looksLikeNoise 排除一看就不是验证码的数字：年份、手机号片段、纯 0。
func looksLikeNoise(c string) bool {
	if strings.Trim(c, "0") == "" {
		return true
	}
	if len(c) == 4 && (strings.HasPrefix(c, "19") || strings.HasPrefix(c, "20")) {
		return true // 2026 这类年份
	}
	if len(c) >= 6 && (strings.HasPrefix(c, "138") || strings.HasPrefix(c, "139") || strings.HasPrefix(c, "186")) {
		return true // 手机号前几位
	}
	return false
}

// LogFetchFailure 统一记一条 warn：后台轮询失败不该弹窗打断用户。
func LogFetchFailure(err error) {
	logger.W("[WRN] mailbox poll: %v", err)
}
