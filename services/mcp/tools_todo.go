package mcp

// 待办域工具：列出、新建、标记完成。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

// updateArg 只在 AI 显式传了 key 时取新值，没传则保留原值。
// 不用 mcpsrv.Arg 的零值兜底：那会把「没传」和「传空串清空」混成一样，
// AI 只改优先级时其余 8 个字段会被全量 UpdateTodo 清空。
func updateArg(args map[string]any, key, current string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return current
}

func registerTodoTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Todo.ListTodos())
	}, mcpsrv.LevelRead, "todo_list", "列出所有待办", nil, nil)

	// ---- 低危写 ----

	r.add(func(args map[string]any) (any, error) {
		title := mcpsrv.Arg(args, "title")
		if title == "" {
			return nil, errors.New("缺少参数 title")
		}
		// 与 UI 的 CreateTodo 走同一签名：这 9 个字段 DB 全都支持，
		// 早先 MCP 只透传前 4 个、后 5 个硬编码空串，等于 AI 建的待办
		// 没法设提醒 / 时段 / 标签 / 重复，而 UI 里同样一条却可以。
		return unwrap(s.Todo.CreateTodo(
			title,
			mcpsrv.Arg(args, "priority"),
			mcpsrv.Arg(args, "dueDate"),
			mcpsrv.Arg(args, "note"),
			mcpsrv.Arg(args, "startTime"),
			mcpsrv.Arg(args, "endTime"),
			mcpsrv.Arg(args, "reminderTime"),
			mcpsrv.Arg(args, "recurrence"),
			mcpsrv.Arg(args, "tags"),
		))
	}, mcpsrv.LevelWrite, "todo_create", "新建待办", []string{"title"},
		map[string]any{
			"title":        str("待办标题"),
			"priority":     str("优先级：none / low / medium / high，留空为 none；不要写 normal（UI 不认）"),
			"dueDate":      str("截止日期，格式 YYYY-MM-DD，可留空"),
			"note":         str("备注，可留空"),
			"startTime":    str("开始时间，格式 YYYY-MM-DD HH:MM:SS，可留空"),
			"endTime":      str("结束时间，格式 YYYY-MM-DD HH:MM:SS，可留空"),
			"reminderTime": str("提醒时间，格式 YYYY-MM-DD HH:MM:SS，可留空；到点通知一次，响过不重复"),
			"tags":         str(`标签，JSON 数组字符串，如 ["工作","紧急"]；可留空`),
			"recurrence":   str(`重复规则，JSON 字符串：{"kind":"daily|weekly|monthly","timeOfDay":"09:00","weekdays":"1,2,3"}；weekdays 用 0-6（0=周日），仅 weekly 需要；可留空`),
		})

	// 增量更新：先读现值，只覆盖 AI 显式传的字段，再走全量 UpdateTodo。
	// 不新开 DB 方法——UpdateTodo 已经处理了 reminder_sent 复位、completed_at
	// 与 status 绑定、重复调度同步，重实现一遍只会漏掉其中一条。
	r.add(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 todo_list 获取）")
		}
		cur, err := s.App.DB.GetTodo(id)
		if err != nil {
			return nil, err
		}
		return unwrap(s.Todo.UpdateTodo(
			id,
			updateArg(args, "title", cur.Title),
			updateArg(args, "priority", cur.Priority),
			updateArg(args, "dueDate", cur.DueDate),
			updateArg(args, "note", cur.Note),
			updateArg(args, "startTime", cur.StartTime),
			updateArg(args, "endTime", cur.EndTime),
			updateArg(args, "reminderTime", cur.ReminderTime),
			updateArg(args, "recurrence", cur.Recurrence),
			updateArg(args, "tags", cur.Tags),
			updateArg(args, "status", cur.Status),
		))
	}, mcpsrv.LevelWrite, "todo_update", "增量修改一条待办：只传 id 和要改的字段，其余字段保持原值。要清空某字段就传空字符串", []string{"id"},
		map[string]any{
			"id":           str("待办 id，用 todo_list 获取"),
			"title":        str("新标题（留空不传表示不改；传空字符串会被拒绝，标题不能为空）"),
			"priority":     str("优先级：none / low / medium / high"),
			"dueDate":      str("截止日期 YYYY-MM-DD；传空字符串清空"),
			"note":         str("备注；传空字符串清空"),
			"startTime":    str("开始时间 YYYY-MM-DD HH:MM:SS；传空字符串清空"),
			"endTime":      str("结束时间 YYYY-MM-DD HH:MM:SS；传空字符串清空"),
			"reminderTime": str("提醒时间 YYYY-MM-DD HH:MM:SS；传空字符串清空并复位已提醒标记"),
			"tags":         str(`标签 JSON 数组字符串，如 ["工作","紧急"]；传 [] 清空`),
			"recurrence":   str(`重复规则 JSON 字符串，格式同 todo_create；传空字符串取消重复`),
			"status":       str("状态：todo / doing / done"),
		})

	r.add(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 todo_list 获取）")
		}
		return unwrap(s.Todo.SetTodoStatus(id, "done"))
	}, mcpsrv.LevelWrite, "todo_done", "把某待办标记为已完成", []string{"id"},
		map[string]any{"id": str("待办 id")})
}
