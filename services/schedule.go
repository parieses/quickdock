package services

import (
	"fmt"

	"quickdock/internal/db"
)

// CreateScheduledTask 新建定时任务，next_run 由后端根据调度规则计算
func (a *AppService) CreateScheduledTask(t db.ScheduledTask) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := validateScheduledTask(&t); err != nil {
		return Fail(err)
	}
	t.NextRun = computeNextRun(&t, nowStr())
	// 一次性任务若计算不出有效时间（如时间已过），仍允许保存但保持禁用/无 next_run
	created, err := a.DB.CreateScheduledTask(&t)
	if err != nil {
		return Fail(err)
	}
	a.wakeScheduler()
	return Ok(created)
}

// ListScheduledTasks 列出全部定时任务
func (a *AppService) ListScheduledTasks() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	tasks, err := a.DB.ListScheduledTasks()
	return wrap(tasks, err)
}

// UpdateScheduledTask 更新定时任务并重算 next_run
func (a *AppService) UpdateScheduledTask(t db.ScheduledTask) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if t.ID == "" {
		return FailMsg("任务 ID 不能为空")
	}
	if err := validateScheduledTask(&t); err != nil {
		return Fail(err)
	}
	if t.Enabled {
		t.NextRun = computeNextRun(&t, nowStr())
	} else {
		t.NextRun = ""
	}
	if err := a.DB.UpdateScheduledTask(&t); err != nil {
		return Fail(err)
	}
	a.wakeScheduler()
	return Ok(nil)
}

// DeleteScheduledTask 删除定时任务
func (a *AppService) DeleteScheduledTask(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteScheduledTask(id); err != nil {
		return Fail(err)
	}
	a.wakeScheduler()
	return Ok(nil)
}

// SetScheduledTaskEnabled 启用/停用定时任务；启用时重算 next_run
func (a *AppService) SetScheduledTaskEnabled(id string, enabled bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	nextRun := ""
	if enabled {
		t, err := a.DB.GetScheduledTask(id)
		if err != nil {
			return Fail(err)
		}
		nextRun = computeNextRun(t, nowStr())
	}
	if err := a.DB.SetTaskEnabled(id, enabled, nextRun); err != nil {
		return Fail(err)
	}
	a.wakeScheduler()
	return Ok(nextRun)
}

// RunScheduledTaskNow 立即手动执行一次（不影响下次排期）
func (a *AppService) RunScheduledTaskNow(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	t, err := a.DB.GetScheduledTask(id)
	if err != nil {
		return Fail(err)
	}
	status, result := a.executeTask(t)
	// 手动执行只更新运行结果，不改动 next_run / enabled
	_ = a.DB.SetTaskRunResult(t.ID, nowStr(), status, result, t.NextRun, t.Enabled)
	if t.Notify {
		icon := "✅"
		if status != "ok" {
			icon = "⚠️"
		}
		a.sendWebhookNotify(icon+" 定时任务："+t.Name, result)
	}
	if status != "ok" {
		return FailMsg(result)
	}
	return OkMsg(result, result)
}

// validateScheduledTask 基础字段校验
func validateScheduledTask(t *db.ScheduledTask) error {
	switch t.Action {
	case "app", "dir", "url", "command", "http":
	default:
		return fmt.Errorf("未知的动作类型: %s", t.Action)
	}
	switch t.ScheduleKind {
	case "once", "interval", "daily", "weekly", "monthly":
	default:
		return fmt.Errorf("未知的调度类型: %s", t.ScheduleKind)
	}
	if t.ScheduleKind == "interval" && t.IntervalSec < 5 {
		return fmt.Errorf("间隔不能小于 5 秒")
	}
	if t.Action == "http" && t.HTTPMethod == "" {
		t.HTTPMethod = "GET"
	}
	return nil
}
