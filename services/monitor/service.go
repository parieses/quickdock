// Package monitor 网站监控服务
// 承载原 AppService 的监控 CRUD/统计领域方法（创建/启停/日志/统计）。
// 常驻检测循环（monitor_checker.go）保留在宿主，本门面仅做配置与手动触发。
package monitor

import (
	"fmt"
	"regexp"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/validators"
	"quickdock/services"
)

// MonitorService 承载监控领域方法。
// App 回指宿主 AppService：DB 为宿主导出字段；检测循环唤醒/单次检测经宿主转发。
type MonitorService struct {
	App *services.AppService
}

// NewMonitorService 创建监控门面服务，App 为宿主服务引用。
func NewMonitorService(app *services.AppService) *MonitorService {
	return &MonitorService{App: app}
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本）
func (s *MonitorService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// validateContentMatch 在保存前预编译校验正则，尽早暴露非法模式
func validateContentMatch(m db.Monitor) *services.ApiResult {
	if m.ContentMatchType == "regex" && m.ContentMatchPattern != "" {
		if _, err := regexp.Compile(m.ContentMatchPattern); err != nil {
			return services.FailMsg("正则语法错误：" + err.Error())
		}
	}
	return nil
}

// validateMonitorURL 保存前校验地址：错填的 URL 不会在保存时报错，而是让这条监控
// 每轮都「请求失败」，用户看到的是"网站挂了"而不是"地址写错了"。
func validateMonitorURL(m db.Monitor) *services.ApiResult {
	if err := validators.ValidateURL(strings.TrimSpace(m.URL)); err != nil {
		return services.Fail(err)
	}
	return nil
}

// CreateMonitor 新建网站监控
func (s *MonitorService) CreateMonitor(m db.Monitor) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if v := validateContentMatch(m); v != nil {
		return v
	}
	if v := validateMonitorURL(m); v != nil {
		return v
	}
	created, err := s.App.DB.CreateMonitor(&m)
	if err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(created)
}

// ListMonitors 列出全部监控
func (s *MonitorService) ListMonitors() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	ms, err := s.App.DB.ListMonitors()
	return services.Wrap(ms, err)
}

// UpdateMonitor 更新监控配置
func (s *MonitorService) UpdateMonitor(m db.Monitor) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if m.ID == "" {
		return services.FailMsg("监控 ID 不能为空")
	}
	if v := validateContentMatch(m); v != nil {
		return v
	}
	if err := s.App.DB.UpdateMonitor(&m); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// DeleteMonitor 删除监控及其日志
func (s *MonitorService) DeleteMonitor(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteMonitor(id); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// SoftDeleteMonitor 软删：进回收站，可恢复。
func (s *MonitorService) SoftDeleteMonitor(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Fail(fmt.Errorf("参数不合法：id 不能为空"))
	}
	if err := s.App.DB.SoftDeleteMonitor(id); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// RestoreMonitor 从回收站恢复。
func (s *MonitorService) RestoreMonitor(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Fail(fmt.Errorf("参数不合法：id 不能为空"))
	}
	if err := s.App.DB.RestoreMonitor(id); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// ListDeletedMonitors 回收站列表。
func (s *MonitorService) ListDeletedMonitors() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListDeletedMonitors()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// SetMonitorSort 拖拽排序。
func (s *MonitorService) SetMonitorSort(id string, sort int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Fail(fmt.Errorf("参数不合法：id 不能为空"))
	}
	if err := s.App.DB.SetMonitorSort(id, sort); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// SetMonitorMaintenance 维护窗口：until=0 取消。
func (s *MonitorService) SetMonitorMaintenance(id string, until int64) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Fail(fmt.Errorf("参数不合法：id 不能为空"))
	}
	if err := s.App.DB.SetMonitorMaintenance(id, until); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// SetMonitorMute 告警静默：until=0 取消。
//
// 与 SetMonitorMaintenance 的区别必须记在名字旁边，否则很容易被当成同一个开关：
// 维护窗口 = 暂停探测（不写日志，图上留缺口）；静默 = 照常探测、照常记日志，
// 只是不弹气泡、不发 Webhook —— 凌晨备份那类「隔几分钟抖一下」的噪音应该用这个。
func (s *MonitorService) SetMonitorMute(id string, until int64) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(id) == "" {
		return services.Fail(fmt.Errorf("参数不合法：id 不能为空"))
	}
	if err := s.App.DB.SetMonitorMute(id, until); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// SetMonitorEnabled 启用/停用监控
func (s *MonitorService) SetMonitorEnabled(id string, enabled bool) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.SetMonitorEnabled(id, enabled); err != nil {
		return services.Fail(err)
	}
	s.App.WakeMonitorChecker()
	return services.Ok(nil)
}

// CheckMonitorNow 立即手动检测一次（不影响下次调度）
func (s *MonitorService) CheckMonitorNow(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	m, err := s.App.DB.GetMonitor(id)
	if err != nil {
		return services.Fail(err)
	}
	// 停用的监控不该"点了立即检测、结果什么都没说"：探针内部会直接返回空状态，
	// 前端把空串当错误显示，用户只看到一条空白错误条。
	if !m.Enabled {
		return services.FailMsg("该监控已停用，未执行检测")
	}
	status, summary := s.App.CheckOneMonitor(m)
	if status == "" {
		return services.FailMsg("检测未执行（监控已停用或数据不可用）")
	}
	if status != "up" {
		return services.FailMsg(summary)
	}
	return services.OkMsg(summary, summary)
}

// GetMonitorLogs 返回某监控最近 limit 条检测日志
func (s *MonitorService) GetMonitorLogs(id string, limit int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	logs, err := s.App.DB.GetMonitorLogs(id, limit)
	return services.Wrap(logs, err)
}

// GetMonitorLogsSince 返回某监控 checked_ts >= sinceTs 的检测日志（时间范围切换用）
func (s *MonitorService) GetMonitorLogsSince(id string, sinceTs, limit int64) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	logs, err := s.App.DB.GetMonitorLogsSince(id, sinceTs, limit)
	return services.Wrap(logs, err)
}

// ClearMonitorLogs 清空某监控的检测日志
func (s *MonitorService) ClearMonitorLogs(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.ClearMonitorLogs(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ListMonitorStats 汇总所有监控近 24h 在线率等指标
func (s *MonitorService) ListMonitorStats() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	stats, err := s.App.DB.ListMonitorStats()
	return services.Wrap(stats, err)
}
