package services

import (
	"fmt"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// StartReminderScheduler 启动待办定时提醒调度器（常驻 goroutine）
// 每 10 秒轮询一次，对「已到提醒时间、未完成、未发送」的待办推送系统通知。
func (a *AppService) StartReminderScheduler() {
	a.schedulerQuit = make(chan struct{})
	a.schedWG.Add(1)
	go func() {
		defer a.schedWG.Done()
		defer logger.RecoverPanic("reminder scheduler")
		// 延迟 3 秒启动，确保通知服务已完成 ServiceStartup 初始化
		time.Sleep(3 * time.Second)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-a.schedulerQuit:
				return
			case <-ticker.C:
				a.checkReminders()
			}
		}
	}()
}

// StopSchedulers 关闭调度器退出通道，令常驻 goroutine 干净退出。
// 必须在 ServiceShutdown 关闭数据库之前调用。
func (a *AppService) StopSchedulers() {
	if a.schedulerQuit == nil {
		return
	}
	a.schedulerQuitOnce.Do(func() { close(a.schedulerQuit) })
}

// schedulerDrainTimeout 退出时等待常驻协程收尾的宽限期。
//
// 不能无限等：慢探针/HTTP 任务最长能跑 30s，点退出后卡半分钟无法接受。
// 给一个短宽限期让「正好在飞」的任务把结果落库，超时就放弃剩余结果——
// 宁可丢最后几条执行记录，也不能让应用退不出去。
const schedulerDrainTimeout = 3 * time.Second

// waitForSchedulers 等到三个常驻循环及其派生的执行协程全部返回。
//
// 为什么必须在 DB.Close() 之前做：循环本体收到退出信号立即返回，但此前派生出的
// 探测/执行协程仍在跑并持有 DB 引用。DB 一旦先关，写回执行结果
// （SetTaskRunResult / AddMonitorLog）会静默失败，任务执行结果永久丢失。
func (a *AppService) waitForSchedulers() {
	done := make(chan struct{})
	go func() {
		a.schedWG.Wait()
		a.taskWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.I("[退出] 常驻循环与执行协程已全部收尾")
	case <-time.After(schedulerDrainTimeout):
		logger.W("[退出] 等待常驻协程收尾超时（%v），放弃剩余执行结果", schedulerDrainTimeout)
	}
}

// checkReminders 检查并发送到期的待办提醒
func (a *AppService) checkReminders() {
	if a.DB == nil || a.Notifier == nil {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	todos, err := a.DB.ListDueReminders(now)
	if err != nil {
		logger.W("QuickDock: 提醒查询失败: %v", err)
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
			logger.W("QuickDock: 提醒发送失败: %v", err)
			continue
		}
		_ = a.DB.MarkReminderSent(t.ID)
		// 进通知中心：桌面通知是一闪而过的，人不在电脑前就彻底丢了。
		// 放在 MarkReminderSent 之后，与「系统通知发失败就重试」的既有语义一致
		// （失败时 continue 不会走到这里，所以通知中心也不会被同一提醒刷屏）。
		a.PushNotification("todo", "warn", "⏰ "+t.Title, body, "todo", t.ID)
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
