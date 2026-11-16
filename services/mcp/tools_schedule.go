package mcp

// 定时任务域工具：看任务、看执行历史、立即跑一次、建 AI 任务。
//
// 为什么只开放创建 action=ai 的任务（不给 app/dir/url/command/http）：
//   - 建任务是用户的排期决定，所以 create 落在写等级、每次弹审批卡，
//     用户在卡上看到「建什么、多久跑一次」点允许才落库；
//   - action=command 的任务本质是「任意命令 + 定时执行」，给它开个 MCP
//     写入口，system_command 那道高危门就整条架空了——这个口子在工具层
//     关死，不依赖用户有没有点「记住」；
//   - action=ai 的任务到点只是跑一段提示词、产出一条 AI 会话，最坏后果是
//     烧 token，量级和 agent 自己连跑几轮工具相当，交给审批闸门兜得住。
//
// 只读 + 「立即执行一次」+「建 AI 任务」是有意的边界：命令类任务仍然
// 只能在定时任务页由用户亲手建，AI 只能触发，不能创造。

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
			case "daily", "weekly":
				row["timeOfDay"] = t.TimeOfDay
			case "monthly":
				row["timeOfDay"] = t.TimeOfDay
				row["dayOfMonth"] = t.DayOfMonth
			case "cron":
				row["cronExpr"] = t.CronExpr
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
		"列出全部定时任务：动作（app/dir/url/command/http/ai/todo）、目标、是否启用、调度方式、下次与上次执行时间、上次结果。查「我的备份/巡检任务在跑吗」先看这里，再看 schedule_runs 有没有失败记录。action=ai 会到点跑一段提示词并产出一条 AI 会话，结果串里的「会话 <id>」可回看；action=todo 是待办的重复规则自动生成的任务，不是用户手动建的。schedule_runs 的 skipped 表示关机错过或不在有效期内，不是执行失败",
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
		"立即执行一次已有的定时任务（等价于在定时任务页点「立即运行」）。不影响下次排期、不改启用状态。只能触发已有的任务——新建要用 schedule_create。命令类任务会等到结束或超时才返回，可能较慢。action=ai 的任务不等待：返回的是「已在后台开始」，不是执行结果——过一会儿用 schedule_runs 看这一轮的 status/result 和对应会话，别把它当成已经跑完了",
		[]string{"taskId"},
		map[string]any{"taskId": str("任务 ID，来自 schedule_list 的 id")})

	r.add(func(args map[string]any) (any, error) {
		name := strings.TrimSpace(mcpsrv.Arg(args, "name"))
		prompt := strings.TrimSpace(mcpsrv.Arg(args, "prompt"))
		kind := strings.TrimSpace(mcpsrv.Arg(args, "scheduleKind"))
		if name == "" {
			return nil, errors.New("缺少参数 name（任务名，一句人话，如「系统占用巡检」）")
		}
		if prompt == "" {
			return nil, errors.New("缺少参数 prompt（每次执行时 AI 要做的事）")
		}
		if kind == "" {
			return nil, errors.New("缺少参数 scheduleKind（interval/daily/weekly/monthly/cron/once）")
		}
		aiMode := strings.TrimSpace(mcpsrv.Arg(args, "aiMode"))
		if aiMode == "" {
			// 默认 agent：会建定时任务的场景（巡检、汇报）几乎都要调工具查真实状态，
			// 纯问答模式查不了；用户明确要「不挂工具的纯回答」时才传 chat。
			aiMode = "agent"
		}
		if aiMode != "agent" && aiMode != "chat" {
			return nil, errors.New("aiMode 只能是 agent（可调工具）或 chat（纯问答）")
		}
		t := db.ScheduledTask{
			Name:         name,
			Action:       "ai",
			Target:       prompt,
			ScheduleKind: kind,
			IntervalSec:  mcpsrv.ArgInt(args, "intervalSec", 0),
			TimeOfDay:    strings.TrimSpace(mcpsrv.Arg(args, "timeOfDay")),
			Weekdays:     strings.TrimSpace(mcpsrv.Arg(args, "weekdays")),
			DayOfMonth:   mcpsrv.ArgInt(args, "dayOfMonth", 0),
			CronExpr:     strings.TrimSpace(mcpsrv.Arg(args, "cronExpr")),
			RunAt:        strings.TrimSpace(mcpsrv.Arg(args, "runAt")),
			Enabled:      true,
			AIMode:       aiMode,
			AIScope:      "new",
			Notify:       mcpsrv.ArgBool(args, "notify"),
		}
		// 复用页面那条创建链路：校验、算 next_run、建专属会话空间、唤醒调度器
		// 全在里面，这里不复制任何一条规则——两套校验迟早会漂移。
		res := s.App.CreateScheduledTask(t)
		if res.Code != 0 {
			return nil, errors.New(res.Msg)
		}
		created, _ := res.Data.(*db.ScheduledTask)
		if created == nil {
			return unwrap(res)
		}
		return map[string]any{
			"id":       created.ID,
			"name":     created.Name,
			"nextRun":  created.NextRun,
			"enabled":  created.Enabled,
			"aiMode":   created.AIMode,
			"schedule": created.ScheduleKind,
		}, nil
	}, mcpsrv.LevelWrite, "schedule_create",
		"新建一个 action=ai 的定时任务：到点用指定的提示词跑一轮 AI，产出一条会话（侧栏闹钟空间里可回看）。适用「以后每半小时/每天早上帮我看一次 xx」这类周期性诉求；用户没说频率时先问，不要替他拍。只能建 AI 任务——要建命令/应用/网址类任务时，让用户去定时任务页手动建。调度参数按 scheduleKind 只传相关的：interval 传 intervalSec（秒，≥5），daily/weekly/monthly 传 timeOfDay（HH:MM 或 HH:MM:SS），weekly 可加 weekdays（CSV，0=周日..6=周六），monthly 可加 dayOfMonth（1-31，-1=最后一天），cron 传 cronExpr（5 段），once 传 runAt（YYYY-MM-DD HH:MM:SS）。创建会弹审批卡，用户允许才生效",
		[]string{"name", "prompt", "scheduleKind"},
		map[string]any{
			"name":         str("任务名，简短可辨认，如「系统占用巡检」"),
			"prompt":       str("每次执行时 AI 收到的提示词，写清楚要做什么、产出什么（跑的时候用户不在场，结果落会话里）"),
			"scheduleKind": str("调度方式：interval=固定间隔 / daily=每天 / weekly=每周 / monthly=每月 / cron=cron 表达式 / once=一次性"),
			"intervalSec":  intp("interval 专用：间隔秒数，最小 5。半小时=1800，一小时=3600"),
			"timeOfDay":    str("daily/weekly/monthly 专用：执行时刻，HH:MM 或 HH:MM:SS"),
			"weekdays":     str("weekly 可选：周几执行，CSV，0=周日..6=周六，如「1,3,5」"),
			"dayOfMonth":   intp("monthly 可选：每月几号，1-31，-1=最后一天"),
			"cronExpr":     str("cron 专用：标准 5 段 cron 表达式（分 时 日 月 周）"),
			"runAt":        str("once 专用：执行时间，YYYY-MM-DD HH:MM:SS"),
			"aiMode":       str("可省。agent=可调工具（默认，巡检/查状态用它）｜chat=纯问答不挂工具"),
			"notify":       mcpsrv.Bool("可省。执行完是否发通知（含机器人渠道），默认不发"),
		},
		// 「本次会话都允许」按**任务名**记忆，不按工具名。
		//
		// 按工具名记的后果是可预料的：用户批准了一次「系统占用巡检」，之后任意任务
		// （包括每天关机、定时发通知）都能在无人过问的情况下被建出来。用户当初同意的
		// 是这一个任务，不是「以后随便建」。
		withGrantKey(func(args map[string]any) (string, bool) {
			name := strings.TrimSpace(mcpsrv.Arg(args, "name"))
			if name == "" {
				// 拿不到任务名就只给单次授权：记不住比记错安全。
				return "", false
			}
			return "schedule_create:" + name, true
		}))
}
