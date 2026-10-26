// Package notify 应用内通知中心的门面服务。
//
// 写入走宿主的 services.AppService.PushNotification（唯一入口，同时负责扇出到
// 系统气泡/机器人/事件），本包只提供「读、标记已读、删除」这些前端要用的操作。
package notify

import (
	"quickdock/internal/db"
	"quickdock/services"
)

// NotificationList 列表返回体的显式形状。
//
// 为什么包一层而不是直接返回 []db.NotifyItem：Wails 的绑定生成器只对
// 出现在方法签名里的具名类型生成 TS 接口。直接返回切片会退化成 any，
// 前端就得手写一份与后端同步维护的镜像类型 —— 那正是最容易漂移的地方。
type NotificationList struct {
	Items []db.NotifyItem `json:"items"`
}

// NotifyService 通知中心门面。
type NotifyService struct {
	App *services.AppService
}

// NewNotifyService 创建通知中心门面。
func NewNotifyService(app *services.AppService) *NotifyService {
	return &NotifyService{App: app}
}

// NotifyList 列出通知（时间倒序）。onlyUnread 为 true 时只列未读。
// limit<=0 时取 200 条——列表本身是「最近发生了什么」，不需要全量。
func (s *NotifyService) NotifyList(limit int, onlyUnread bool) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	list, err := s.App.DB.ListNotifications(limit, onlyUnread)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(&NotificationList{Items: list})
}

// NotifyUnread 返回未读数，供徽标轮询/事件刷新。
func (s *NotifyService) NotifyUnread() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	n, err := s.App.DB.UnreadNotificationCount()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

// NotifyMarkRead 标记单条已读。
func (s *NotifyService) NotifyMarkRead(id string) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	if err := s.App.DB.MarkNotificationRead(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(true)
}

// NotifyMarkAllRead 全部标记已读，返回受影响条数。
func (s *NotifyService) NotifyMarkAllRead() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	n, err := s.App.DB.MarkAllNotificationsRead()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

// NotifyDelete 删除单条。
func (s *NotifyService) NotifyDelete(id string) *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	if err := s.App.DB.DeleteNotification(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(true)
}

// NotifyClear 清空通知中心，返回删除条数。
func (s *NotifyService) NotifyClear() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	n, err := s.App.DB.ClearNotifications()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

// NotifyKinds 统计各来源的条数，供面板上的筛选标签显示数量。
func (s *NotifyService) NotifyKinds() *services.ApiResult {
	if s.App.DB == nil {
		return services.FailMsg("数据库未就绪")
	}
	list, err := s.App.DB.ListNotifications(500, false)
	if err != nil {
		return services.Fail(err)
	}
	out := map[string]int{"all": len(list), "unread": 0}
	for _, n := range list {
		if !n.Read {
			out["unread"]++
		}
		out[n.Kind]++
	}
	return services.Ok(out)
}
