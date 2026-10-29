package mcp

// 待办域工具：列出、新建、标记完成。

import (
	"errors"

	mcpsrv "quickdock/internal/mcp"
)

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
		return unwrap(s.Todo.CreateTodo(title, mcpsrv.Arg(args, "priority"), mcpsrv.Arg(args, "dueDate"),
			mcpsrv.Arg(args, "note"), "", "", "", "", ""))
	}, mcpsrv.LevelWrite, "todo_create", "新建待办", []string{"title"},
		map[string]any{
			"title":    str("待办标题"),
			"priority": str("优先级：low / normal / high，留空 normal"),
			"dueDate":  str("截止日期，格式 YYYY-MM-DD，可留空"),
			"note":     str("备注，可留空"),
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
