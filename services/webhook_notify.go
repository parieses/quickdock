package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
	Dingtalk  string `json:"dingtalk"`
	Wecom     string `json:"wecom"`
	Feishu    string `json:"feishu"`
	ServerChan string `json:"serverchan"`
	PushPlus  string `json:"pushplus"`
	Telegram  string `json:"telegram"`
}

const webhookSettingKey = "notify_webhook"

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
		Dingtalk:  strings.TrimSpace(dingtalk),
		Wecom:     strings.TrimSpace(wecom),
		Feishu:    strings.TrimSpace(feishu),
		ServerChan: strings.TrimSpace(serverchan),
		PushPlus:  strings.TrimSpace(pushplus),
		Telegram:  strings.TrimSpace(telegram),
	}
	b, _ := json.Marshal(cfg)
	if err := a.DB.SetSetting(webhookSettingKey, string(b)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// TestWebhook 向指定平台的 Webhook 地址发送一条测试消息（保存前即可验证配置是否正确）。
// kind: dingtalk | wecom | feishu | serverchan | pushplus | telegram
func (a *AppService) TestWebhook(kind, url string) *ApiResult {
	url = strings.TrimSpace(url)
	if url == "" {
		return FailMsg("Webhook 地址/凭据为空")
	}
	title := "🔔 QuickDock 测试通知"
	body := "这是一条来自 QuickDock 的测试消息，收到即表示机器人配置成功。"
	if err := postWebhook(kind, url, title, body); err != nil {
		return Fail(err)
	}
	return OkMsg(nil, "发送成功")
}

// sendWebhookNotify 向所有已配置的机器人异步推送通知（best-effort，失败静默）。
// 用于监控状态翻转等事件通知。
func (a *AppService) sendWebhookNotify(title, body string) {
	cfg := a.loadWebhookConfig()
	targets := []struct{ kind, url string }{
		{"dingtalk", cfg.Dingtalk},
		{"wecom", cfg.Wecom},
		{"feishu", cfg.Feishu},
		{"serverchan", cfg.ServerChan},
		{"pushplus", cfg.PushPlus},
		{"telegram", cfg.Telegram},
	}
	for _, tg := range targets {
		if strings.TrimSpace(tg.url) == "" {
			continue
		}
		go func(kind, url string) {
			defer recoverPanic("webhook:" + kind)
			_ = postWebhook(kind, url, title, body)
		}(tg.kind, tg.url)
	}
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
	var endpoint = url

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
	default:
		return fmt.Errorf("未知的通知平台：%s", kind)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// 各平台都会在 HTTP 200 的响应体里用业务码报错。
	var r struct {
		ErrCode    int    `json:"errcode"`       // 钉钉 / 企业微信
		ErrMsg     string `json:"errmsg"`        //
		Code       int    `json:"code"`          // 飞书（新版）/ ServerChan / PushPlus
		Msg        string `json:"msg"`           //
		StatusCode int    `json:"StatusCode"`    // 飞书（旧版）
		StatusMsg  string `json:"StatusMessage"` //
		OK         bool   `json:"ok"`            // Telegram
		Description string `json:"description"`  // Telegram 错误描述
	}
	_ = json.Unmarshal(respBody, &r)
	// Telegram 成功时 {"ok":true}，失败时 {"ok":false,"description":"..."}
	if kind == "telegram" {
		var tg struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(respBody, &tg)
		if !tg.OK {
			return fmt.Errorf("Telegram 返回错误：%s", tg.Description)
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
