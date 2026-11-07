package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// webhookClient 共享 HTTP 客户端（通知投递，连接复用）
var webhookClient = &http.Client{Timeout: 10 * time.Second}

// WebhookConfig 浏览器/机器人 Webhook 通知配置。
// 各字段对应一个渠道的"地址或凭据"，留空即未启用：
//   - dingtalk / wecom / feishu: 自定义机器人 webhook 地址
//   - serverchan: https://sctapi.ftqq.com/<SENDKEY>.send
//   - pushplus:   PushPlus 的 token
//   - telegram:   "botToken|chatId" 或完整 sendMessage 地址
type WebhookConfig struct {
	Dingtalk   string `json:"dingtalk"`
	Wecom      string `json:"wecom"`
	Feishu     string `json:"feishu"`
	ServerChan string `json:"serverchan"`
	PushPlus   string `json:"pushplus"`
	Telegram   string `json:"telegram"`
}

const webhookSettingKey = "notify_webhook"

// ExtraNotifyChannel 额外的通知渠道（动态多渠道，未来加平台零签名变化）。
// 内置 6 家仍存 notify_webhook；这里放在独立键里，避免改动 SetWebhookConfig 的
// 6 参数签名破坏存量调用与 MCP/插件。kind 取值见 supportedWebhookKinds。
type ExtraNotifyChannel struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // bark | discord | slack | ntfy | gotify | custom（见 supportedWebhookKinds）
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

const webhookExtraSettingKey = "notify_extra_channels"

// supportedWebhookKinds 是 postWebhook 认识的全部平台类型（内置 6 + 扩展 6，有序）。
// 扩展渠道的 kind 白名单、TestWebhook 的合法 kind、SendWebhookMessage 的可选渠道提示
// 都以它为唯一来源：以后接第 13 个平台，只在这里加一项 + postWebhook 加一个 case，
// 存储结构、SetWebhookConfig 签名、前端都不用动。
var supportedWebhookKinds = []string{
	"dingtalk", "wecom", "feishu", "serverchan", "pushplus", "telegram",
	"bark", "discord", "slack", "ntfy", "gotify", "custom",
}

func isWebhookKind(k string) bool {
	for _, x := range supportedWebhookKinds {
		if x == k {
			return true
		}
	}
	return false
}

// loadWebhookConfig 从 settings 读取机器人通知配置（内部用，出错返回空配置）
func (a *AppService) loadWebhookConfig() WebhookConfig {
	var cfg WebhookConfig
	if a.DB == nil {
		return cfg
	}
	raw, err := a.DB.GetSetting(webhookSettingKey)
	if err != nil || raw == "" {
		return cfg
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg
}

// GetWebhookConfig 读取机器人通知配置（供前端设置页回填）
func (a *AppService) GetWebhookConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	return Ok(a.loadWebhookConfig())
}

// SetWebhookConfig 保存机器人通知配置。
func (a *AppService) SetWebhookConfig(dingtalk, wecom, feishu, serverchan, pushplus, telegram string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg := WebhookConfig{
		Dingtalk:   strings.TrimSpace(dingtalk),
		Wecom:      strings.TrimSpace(wecom),
		Feishu:     strings.TrimSpace(feishu),
		ServerChan: strings.TrimSpace(serverchan),
		PushPlus:   strings.TrimSpace(pushplus),
		Telegram:   strings.TrimSpace(telegram),
	}
	b, _ := json.Marshal(cfg)
	if err := a.DB.SetSetting(webhookSettingKey, string(b)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// loadExtraNotifyChannels 读取扩展通知渠道；坏数据（键不存在、JSON 损坏、类型不对）
// 一律返回空切片而不是 panic——设置项可能被手改或旧版本写入，通知链路不该因此崩掉。
func (a *AppService) loadExtraNotifyChannels() []ExtraNotifyChannel {
	if a.DB == nil {
		return []ExtraNotifyChannel{}
	}
	raw, err := a.DB.GetSetting(webhookExtraSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return []ExtraNotifyChannel{}
	}
	var list []ExtraNotifyChannel
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []ExtraNotifyChannel{}
	}
	if list == nil {
		return []ExtraNotifyChannel{}
	}
	// 手改设置可能带大写 kind，统一收敛，保证 postWebhook 的 switch 能命中。
	for i := range list {
		list[i].Kind = strings.ToLower(strings.TrimSpace(list[i].Kind))
	}
	return list
}

// GetExtraNotifyChannels 读取扩展通知渠道列表（供前端设置页回填）。
func (a *AppService) GetExtraNotifyChannels() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	return Ok(a.loadExtraNotifyChannels())
}

// hasControlChars 拒绝 ASCII 控制字符与 DEL；尤其 ntfy 的 Title 头遇到 CR/LF 会被
// http.Client 直接判为非法，且这些字符放进 URL/名称也没有任何正当用途。
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// SetExtraNotifyChannels 保存扩展通知渠道。raw 为 JSON.stringify(channels)。
func (a *AppService) SetExtraNotifyChannels(raw string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	var list []ExtraNotifyChannel
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return FailMsg("扩展通知渠道 JSON 格式错误：" + err.Error())
	}
	if len(list) > 20 {
		return FailMsg("扩展通知渠道最多 20 条")
	}
	for i := range list {
		c := &list[i]
		c.ID = strings.TrimSpace(c.ID)
		c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
		c.Name = strings.TrimSpace(c.Name)
		c.URL = strings.TrimSpace(c.URL)
		if len(c.ID) > 80 || len(c.Name) > 80 || len(c.URL) > 2000 {
			return FailMsg("扩展渠道 id/name/url 超出长度上限（80 / 80 / 2000）")
		}
		if !isWebhookKind(c.Kind) {
			return FailMsg("扩展渠道 kind 不支持：" + c.Kind + "（可选：" + strings.Join(supportedWebhookKinds, " / ") + "）")
		}
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			return FailMsg("扩展渠道 URL 必须以 http:// 或 https:// 开头：" + c.URL)
		}
		if hasControlChars(c.ID) || hasControlChars(c.Name) || hasControlChars(c.URL) || hasControlChars(c.Kind) {
			return FailMsg("扩展渠道字段含控制字符")
		}
	}
	// null 归一成 []，避免前端拿到 null 当成「没配置」却又不是数组。
	if list == nil {
		list = []ExtraNotifyChannel{}
	}
	b, _ := json.Marshal(list)
	if err := a.DB.SetSetting(webhookExtraSettingKey, string(b)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// notifyTarget 统一描述一个可投递的目标：selector 用于 channel 定向与结果展示，
// kind 用于 postWebhook 构造 payload，url 为投递地址（内置 6 家里也可能是凭据）。
type notifyTarget struct {
	selector string
	kind     string
	url      string
}

// collectNotifyTargets 汇总「已配置且启用」的投递目标：内置 6 渠道 + extras 里启用的扩展渠道。
// best-effort 扇出与 MCP 显式发送共用，避免渠道名单在两处漂移。
func (a *AppService) collectNotifyTargets(extras []ExtraNotifyChannel) []notifyTarget {
	cfg := a.loadWebhookConfig()
	base := []struct{ kind, url string }{
		{"dingtalk", cfg.Dingtalk},
		{"wecom", cfg.Wecom},
		{"feishu", cfg.Feishu},
		{"serverchan", cfg.ServerChan},
		{"pushplus", cfg.PushPlus},
		{"telegram", cfg.Telegram},
	}
	out := make([]notifyTarget, 0, len(base)+len(extras))
	for _, b := range base {
		if u := strings.TrimSpace(b.url); u != "" {
			out = append(out, notifyTarget{selector: b.kind, kind: b.kind, url: u})
		}
	}
	for _, c := range extras {
		if !c.Enabled || strings.TrimSpace(c.URL) == "" {
			continue
		}
		sel := strings.TrimSpace(c.ID)
		if sel == "" {
			sel = c.Kind
		}
		out = append(out, notifyTarget{selector: sel, kind: c.Kind, url: strings.TrimSpace(c.URL)})
	}
	return out
}

// channelSelectors 返回当前所有合法的 channel 定向值：全部内置 kind + 全部扩展渠道的
// id/kind（不只启用的，方便用户看到自己配过什么）。动态生成，不把平台名单写死在字符串里。
func channelSelectors(extras []ExtraNotifyChannel) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, k := range supportedWebhookKinds {
		add(k)
	}
	for _, c := range extras {
		add(c.ID)
		add(c.Kind)
	}
	return out
}

// TestWebhook 向指定平台发送一条测试消息（保存前即可验证配置是否正确）。
// kind 见 supportedWebhookKinds（内置 6 家 + bark/discord/slack/ntfy/gotify/custom）。
// 前端所有渠道的「测试」按钮统一走这里，不必为每个平台新增导出方法。
func (a *AppService) TestWebhook(kind, url string) *ApiResult {
	kind = strings.ToLower(strings.TrimSpace(kind))
	url = strings.TrimSpace(url)
	if url == "" {
		return FailMsg("Webhook 地址/凭据为空")
	}
	if !isWebhookKind(kind) {
		return FailMsg("不支持的通知平台：" + kind + "（可选：" + strings.Join(supportedWebhookKinds, " / ") + "）")
	}
	title := "🔔 QuickDock 测试通知"
	body := "这是一条来自 QuickDock 的测试消息，收到即表示机器人配置成功。"
	if err := postWebhook(kind, url, title, body); err != nil {
		return Fail(err)
	}
	return OkMsg(nil, "发送成功")
}

// sendWebhookNotify 向所有已启用渠道异步推送通知（best-effort，单渠道失败不影响其它渠道）。
// 用于监控状态翻转等事件通知；内置 6 家与扩展渠道一起扇出。
func (a *AppService) sendWebhookNotify(title, body string) {
	targets := a.collectNotifyTargets(a.loadExtraNotifyChannels())
	for _, tg := range targets {
		go func(t notifyTarget) {
			defer logger.RecoverPanic("webhook:" + t.kind)
			// best-effort 仍要留一条 W：配置写错 / 被限流 / 机器人被移出群时，
			// 用户只看到「没收到通知」，日志里不该毫无线索。
			if err := postWebhook(t.kind, t.url, title, body); err != nil {
				logger.W("[webhook] %s 通知发送失败: %v", t.selector, err)
			}
		}(tg)
	}
	if len(targets) == 0 {
		logger.W("[webhook] 通知未发送：未配置任何通知渠道（标题=%s）", title)
	}
}

// SendWebhookMessage 通过 MCP / 外部调用向已启用的通知渠道发送消息（「机器人通知」的显式入口）。
//
// 与定时任务/监控自动触发走的 sendWebhookNotify 不同：那个是 best-effort 异步、不回收结果；
// 这里同步返回每个渠道的成败，便于调用方判断是否真的发出去了。
//
// 前提：必须至少配置过一个启用渠道，否则直接返回可读错误——而不是静默「发不出去」。
//
// channel 留空 → 发给所有已启用渠道；指定 → 只发匹配的渠道，可用内置 kind、
// 扩展渠道的 id，或扩展渠道的 kind（命中该 kind 的全部渠道）。
func (a *AppService) SendWebhookMessage(title, body, channel string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return FailMsg("消息标题 title 不能为空")
	}

	extras := a.loadExtraNotifyChannels()
	active := a.collectNotifyTargets(extras)
	selectors := channelSelectors(extras)
	selectorsText := strings.Join(selectors, " / ")

	channel = strings.ToLower(strings.TrimSpace(channel))
	targets := active
	if channel != "" {
		// 「可选渠道」文案由 channelSelectors 动态生成，不再硬列 6 家。
		known := false
		for _, s := range selectors {
			if strings.EqualFold(s, channel) {
				known = true
				break
			}
		}
		if !known {
			return FailMsg("未知的通知渠道: " + channel + "（可选：" + selectorsText + "）")
		}
		filtered := make([]notifyTarget, 0, len(active))
		for _, tg := range active {
			if strings.EqualFold(tg.selector, channel) || strings.EqualFold(tg.kind, channel) {
				filtered = append(filtered, tg)
			}
		}
		if len(filtered) == 0 {
			return FailMsg("通知渠道 " + channel + " 未配置或未启用（可选：" + selectorsText + "）")
		}
		targets = filtered
	}

	if len(targets) == 0 {
		return FailMsg("未配置任何启用通知渠道，请先在「设置 → 通知」中配置后再发送（可选：" + selectorsText + "）")
	}

	results := make([]map[string]any, 0, len(targets))
	for _, tg := range targets {
		if err := postWebhook(tg.kind, tg.url, title, body); err != nil {
			logger.W("[webhook] MCP 调用发送 %s(%s) 失败: %v", tg.selector, tg.kind, err)
			results = append(results, map[string]any{"channel": tg.selector, "ok": false, "error": err.Error()})
		} else {
			results = append(results, map[string]any{"channel": tg.selector, "ok": true})
		}
	}
	okCount := 0
	for _, r := range results {
		if b, _ := r["ok"].(bool); b {
			okCount++
		}
	}
	return Ok(map[string]any{
		"title":   title,
		"channel": channel,
		"sent":    len(targets),
		"ok":      okCount,
		"results": results,
	})
}

// isSimpleOKWebhook 表示「2xx 即成功、不解析响应体业务码」的渠道。
// 新增 6 家全都如此：Discord 返回 204 空 body、Slack 返回纯文本 "ok"、
// Bark 的 {"code":200} 恰恰是成功码（套内置渠道的 code!=0 判定会误报）、
// ntfy/gotify/custom 的响应体也不是 errcode/code 语义。
func isSimpleOKWebhook(kind string) bool {
	switch kind {
	case "bark", "discord", "slack", "ntfy", "gotify", "custom":
		return true
	}
	return false
}

// headerValueSafe 压掉请求头值里的控制字符（尤其 CR/LF）：
// http.Client 会在发送时拒绝含 CR/LF 的头值，而 ntfy 的 Title 头来自用户输入的标题。
func headerValueSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// postWebhook 按各平台消息格式构造 payload 并 POST，解析返回判定是否成功。
func postWebhook(kind, url, title, body string) error {
	ts := time.Now().Format("2006-01-02 15:04:05")
	content := title
	if body != "" {
		content += "\n" + body
	}
	content += "\n—— " + ts

	var payload []byte
	endpoint := url
	contentType := "application/json"
	headers := map[string]string{}

	switch kind {
	case "feishu":
		payload, _ = json.Marshal(map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": content},
		})
	case "dingtalk", "wecom":
		// 钉钉与企业微信自定义机器人 text 消息格式一致
		payload, _ = json.Marshal(map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]string{"content": content},
		})
	case "serverchan":
		// ServerChan 第三版：POST https://sctapi.ftqq.com/<SENDKEY>.send
		// payload 为 {title, desp} JSON。
		payload, _ = json.Marshal(map[string]string{"title": title, "desp": content})
	case "pushplus":
		// PushPlus：POST https://www.pushplus.plus/send，body {token,title,content,template}
		endpoint = "https://www.pushplus.plus/send"
		payload, _ = json.Marshal(map[string]string{
			"token":    strings.TrimSpace(url),
			"title":    title,
			"content":  content,
			"template": "txt",
		})
	case "telegram":
		// Telegram bot sendMessage。支持两种配置：
		//   a) url = "botToken|chatId"
		//   b) url = 完整 sendMessage 地址
		chatID := ""
		if strings.Contains(url, "|") {
			parts := strings.SplitN(url, "|", 2)
			token := strings.TrimSpace(parts[0])
			chatID = strings.TrimSpace(parts[1])
			endpoint = "https://api.telegram.org/bot" + token + "/sendMessage"
		} else {
			endpoint = strings.TrimSpace(url)
		}
		text := content
		m := map[string]string{"text": text}
		if chatID != "" {
			m["chat_id"] = chatID
		}
		payload, _ = json.Marshal(m)
	case "bark":
		payload, _ = json.Marshal(map[string]string{
			"title": title, "body": body, "group": "QuickDock",
		})
	case "discord":
		payload, _ = json.Marshal(map[string]string{
			"content": "**" + title + "**\n" + body,
		})
	case "slack":
		payload, _ = json.Marshal(map[string]string{
			"text": "*" + title + "*\n" + body,
		})
	case "ntfy":
		// ntfy 用纯文本请求体 + Title/Tags 头：标题放头里，正文只放消息体。
		contentType = "text/plain; charset=utf-8"
		plain := body
		if strings.TrimSpace(plain) == "" {
			plain = title
		}
		payload = []byte(plain)
		headers["Title"] = headerValueSafe(title)
		headers["Tags"] = "bell"
	case "gotify":
		// url 视为 ${base}/message?token=xxx，原样 POST 即可。
		payload, _ = json.Marshal(map[string]any{
			"title": title, "message": body, "priority": 5,
		})
	case "custom":
		payload, _ = json.Marshal(map[string]any{
			"title": title, "body": body, "source": "QuickDock", "timestamp": time.Now().Unix(),
		})
	default:
		return fmt.Errorf("未知的通知平台：%s", kind)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	// 接受任意 2xx：Discord 返回 204 无 body，其余平台偶尔也用 201/202。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if isSimpleOKWebhook(kind) {
		return nil // 2xx（含空 body）即成功，不套内置渠道的业务码解析
	}

	// 内置 6 家：响应体里用业务码报错，HTTP 200 也可能带着 errcode/code。
	var r struct {
		ErrCode     int    `json:"errcode"`       // 钉钉 / 企业微信
		ErrMsg      string `json:"errmsg"`        //
		Code        int    `json:"code"`          // 飞书（新版）/ ServerChan / PushPlus
		Msg         string `json:"msg"`           //
		StatusCode  int    `json:"StatusCode"`    // 飞书（旧版）
		StatusMsg   string `json:"StatusMessage"` //
		OK          bool   `json:"ok"`            // Telegram
		Description string `json:"description"`   // Telegram 错误描述
	}
	_ = json.Unmarshal(respBody, &r)
	// Telegram 成功时 {"ok":true}，失败时 {"ok":false,"description":"..."}
	if kind == "telegram" {
		if !r.OK {
			return fmt.Errorf("Telegram 返回错误：%s", r.Description)
		}
		return nil
	}
	if r.ErrCode != 0 {
		return fmt.Errorf("平台返回错误 %d：%s", r.ErrCode, r.ErrMsg)
	}
	if r.Code != 0 {
		return fmt.Errorf("平台返回错误 %d：%s", r.Code, r.Msg)
	}
	if r.StatusCode != 0 {
		return fmt.Errorf("平台返回错误 %d：%s", r.StatusCode, r.StatusMsg)
	}
	return nil
}
