package services

import (
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// PushNotification 是应用内通知的唯一入口。
//
// 为什么必须收口：通知有三个消费面——系统托盘气泡（notifications.SendNotification）、
// 已配置机器人（sendWebhookNotify）、应用内通知中心（这张表 + notify:new 事件）。
// 如果每个业务点各自拼 title、各自写库、各自发事件，新增一个消费面就要改 N 处，
// 且总会漏掉一处（"手动跑有通知、到点跑没通知"就是这么来的，schedule_runner 里
// 已经踩过一次，见 notifyScheduleRun 的注释）。
//
// 现在各业务点只描述「发生了什么」（kind/level/title/body/ref），
// 扇出到哪几个面由本方法决定；要加面只改这里。
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
