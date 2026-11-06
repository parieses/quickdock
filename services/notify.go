package services

import (
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// PushNotification 是应用内通知中心（左下角铃铛）的唯一写入入口。
//
// 为什么必须收口：一条通知有几个互相独立的消费面——应用内通知中心（这张表 +
// notify:new 事件）、系统托盘气泡（notifications.SendNotification，见 NotifyDesktop）、
// 已配置的机器人（sendWebhookNotify）。写库这一面收在这里，因为它的字段与来源
// 分类必须全局一致；另外两面是「打扰」，要不要发取决于场景，由业务点自己显式调用。
//
// 曾有一版注释说这里会扇出到全部三个面，实际并没有（也不该）：待办提醒、监控翻转、
// 定时任务都已经各自发过气泡，收进来只会每条弹两次。
//
// 失败不向上抛：通知是锦上添花，它的失败不该让监控轮询或任务执行变成失败。
func (a *AppService) PushNotification(kind, level, title, body, refType, refID string) *ApiResult {
	if a.DB == nil {
		return FailMsg("数据库未就绪")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		// 没有标题的通知在列表里就是一坨看不出所以然的内容，直接拒收比留个空行好
		return FailMsg("通知标题不能为空")
	}
	rec, err := a.DB.InsertNotification(&db.NotifyItem{
		Kind: kind, Level: level, Title: title, Body: body,
		RefType: refType, RefID: refID,
	})
	if err != nil {
		logger.W("[notify] 写入通知失败: %v", err)
		return Fail(err)
	}
	if app := a.App(); app != nil {
		app.Event.Emit("notify:new", rec)
	}
	return Ok(rec)
}

// NotifyDesktop 叠加一条系统桌面通知（任务栏气泡 / 系统通知中心）。
//
// 与 PushNotification 的分工：那个方法只负责「留一条记录」（写库 + notify:new 事件，
// 落在左下角通知中心），它的价值在于事后回看；而桌面气泡是即时打断，
// 不是每条记录都配得上一次打扰，所以由调用方显式选择，不塞进 PushNotification 里
// 做统一扇出——否则待办、监控、定时任务会各收到两条（它们本来就自己发气泡）。
//
// 单独开一个方法而不是让各域直接拿 App.Notifier 拼：调用方只需给三样东西，
// nil 判空、失败日志、ID 兜底这些容易漏的细节收在这里；services/ai 这类外域包
// 也不用为了一个气泡去 import notifications。
//
// id 用于让系统区分「这是不是同一条通知」：相同 ID 会被就地替换而不是再弹一条，
// 所以调用方要带上能区分的后缀（时间戳 / 记录 ID）。留空则自动生成。
func (a *AppService) NotifyDesktop(id, title, body string) {
	if a.Notifier == nil {
		return
	}
	if strings.TrimSpace(id) == "" {
		id = "qd-" + time.Now().Format("20060102150405.000")
	}
	if err := a.Notifier.SendNotification(notifications.NotificationOptions{
		ID:    id,
		Title: title,
		Body:  body,
	}); err != nil {
		logger.W("[notify] 桌面通知发送失败(%s): %v", title, err)
	}
}
