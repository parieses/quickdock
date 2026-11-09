// Package mailbox 提供「只读 IMAP 收件箱 + 验证码抽取」引擎。
//
// 边界（产品约定，不要顺手扩大）：
//   - 只连收件箱、只 SELECT READ-ONLY、只用 BODY.PEEK —— 绝不把邮件标成已读；
//   - 不发信、不删信、不归档、不改任何 flag；
//   - 正文只在本次调用的内存里用于识别验证码，不落库、不外传；
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
	dialTimeout           = 8 * time.Second
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

// Message 是一封识别出的候选邮件。只有 Code 非空的会进卡片。
type Message struct {
	ID       string `json:"id"`
	UID      uint32 `json:"uid"`
	Account  string `json:"account"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Code     string `json:"code"`
	Preview  string `json:"preview"`
	Received int64  `json:"received"`
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
		code := ExtractCode(subject, body)
		if code == "" {
			continue // 卡片只收验证码邮件，不变成邮件客户端
		}
		out = append(out, Message{
			ID:       fmt.Sprintf("%s/%d", cfg.Email, b.UID),
			UID:      uint32(b.UID),
			Account:  cfg.Email,
			From:     from,
			Subject:  subject,
			Code:     code,
			Preview:  previewOf(body, 80),
			Received: received,
		})
	}
	// 三个数字分别回答：有没有搜到邮件、FETCH 有没有真的拿到正文、抽码有没有命中。
	// 「读到=0 但候选UID>0」说明服务器没回 BODY.PEEK 段（静默失败，不报错），
	// 「含验证码=0」才是正常的「最近没收到验证码」。排障时这三者不能混为一谈。
	logger.I("[mailbox] 抓取完成 account=%s 候选UID=%d 读到=%d 含验证码=%d",
		cfg.Email, len(uids), len(bufs), len(out))
	return out, nil
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

func parseMessage(raw []byte) (subject, from, body string) {
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
	body = readPart(msg.Body, msg.Header.Get("Content-Transfer-Encoding"), params["charset"], mediaType, params["boundary"], 0)
	return subject, from, body
}

func readPart(r io.Reader, encoding, charset, mediaType, boundary string, depth int) string {
	if depth > 4 {
		return ""
	}
	if strings.HasPrefix(mediaType, "multipart/") && boundary != "" {
		mr := multipart.NewReader(r, boundary)
		var sb strings.Builder
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
				sb.WriteString(readPart(p, p.Header.Get("Content-Transfer-Encoding"), params["charset"], ct, params["boundary"], depth+1))
				sb.WriteString("\n")
			}
			_ = p.Close()
		}
		return stripTags(sb.String())
	}
	data, err := io.ReadAll(io.LimitReader(r, maxMessageBytes))
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
	text := decodeCharset(charset, string(data))
	if strings.Contains(mediaType, "html") {
		text = stripTags(text)
	}
	return text
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
// 误报比漏报更烦：订单号、手机号后四位、金额、时间戳都会撞上数字正则。
// 策略是「关键词 + 独立短码」双条件，且优先同一行内的码；宁可漏也不乱弹。

var codeKeywords = []string{
	"验证码", "校验码", "动态密码", "动态口令", "确认码", "安全码", "一次性密码", "短信验证码",
	"verification code", "verify code", "security code", "one-time", "one time", "otp", "passcode", "pin code",
}

// 注意 Go 的 regexp 是 RE2，不支持 lookahead。混合码只能先用宽松边界抓候选，
// 再在 firstCode 里用代码判断「必须同时含数字和字母」。
var alnumRE = regexp.MustCompile(`(?:^|[^0-9A-Za-z])([0-9A-Za-z]{4,8})(?:[^0-9A-Za-z]|$)`)

var numericCodeRE = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4,8})(?:[^0-9]|$)`)

func hasKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, k := range codeKeywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// ExtractCode 从主题 + 正文里抽一个最像验证码的短码；找不到返回空串。
func ExtractCode(subject, body string) string {
	if hasKeyword(subject) {
		if c := firstCode(subject); c != "" {
			return c
		}
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r", "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 同一行既有关键词又有独立短码 —— 最可靠的形态。
		if hasKeyword(line) {
			if c := firstCode(line); c != "" && !looksLikeNoise(c) {
				return c
			}
			// 「验证码是：」和码分在相邻两行也很常见。
			window := line
			if i+1 < len(lines) {
				window += " " + strings.TrimSpace(lines[i+1])
			}
			if c := firstCode(window); c != "" && !looksLikeNoise(c) {
				return c
			}
		}
	}
	// 关键词在正文但码离得远：取关键词后 120 字窗口内的第一个独立短码。
	if hasKeyword(body) {
		lower := strings.ToLower(body)
		for _, k := range codeKeywords {
			if idx := strings.Index(lower, k); idx >= 0 {
				end := idx + len(k) + 120
				if end > len(body) {
					end = len(body)
				}
				if c := firstCode(body[idx+len(k) : end]); c != "" && !looksLikeNoise(c) {
					return c
				}
				break
			}
		}
	}
	return "" // 没有关键词就不猜：纯 6 位数字可能是订单号、金额、时间
}

func firstCode(s string) string {
	// 数字码优先；字母数字混合只在没有纯数字候选时才用。
	for _, m := range numericCodeRE.FindAllStringSubmatch(s, -1) {
		c := m[1]
		if !looksLikeNoise(c) {
			return c
		}
	}
	for _, m := range alnumRE.FindAllStringSubmatch(s, -1) {
		c := m[1]
		hasDigit, hasLetter := false, false
		for _, r := range c {
			switch {
			case r >= '0' && r <= '9':
				hasDigit = true
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
				hasLetter = true
			}
		}
		if hasDigit && hasLetter && !looksLikeNoise(c) {
			return c
		}
	}
	return ""
}

// looksLikeNoise 排除一看就不是验证码的数字：年份、日期、手机号片段、纯 0。
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
