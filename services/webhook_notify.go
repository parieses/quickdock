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

// WebhookConfig 机器人 Webhook 通知配置（钉钉 / 企业微信 / 飞书）。
// 三个字段各存对应平台自定义机器人的 Webhook 地址，留空即为未启用。
type WebhookConfig struct {
	Dingtalk string `json:"dingtalk"`
	Wecom    string `json:"wecom"`
	Feishu   string `json:"feishu"`
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

// SetWebhookConfig 保存机器人通知配置（钉钉 / 企业微信 / 飞书 的 Webhook 地址）
func (a *AppService) SetWebhookConfig(dingtalk, wecom, feishu string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg := WebhookConfig{
		Dingtalk: strings.TrimSpace(dingtalk),
		Wecom:    strings.TrimSpace(wecom),
		Feishu:   strings.TrimSpace(feishu),
	}
	b, _ := json.Marshal(cfg)
	if err := a.DB.SetSetting(webhookSettingKey, string(b)); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// TestWebhook 向指定平台的 Webhook 地址发送一条测试消息（保存前即可验证配置是否正确）。
// kind: dingtalk | wecom | feishu
func (a *AppService) TestWebhook(kind, url string) *ApiResult {
	url = strings.TrimSpace(url)
	if url == "" {
		return FailMsg("Webhook 地址为空")
	}
	title := "🔔 QuickDock 测试通知"
	body := "这是一条来自 QuickDock 网站监控的测试消息，收到即表示机器人配置成功。"
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
	}
	for _, tg := range targets {
		if strings.TrimSpace(tg.url) == "" {
			continue
		}
		go func(kind, url string) {
			_ = postWebhook(kind, url, title, body)
		}(tg.kind, tg.url)
	}
}

// postWebhook 按各平台 text 消息格式构造 payload 并 POST，解析返回判定是否成功。
func postWebhook(kind, url, title, body string) error {
	content := title
	if body != "" {
		content += "\n" + body
	}

	var payload []byte
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
	default:
		return fmt.Errorf("未知的通知平台：%s", kind)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// 三家都会在 HTTP 200 的响应体里用业务码报错（如钉钉关键词不匹配 errcode=310000）。
	var r struct {
		ErrCode    int    `json:"errcode"`       // 钉钉 / 企业微信
		ErrMsg     string `json:"errmsg"`        //
		Code       int    `json:"code"`          // 飞书（新版，0=成功）
		Msg        string `json:"msg"`           //
		StatusCode int    `json:"StatusCode"`    // 飞书（旧版，0=成功）
		StatusMsg  string `json:"StatusMessage"` //
	}
	_ = json.Unmarshal(respBody, &r)
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
