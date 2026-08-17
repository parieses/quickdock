package services

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// ---- 番茄钟 / 专注计时 ----
// 前端待办页跑倒计时，到点的"专注完成"通过本方法发系统通知 + 可选 webhook。

// NotifyFocusComplete 专注计时结束时调用：发系统通知 + 机器人/webhook 推送。
// title 为待办标题；minutes 为本次专注时长（分钟）。
func (a *AppService) NotifyFocusComplete(title string, minutes int) *ApiResult {
	title = trimTitle(title)
	if title == "" {
		title = "专注计时"
	}
	if minutes <= 0 {
		minutes = 25
	}
	msg := fmt.Sprintf("「%s」专注 %d 分钟已到，休息一下吧 🍅", title, minutes)

	// 系统通知
	if a.Notifier != nil {
		_ = a.Notifier.SendNotification(notifications.NotificationOptions{
			ID:    "focus-" + fmt.Sprintf("%d", time.Now().UnixNano()),
			Title: "🍅 专注完成：" + title,
			Body:  fmt.Sprintf("已专注 %d 分钟，该休息了", minutes),
		})
	}
	// 可选 webhook（复用监控通知的 webhook 通道）
	a.sendWebhookNotify("🍅 专注完成", msg)
	return Ok(nil)
}

// NotifyFocusStart 专注开始时可选的提示（可选调用，不发 webhook，避免刷屏）。
func (a *AppService) NotifyFocusStart(title string, minutes int) *ApiResult {
	if a.Notifier == nil {
		return Ok(nil)
	}
	title = trimTitle(title)
	if title == "" {
		title = "专注计时"
	}
	if minutes <= 0 {
		minutes = 25
	}
	_ = a.Notifier.SendNotification(notifications.NotificationOptions{
		ID:    "focus-start-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Title: "🍅 开始专注",
		Body:  fmt.Sprintf("「%s」开始 %d 分钟计时", title, minutes),
	})
	return Ok(nil)
}

// trimTitle 清理标题（限长，避免通知内容过长）
func trimTitle(s string) string {
	if len(s) == 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
