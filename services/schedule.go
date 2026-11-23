package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
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
	// AI 任务一保存就在侧栏建好专属的闹钟空间，不用等第一次执行才冒出来。
	// 用库里刚写入的 created_at 命名（RFC3339），保证空间名里的创建时间是历史事实。
	if t.Action == "ai" && a.EnsureScheduleSpaceFn != nil {
		a.EnsureScheduleSpaceFn(t.ID, t.Name, created.CreatedAt)
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
	// 提前取一次旧值：改名同步空间名、重算 next_run 都要用，别在各分支里重复查。
	prev, perr := a.DB.GetScheduledTask(t.ID)
	if t.Enabled {
		// 只有调度规则本身变了才重算 next_run。
		// 过去无条件重算：改个名字、换条命令，间隔任务的节拍就从「3 分钟后」被推回
		// 「一整个周期之后」，用户以为任务丢了。
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
	if t.Action == "ai" {
		// created_at 以库里的旧值为准：前端回传的字段可能被漏掉，
		// 而空间名里的创建时间一旦漂移，同一个任务就会有两个不同名字的空间。
		createdAt := t.CreatedAt
		if perr == nil && prev != nil && prev.CreatedAt != "" {
			createdAt = prev.CreatedAt
		}
		if a.EnsureScheduleSpaceFn != nil {
			a.EnsureScheduleSpaceFn(t.ID, t.Name, createdAt)
		}
		// 改名只在空间名还是旧自动名时同步；用户手动改过名就不动它。
		if perr == nil && prev != nil && prev.Name != t.Name && a.SyncScheduleSpaceNameFn != nil {
			a.SyncScheduleSpaceNameFn(t.ID, prev.Name, t.Name, createdAt)
		}
		// 空间刚被认领时（升级后第一次编辑），老会话可能还是「一次运行一条」，
		// 顺手收拢成一条；幂等，平时是空操作。
		if _, merr := a.DB.MergeScheduledConversations(t.ID); merr != nil {
			logger.W("[schedule] 合并定时任务历史会话失败 task=%s: %v", t.ID, merr)
		}
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
	// AI 动作一轮可能跑好几分钟（工具循环十几轮），走同步 RPC 会把前端卡到超时。
	// 命令/HTTP 最长 30 秒还能等，AI 不行：改成后台跑 + 立即返回，结果与定时
	// 触发走同一套收尾（运行历史 + 通知），只是不动 next_run / enabled。
	if t.Action == "ai" {
		return a.runAITaskAsync(t)
	}
	startedAt := time.Now()
	status, result := a.executeTask(t)
	durationMs := time.Since(startedAt).Milliseconds()
	// 操作流水：手动触发才记（定时后台跑几千条会把时间线冲垮，
	// 那些该去定时任务自己的执行记录里看）。
	a.NoteActivity(db.ActivityScheduleRun, t.ID, t.Name)
	// 手动执行只更新运行结果，不改动 next_run / enabled
	runAt := nowStr()
	_ = a.DB.SetTaskRunResult(t.ID, runAt, status, result, t.NextRun, t.Enabled)
	// 追加一条执行历史（含耗时）
	_ = a.DB.InsertScheduleRun(t.ID, t.Name, status, result, durationMs)
	a.emitScheduleRunUpdated(t, status, result, runAt)
	// 执行结果按 t.Notify 推送给系统通知 + 已配置机器人（结果/耗时统一拼进消息体）
	a.notifyScheduleRun(t, status, result, durationMs)
	if status != "ok" {
		return FailMsg(result)
	}
	return OkMsg(result, result)
}

// RunScheduledTaskNowAsync 立即手动执行一次，所有动作都在后台跑、RPC 立即返回。
//
// 与 RunScheduledTaskNow 的区别：那个对命令/HTTP 仍同步等满 30 秒，前端点一下
// 就卡住、没有进度也不能取消。这里把执行整体搬进 goroutine，UI 靠
// schedule:run-progress / schedule:run-updated 两个事件显示状态，并可经
// CancelScheduledRun 真正终止命令/HTTP。
//
// 收尾与同步路径完全一致（运行历史 + 通知 + 不重排 next_run），结果不从 RPC
// 返回——异步返回后再给结果没有意义，用户去运行历史/通知里看。
// 原有同步 binding 保留：MCP 调用方与 AIPage 的「空间内运行」仍需要拿返回值。
func (a *AppService) RunScheduledTaskNowAsync(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	t, err := a.DB.GetScheduledTask(id)
	if err != nil {
		return Fail(err)
	}
	if _, loaded := a.schedInflight.LoadOrStore(t.ID, struct{}{}); loaded {
		return FailMsg("该任务正在执行中")
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.schedRunCancels.Store(t.ID, cancel)
	a.taskWG.Add(1)
	go func() {
		defer a.taskWG.Done()
		defer a.schedInflight.Delete(t.ID)
		defer a.schedRunCancels.Delete(t.ID)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logger.E("QuickDock: 手动任务 %s 异步执行 panic: %v", t.ID, r)
			}
		}()

		startedAt := time.Now()
		a.emitScheduleRunProgress(t.ID, "start", "任务已开始执行")
		status, result := a.executeTaskContext(ctx, t)
		durationMs := time.Since(startedAt).Milliseconds()
		a.NoteActivity(db.ActivityScheduleRun, t.ID, t.Name)
		runAt := nowStr()
		_ = a.DB.SetTaskRunResult(t.ID, runAt, status, result, t.NextRun, t.Enabled)
		_ = a.DB.InsertScheduleRun(t.ID, t.Name, status, result, durationMs)
		a.emitScheduleRunUpdated(t, status, result, runAt)
		a.notifyScheduleRun(t, status, result, durationMs)
	}()
	return OkMsg("任务已在后台开始，可在此页查看进度或取消", "任务已在后台开始")
}

// CancelScheduledRun 取消一个正在后台执行的手动任务。
//
// 命令动作杀进程树（sysutil.KillProcess，Windows 用 taskkill /F），HTTP 动作
// 取消在途请求；两者的执行结果都会以 status=canceled 写进运行历史。
// AI 动作跑在 services/ai 自己的链路里，此处只登记了等待、不中断已发出的工具
// 调用——UI 对 AI 任务不显示取消按钮，避免误导。
func (a *AppService) CancelScheduledRun(id string) *ApiResult {
	v, ok := a.schedRunCancels.Load(id)
	if !ok {
		return FailMsg("该任务当前没有正在执行的手动运行")
	}
	v.(context.CancelFunc)()
	return OkMsg("已发送取消信号", "已发送取消信号")
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
		a.Weekdays == b.Weekdays &&
		a.CronExpr == b.CronExpr &&
		a.DayOfMonth == b.DayOfMonth &&
		a.ValidFrom == b.ValidFrom &&
		a.ValidUntil == b.ValidUntil
}

// normalizeNotifyPolicy 把通知策略归一到 always / failed / never。
// 空值（老库补列前、或调用方漏传）一律 always —— 与升级前行为一致，
// 不能因为一次升级就让用户突然收不到通知。
func normalizeNotifyPolicy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "failed":
		return "failed"
	case "never":
		return "never"
	default:
		return "always"
	}
}

// validateScheduledTask 基础字段校验
func validateScheduledTask(t *db.ScheduledTask) error {
	// 验证名称
	if err := validators.ValidateName(t.Name, 1, 100); err != nil {
		return err
	}

	// 验证动作类型
	switch t.Action {
	case "app", "dir", "url", "command", "http", "ai":
	default:
		return fmt.Errorf("未知的动作类型: %s", t.Action)
	}

	// 验证调度类型
	switch t.ScheduleKind {
	case "once", "interval", "daily", "weekly", "monthly", "cron":
	default:
		return fmt.Errorf("未知的调度类型: %s", t.ScheduleKind)
	}

	// cron：把多余空白压成单空格再校验。表达式不合法必须在这里挡住——
	// 落到 computeNextRun 只会返回 ""，任务「保存成功」却被静默停用。
	if t.ScheduleKind == "cron" {
		t.CronExpr = strings.Join(strings.Fields(t.CronExpr), " ")
		if err := validateCronExpr(t.CronExpr); err != nil {
			return err
		}
	}

	// monthly：几号是显式字段。-1=最后一天，0=老数据（沿用创建日），1..31=指定日。
	if t.ScheduleKind == "monthly" && (t.DayOfMonth < -1 || t.DayOfMonth > 31) {
		return fmt.Errorf("每月日期无效: %d（应为 1-31 或 -1 表示最后一天）", t.DayOfMonth)
	}

	// daily/weekly/monthly 缺时刻算不出 next_run，与其存一条永远不跑的任务，
	// 不如在保存时说清楚。cron 的时刻写在表达式里，不适用。
	if t.ScheduleKind == "daily" || t.ScheduleKind == "weekly" || t.ScheduleKind == "monthly" {
		if strings.TrimSpace(t.TimeOfDay) == "" {
			return fmt.Errorf("请填写执行时刻")
		}
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
	case "ai":
		if strings.TrimSpace(t.Target) == "" {
			return fmt.Errorf("AI 提示词不能为空")
		}
		// 提示词要进系统提示 + 用户消息两处，过长会把小模型的上下文挤爆。
		if len([]rune(t.Target)) > 8000 {
			return fmt.Errorf("AI 提示词不能超过 8000 字")
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

	// AI 动作：档位与作用域归一。脏值回落而不是报错，免得旧前端漏传字段时
	// 整条任务保存不了；但绝不把未知值当成 agent —— 那会静默放开工具权限。
	if t.Action == "ai" {
		switch strings.TrimSpace(t.AIMode) {
		case "readonly", "agent":
			t.AIMode = strings.TrimSpace(t.AIMode)
		default:
			t.AIMode = "chat"
		}
		// 会话作用域：现在只有一种口径——每次执行追加到任务那条会话。
		// 早期版本存过 'new'（每次运行开一条），那批老任务也一并归到 continue，
		// 否则升级后它们还会每跑一次就多出一条同名会话。
		// 列本身留着：将来若要做「每次独立运行」，改这里 + ai_schedule 的取会话逻辑即可。
		t.AIScope = "continue"
	} else {
		t.AIMode = ""
		t.AIScope = "continue"
	}

	// 有效期：空 = 不限。归一成与 next_run 同一格式，字典序即时间序。
	// 前端 datetime-local 的 'T' 分隔由 parseSchedTime 兼容。
	t.ValidFrom = strings.TrimSpace(t.ValidFrom)
	t.ValidUntil = strings.TrimSpace(t.ValidUntil)
	if t.ValidFrom != "" {
		v, ok := parseSchedTime(t.ValidFrom)
		if !ok {
			return fmt.Errorf("生效时间格式无效")
		}
		t.ValidFrom = v.Format(schedTimeLayout)
	}
	if t.ValidUntil != "" {
		v, ok := parseSchedTime(t.ValidUntil)
		if !ok {
			return fmt.Errorf("失效时间格式无效")
		}
		t.ValidUntil = v.Format(schedTimeLayout)
	}
	if t.ValidFrom != "" && t.ValidUntil != "" && t.ValidUntil <= t.ValidFrom {
		return fmt.Errorf("失效时间必须晚于生效时间")
	}
	// once 只有一个调度点：生效时间若晚于它，computeNextRun 永远返回 ""，
	// 任务会在第一次扫描时被静默停用。这种配置保存时就该说清楚，而不是
	// 让用户对着一条「已启用但从不运行」的任务卡猜原因。
	if t.ScheduleKind == "once" && t.ValidFrom != "" {
		if at, ok := parseSchedTime(t.RunAt); ok && t.ValidFrom > at.Format(schedTimeLayout) {
			return fmt.Errorf("生效时间晚于一次性任务的运行时间，该任务永远不会执行")
		}
	}

	t.NotifyPolicy = normalizeNotifyPolicy(t.NotifyPolicy)

	return nil
}
