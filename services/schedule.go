package services

import (
	"fmt"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/validators"
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
	// 一次性任务若计算不出有效时间（如时间已过）：仍允许保存，但必须置 enabled=false
	// 避免产出「enabled=1 且 next_run=''」的假启用任务（调度器永不触发、UI 显示已启用）
	if t.NextRun == "" {
		t.Enabled = false
	}
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
		if t.NextRun == "" {
			t.Enabled = false // 一次性任务时间已过：不启用
		}
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
		if nextRun == "" {
			enabled = false // 一次性任务时间已过：拒绝启用
		}
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
	// 验证名称
	if err := validators.ValidateName(t.Name, 1, 100); err != nil {
		return err
	}

	// 验证动作类型
	switch t.Action {
	case "app", "dir", "url", "command", "http":
	default:
		return fmt.Errorf("未知的动作类型: %s", t.Action)
	}

	// 验证调度类型
	switch t.ScheduleKind {
	case "once", "interval", "daily", "weekly", "monthly":
	default:
		return fmt.Errorf("未知的调度类型: %s", t.ScheduleKind)
	}

	// 验证间隔时间
	if t.ScheduleKind == "interval" {
		if t.IntervalSec < 5 {
			return fmt.Errorf("间隔不能小于 5 秒")
		}
		if t.IntervalSec > 86400 {
			return fmt.Errorf("间隔不能大于 24 小时")
		}
	}

	// 验证目标路径/URL
	switch t.Action {
	case "app", "dir":
		if t.Target == "" {
			return fmt.Errorf("目标路径不能为空")
		}
		if err := validators.ValidatePath(t.Target); err != nil {
			return err
		}
	case "url":
		if t.Target == "" {
			return fmt.Errorf("URL 不能为空")
		}
		if err := validators.ValidateURL(t.Target); err != nil {
			return err
		}
	case "command":
		if t.Target == "" {
			return fmt.Errorf("命令不能为空")
		}
		if len(t.Target) > 1000 {
			return fmt.Errorf("命令长度不能超过 1000 字符")
		}
	}

	// 验证工作时间
	if t.TimeOfDay != "" {
		if _, err := validators.ParseTimeOfDay(t.TimeOfDay); err != nil {
			return err
		}
	}

	// 验证星期
	if t.Weekdays != "" {
		validDays := map[string]bool{
			"mon": true, "tue": true, "wed": true, "thu": true,
			"fri": true, "sat": true, "sun": true,
		}
		for _, day := range strings.Split(t.Weekdays, ",") {
			day = strings.TrimSpace(strings.ToLower(day))
			if day != "" && !validDays[day] {
				return fmt.Errorf("无效的星期: %s", day)
			}
		}
	}

	// 验证 HTTP 方法
	if t.Action == "http" && t.HTTPMethod == "" {
		t.HTTPMethod = "GET"
	}
	if t.Action == "http" {
		validMethods := map[string]bool{
			"GET": true, "POST": true, "PUT": true, "DELETE": true,
			"PATCH": true, "HEAD": true, "OPTIONS": true,
		}
		if !validMethods[strings.ToUpper(t.HTTPMethod)] {
			return fmt.Errorf("无效的 HTTP 方法: %s", t.HTTPMethod)
		}
	}

	return nil
}
