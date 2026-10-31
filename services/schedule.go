package services

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	// 一次性任务若计算不出有效时间（时间已过）：直接拒绝，不再"存下来但停用"。
	// 静默降级最误导：前端收到成功提示，用户回头却找不到一条能跑的任务，
	// 只会以为调度器坏了。其它类型算不出时间属于兜底（参数异常），仍允许保存但停用。
	if t.NextRun == "" {
		if t.ScheduleKind == "once" {
			return FailMsg("设定的执行时间已过，请选择将来的时间")
		}
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
		// 只有调度规则本身变了才重算 next_run。
		// 过去无条件重算：改个名字、换条命令，间隔任务的节拍就从「3 分钟后」被推回
		// 「一整个周期之后」，用户以为任务丢了。
		prev, perr := a.DB.GetScheduledTask(t.ID)
		recalc := perr != nil || prev == nil || prev.NextRun == "" || !sameSchedule(prev, &t)
		if recalc {
			t.NextRun = computeNextRun(&t, nowStr())
			if t.NextRun == "" {
				if t.ScheduleKind == "once" {
					return FailMsg("设定的执行时间已过，请选择将来的时间")
				}
				t.Enabled = false // 兜底：算不出下次运行时不留假启用任务
			}
		} else {
			t.NextRun = prev.NextRun
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

// SetScheduledTaskSort 调整任务的显示顺序（拖拽排序），不触碰调度规则与 next_run。
// 列表按 sort ASC, created_at ASC 排；前端拖完把整段顺序按 index*10 回写，
// 留出间隔便于以后插入。
func (a *AppService) SetScheduledTaskSort(id string, sort int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		return FailMsg("任务 ID 不能为空")
	}
	if err := a.DB.SetScheduledTaskSort(id, sort); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// DeleteScheduledTask 删除定时任务
func (a *AppService) DeleteScheduledTask(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteScheduledTaskToRecycle(id); err != nil {
		return Fail(err)
	}
	// 级联清理执行历史，避免历史表残留孤儿记录（保留策略只按时间/条数收敛，不感知任务删除）
	_ = a.DB.DeleteScheduleRunsForTask(id)
	a.wakeScheduler()
	return Ok(nil)
}

// GetScheduleRuns 返回指定任务的执行历史（按时间倒序，最多 limit 条）
func (a *AppService) GetScheduleRuns(taskID string, limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	runs, err := a.DB.ListScheduleRuns(taskID, limit)
	return wrap(runs, err)
}

// GetAllScheduleRuns 返回全部任务的执行历史（按时间倒序，最多 limit 条）。
// 单任务视角回答不了「过去一周失败了几次」，页面统计需要跨任务的一次查询。
func (a *AppService) GetAllScheduleRuns(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	runs, err := a.DB.ListAllScheduleRuns(limit)
	return wrap(runs, err)
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

// SetAllScheduledTasksEnabled 批量启用/停用。
// 只处理普通定时任务：action='todo' 是待办重复的内部实现，入口在待办页，
// 不该被这里的「暂停所有」误停（否则用户只是投个屏，重复待办也一起哑了）。
// 启用时沿用单条启用的口径：一次性任务时间已过则保持停用，不让 nextRun 为空却标成运行中。
func (a *AppService) SetAllScheduledTasksEnabled(enabled bool) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListScheduledTasks()
	if err != nil {
		return Fail(err)
	}
	changed := 0
	for i := range list {
		t := &list[i]
		if t.Action == "todo" || t.Enabled == enabled {
			continue
		}
		nextRun := ""
		on := enabled
		if on {
			nextRun = computeNextRun(t, nowStr())
			if nextRun == "" {
				on = false // 一次性任务时间已过：拒绝启用
			}
		}
		if err := a.DB.SetTaskEnabled(t.ID, on, nextRun); err != nil {
			return Fail(err)
		}
		if on == enabled {
			changed++
		}
	}
	a.wakeScheduler()
	return Ok(changed)
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
	startedAt := time.Now()
	status, result := a.executeTask(t)
	durationMs := time.Since(startedAt).Milliseconds()
	// 操作流水：手动触发才记（定时后台跑几千条会把时间线冲垮，
	// 那些该去定时任务自己的执行记录里看）。
	a.NoteActivity(db.ActivityScheduleRun, t.ID, t.Name)
	// 手动执行只更新运行结果，不改动 next_run / enabled
	_ = a.DB.SetTaskRunResult(t.ID, nowStr(), status, result, t.NextRun, t.Enabled)
	// 追加一条执行历史（含耗时）
	_ = a.DB.InsertScheduleRun(t.ID, t.Name, status, result, durationMs)
	// 执行结果按 t.Notify 推送给系统通知 + 已配置机器人（结果/耗时统一拼进消息体）
	a.notifyScheduleRun(t, status, result, durationMs)
	if status != "ok" {
		return FailMsg(result)
	}
	return OkMsg(result, result)
}

// sameSchedule 判断两次保存的调度规则是否等价（忽略名称/动作/通知等业务字段）。
//
// 只比规则本身，是为了让"改个名字"不重置节拍；只要规则没变，上次算好的 next_run
// 就还能用。
func sameSchedule(a, b *db.ScheduledTask) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ScheduleKind == b.ScheduleKind &&
		a.RunAt == b.RunAt &&
		a.IntervalSec == b.IntervalSec &&
		a.TimeOfDay == b.TimeOfDay &&
		a.Weekdays == b.Weekdays
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

	// 失败重试次数：钳到 0..3 而不是报错。
	//
	// 越界只可能来自旧前端漏传字段或手改库，为这点小事让整条任务保存失败
	// 不值得；但也不能放任（比如 100 次会把一次失败变成十五分钟的连续重试）。
	// 「once」任务没有下一次调度，重试次数无意义，一并归零。
	if t.RetryCount < 0 {
		t.RetryCount = 0
	}
	if t.RetryCount > 3 {
		t.RetryCount = 3
	}
	if t.ScheduleKind == "once" {
		t.RetryCount = 0
	}

	// 验证间隔时间
	if t.ScheduleKind == "interval" {
		if t.IntervalSec < 5 {
			return fmt.Errorf("间隔不能小于 5 秒")
		}
		// 上限放到 30 天：前端本来就提供「天」这个单位，卡在 24 小时等于
		// 「选 2 天必定保存失败」，而这类巡检/备份任务动辄就是几天一次。
		if t.IntervalSec > 30*86400 {
			return fmt.Errorf("间隔不能大于 30 天")
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

	// 验证星期：与 store/computeNextRun（parseWeekdays 按整数 0-6 解析，0=周日）保持一致。
	// 前端 SchedulePage 发送的是整数数组（如 "1,2,3"），故此处应按整数校验；
	// 同时兼容历史 mon..sun 写法（归一化为整数后判定），避免已存数据被拒。
	if t.Weekdays != "" {
		for _, day := range strings.Split(t.Weekdays, ",") {
			day = strings.TrimSpace(strings.ToLower(day))
			if day == "" {
				continue
			}
			var n int
			var perr error
			switch day {
			case "mon":
				n = 1
			case "tue":
				n = 2
			case "wed":
				n = 3
			case "thu":
				n = 4
			case "fri":
				n = 5
			case "sat":
				n = 6
			case "sun":
				n = 0
			default:
				n, perr = strconv.Atoi(day)
			}
			if perr != nil || n < 0 || n > 6 {
				return fmt.Errorf("无效的星期: %s（应为 0-6，0=周日，或 mon..sun）", day)
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
