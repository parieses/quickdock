package services

import (
	"regexp"

	"quickdock/internal/db"
)

// validateMonitorContentMatch 在保存前预编译校验正则，尽早暴露非法模式
func validateMonitorContentMatch(m db.Monitor) *ApiResult {
	if m.ContentMatchType == "regex" && m.ContentMatchPattern != "" {
		if _, err := regexp.Compile(m.ContentMatchPattern); err != nil {
			return FailMsg("正则语法错误：" + err.Error())
		}
	}
	return nil
}

// CreateMonitor 新建网站监控
func (a *AppService) CreateMonitor(m db.Monitor) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if v := validateMonitorContentMatch(m); v != nil {
		return v
	}
	created, err := a.DB.CreateMonitor(&m)
	if err != nil {
		return Fail(err)
	}
	a.wakeMonitorChecker()
	return Ok(created)
}

// ListMonitors 列出全部监控
func (a *AppService) ListMonitors() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	ms, err := a.DB.ListMonitors()
	return wrap(ms, err)
}

// UpdateMonitor 更新监控配置
func (a *AppService) UpdateMonitor(m db.Monitor) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if m.ID == "" {
		return FailMsg("监控 ID 不能为空")
	}
	if v := validateMonitorContentMatch(m); v != nil {
		return v
	}
	if err := a.DB.UpdateMonitor(&m); err != nil {
		return Fail(err)
	}
	a.wakeMonitorChecker()
	return Ok(nil)
}

// DeleteMonitor 删除监控及其日志
func (a *AppService) DeleteMonitor(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteMonitor(id); err != nil {
		return Fail(err)
	}
	a.wakeMonitorChecker()
	return Ok(nil)
}

// SetMonitorEnabled 启用/停用监控
func (a *AppService) SetMonitorEnabled(id string, enabled bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetMonitorEnabled(id, enabled); err != nil {
		return Fail(err)
	}
	a.wakeMonitorChecker()
	return Ok(nil)
}

// CheckMonitorNow 立即手动检测一次（不影响下次调度）
func (a *AppService) CheckMonitorNow(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	m, err := a.DB.GetMonitor(id)
	if err != nil {
		return Fail(err)
	}
	status, summary := a.checkOneMonitor(m)
	if status != "up" {
		return FailMsg(summary)
	}
	return OkMsg(summary, summary)
}

// GetMonitorLogs 返回某监控最近 limit 条检测日志
func (a *AppService) GetMonitorLogs(id string, limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	logs, err := a.DB.GetMonitorLogs(id, limit)
	return wrap(logs, err)
}

// GetMonitorLogsSince 返回某监控 checked_ts >= sinceTs 的检测日志（时间范围切换用）
func (a *AppService) GetMonitorLogsSince(id string, sinceTs, limit int64) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	logs, err := a.DB.GetMonitorLogsSince(id, sinceTs, limit)
	return wrap(logs, err)
}

// ClearMonitorLogs 清空某监控的检测日志
func (a *AppService) ClearMonitorLogs(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ClearMonitorLogs(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ListMonitorStats 汇总所有监控近 24h 在线率等指标
func (a *AppService) ListMonitorStats() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	stats, err := a.DB.ListMonitorStats()
	return wrap(stats, err)
}
