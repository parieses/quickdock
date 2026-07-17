package services

import (
	"fmt"
	"time"

	"quickdock/internal/db"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// StartReminderScheduler 启动待办定时提醒调度器（常驻 goroutine）
// 每 10 秒轮询一次，对「已到提醒时间、未完成、未发送」的待办推送系统通知。
func (a *AppService) StartReminderScheduler() {
	go func() {
		// 延迟 3 秒启动，确保通知服务已完成 ServiceStartup 初始化
		time.Sleep(3 * time.Second)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			a.checkReminders()
		}
	}()
}

// checkReminders 检查并发送到期的待办提醒
func (a *AppService) checkReminders() {
	if a.DB == nil || a.Notifier == nil {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	todos, err := a.DB.ListDueReminders(now)
	if err != nil {
		fmt.Println("QuickDock: 提醒查询失败:", err)
		return
	}
	for _, t := range todos {
		body := buildReminderBody(t)
		err := a.Notifier.SendNotification(notifications.NotificationOptions{
			ID:    "reminder-" + t.ID,
			Title: "⏰ " + t.Title,
			Body:  body,
		})
		if err != nil {
			fmt.Println("QuickDock: 提醒发送失败:", err)
			continue
		}
		_ = a.DB.MarkReminderSent(t.ID)
	}
}

// buildReminderBody 组合提醒正文：时间区间 + 备注
func buildReminderBody(t db.Todo) string {
	parts := []string{}
	if t.StartTime != "" {
		if t.EndTime != "" {
			parts = append(parts, fmt.Sprintf("时间：%s ～ %s", t.StartTime, t.EndTime))
		} else {
			parts = append(parts, fmt.Sprintf("开始：%s", t.StartTime))
		}
	} else if t.ReminderTime != "" {
		parts = append(parts, fmt.Sprintf("提醒：%s", t.ReminderTime))
	}
	if t.Note != "" {
		parts = append(parts, t.Note)
	}
	if len(parts) == 0 {
		return "该提醒时间已到"
	}
	return joinLines(parts)
}

func joinLines(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

// SendTestNotification 发送一条测试通知（前端「发送测试提醒」按钮调用）
func (a *AppService) SendTestNotification(title, body string) *ApiResult {
	if a.Notifier == nil {
		return Fail(fmt.Errorf("通知服务不可用"))
	}
	if title == "" {
		title = "快启坞提醒"
	}
	if body == "" {
		body = "这是一条测试提醒，确认系统通知工作正常。"
	}
	err := a.Notifier.SendNotification(notifications.NotificationOptions{
		ID:    "test-" + time.Now().Format("20060102150405"),
		Title: title,
		Body:  body,
	})
	if err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
