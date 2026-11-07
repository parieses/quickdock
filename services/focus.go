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

// ---- 专注会话落库 / 统计 ----
// 前端番茄钟到点完成时先写 localStorage 保底，再异步调下面的接口把会话落进
// focus_sessions；统计面板再用 ListFocusSessions / FocusStats 读出来。
// 门面只做参数校验 + 委托 db 层，风格照 services/link.go。

// 入参上限：id / 标题 / 时间串都远小于 500 字节，挡住异常长串避免拖慢查询。
const maxFocusParamLen = 500

// SaveFocusSession 落一条专注会话。data: db.FocusSession（落库行）
func (a *AppService) SaveFocusSession(todoID, title, startedAt, endedAt string, minutes int, done bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if len(todoID) > maxFocusParamLen || len(title) > maxFocusParamLen ||
		len(startedAt) > maxFocusParamLen || len(endedAt) > maxFocusParamLen {
		return FailMsg("参数过长")
	}
	if startedAt == "" || endedAt == "" {
		return FailMsg("时间不能为空")
	}
	if minutes < 1 || minutes > 1440 {
		return FailMsg("专注时长非法")
	}
	return wrap(a.DB.SaveFocusSession(todoID, title, startedAt, endedAt, minutes, done))
}

// ListFocusSessions 列出 [from, to] 内开始的会话。data: []db.FocusSession
func (a *AppService) ListFocusSessions(from, to string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if len(from) > maxFocusParamLen || len(to) > maxFocusParamLen {
		return FailMsg("参数过长")
	}
	return wrap(a.DB.ListFocusSessions(from, to))
}

// FocusStats 汇总最近 days 天的完成会话。data: map，键 sessions/minutes/byTodo/daily
func (a *AppService) FocusStats(days int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if days < 1 || days > 3650 {
		return FailMsg("天数非法")
	}
	s, err := a.DB.FocusStats(days)
	if err != nil {
		return Fail(err)
	}
	return Ok(map[string]interface{}{
		"sessions": s.Sessions,
		"minutes":  s.Minutes,
		"byTodo":   s.ByTodo,
		"daily":    s.Daily,
	})
}
