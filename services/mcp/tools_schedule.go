package mcp

// 定时任务域工具：看任务、看执行历史、立即跑一次。
//
// 为什么不给创建 / 修改 / 删除：
//   - 建任务是用户的排期决定，AI 建的定时任务会在用户不知情时长期反复执行，
//     而它一旦跑错，用户看到的是「应用自己在动」，没人知道是谁设的；
//   - 更实际的原因是安全：action=command 的任务本质是「任意命令 + 定时执行」，
//     给它开个 MCP 写入口，system_command 那道高危门就整条架空了。
//
// 只读 + 「立即执行一次」是有意的边界：跑什么是用户写好的，AI 只能触发，
// 不能创造。触发也不影响 next_run 与启用状态。

import (
	"errors"
	"fmt"
	"strings"

	"quickdock/internal/db"
	mcpsrv "quickdock/internal/mcp"
)

func registerScheduleTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str
	intp := mcpsrv.Int

	r.add(func(args map[string]any) (any, error) {
		res := s.App.ListScheduledTasks()
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		tasks, _ := res.Data.([]db.ScheduledTask)
		out := make([]map[string]any, 0, len(tasks))
		for _, t := range tasks {
			row := map[string]any{
				"id":           t.ID,
				"name":         t.Name,
				"action":       t.Action,
				"target":       mcpsrv.TruncateRunes(t.Target, 200),
				"enabled":      t.Enabled,
				"scheduleKind": t.ScheduleKind,
				"nextRun":      t.NextRun,
				"lastRun":      t.LastRun,
				"lastStatus":   t.LastStatus,
				"lastResult":   mcpsrv.TruncateRunes(t.LastResult, 200),
			}
			// 调度参数按 kind 只给相关的那个：三个字段一起给，模型很容易把
			// 上一次保存残留的 intervalSec 当成当前频率。
			switch t.ScheduleKind {
			case "interval":
				row["intervalSec"] = t.IntervalSec
			case "daily", "weekly", "monthly":
				row["timeOfDay"] = t.TimeOfDay
			case "once":
				row["runAt"] = t.RunAt
			}
			if t.Weekdays != "" {
				row["weekdays"] = t.Weekdays
			}
			out = append(out, row)
		}
		return map[string]any{"count": len(out), "tasks": out}, nil
	}, mcpsrv.LevelRead, "schedule_list",
		"列出全部定时任务：动作（app/dir/url/command/http/todo）、目标、是否启用、调度方式、下次与上次执行时间、上次结果。查「我的备份/巡检任务在跑吗」先看这里，再看 schedule_runs 有没有失败记录。注意 action=todo 是待办的重复规则自动生成的任务，不是用户手动建的",
		nil, nil)

	r.add(func(args map[string]any) (any, error) {
		limit := mcpsrv.ArgInt(args, "limit", 20)
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		taskID := strings.TrimSpace(mcpsrv.Arg(args, "taskId"))
		var (
			runs []db.ScheduleRun
			err  error
		)
		if taskID != "" {
			runs, err = s.App.DB.ListScheduleRuns(taskID, limit)
		} else {
			runs, err = s.App.DB.ListAllScheduleRuns(limit)
		}
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(runs))
		for _, run := range runs {
			out = append(out, map[string]any{
				"taskId":     run.TaskID,
				"taskName":   run.TaskName,
				"runAt":      run.RunAt,
				"status":     run.Status,
				"result":     mcpsrv.TruncateRunes(run.Result, 300),
				"durationMs": run.DurationMs,
			})
		}
		return map[string]any{"count": len(out), "runs": out}, nil
	}, mcpsrv.LevelRead, "schedule_runs",
		"查看定时任务的执行历史（时间倒序）：哪个任务、什么时候跑的、成功还是失败、结果摘要、耗时。省略 taskId 时返回全部任务的最近记录——回答「昨晚有没有任务失败」用它一次就够，不必逐任务去查",
		nil,
		map[string]any{
			"taskId": str("可选。只看某个任务，来自 schedule_list 的 id"),
			"limit":  intp("返回条数，默认 20，最多 100"),
		})

	r.add(func(args map[string]any) (any, error) {
		id := strings.TrimSpace(mcpsrv.Arg(args, "taskId"))
		if id == "" {
			return nil, errors.New("缺少参数 taskId（用 schedule_list 获取）")
		}
		res := s.App.RunScheduledTaskNow(id)
		if res.Code != 0 {
			// 执行失败时也要让模型拿到「跑过了但失败了」这件事，
			// 而不是笼统的调用错误——它得知道自己该不该重试、要不要通知用户。
			return nil, fmt.Errorf("任务已执行但失败：%s", res.Msg)
		}
		return unwrap(res)
	}, mcpsrv.LevelWrite, "schedule_run_now",
		"立即执行一次已有的定时任务（等价于在定时任务页点「立即运行」）。不影响下次排期、不改启用状态。只能触发用户已创建的任务，不能新建——执行内容是人写的，AI 只负责在需要时跑一次。命令类任务会等到结束或超时才返回，可能较慢",
		[]string{"taskId"},
		map[string]any{"taskId": str("任务 ID，来自 schedule_list 的 id")})
}
