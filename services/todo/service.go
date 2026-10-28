// Package todo 待办服务
// 承载原 AppService 的待办领域方法（增删改查/子任务/看板状态）。
package todo

import (
	"quickdock/services"
)

// TodoService 承载待办领域方法。
// App 回指宿主 AppService：DB 为宿主导出字段；重复调度的同步经宿主 SyncTodoSchedule 转发。
type TodoService struct {
	App *services.AppService
}

// NewTodoService 创建待办门面服务，App 为宿主服务引用。
func NewTodoService(app *services.AppService) *TodoService {
	return &TodoService{App: app}
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本）
func (s *TodoService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// CreateTodo 新建待办（含起止时间、提醒时间、标签与重复配置）
func (s *TodoService) CreateTodo(title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	t, err := s.App.DB.CreateTodo(title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags)
	if err != nil {
		return services.Fail(err)
	}
	s.App.SyncTodoSchedule(t)
	return services.Ok(t)
}

// ListTodos 列出所有待办
func (s *TodoService) ListTodos() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	todos, err := s.App.DB.ListTodos()
	return services.Wrap(todos, err)
}

// SearchTodos 命令面板检索待办：标题 / 备注 / 标签模糊匹配。
// 前端本地没有待办池（不像 items/notes 有一份常驻列表），这一路是唯一来源，
// 失败时要给出明确错误而不是静默返回空——静默空会让「待办到底有没有进面板」无法诊断。
func (s *TodoService) SearchTodos(q string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	todos, err := s.App.DB.SearchTodos(q, 20)
	return services.Wrap(todos, err)
}

// CreateSubtask 新建子任务（归属指定父待办）
func (s *TodoService) CreateSubtask(parentID, title string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	t, err := s.App.DB.CreateSubtask(parentID, title)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(t)
}

// UpdateTodo 更新待办（含起止时间、提醒时间、标签、重复配置与状态）
func (s *TodoService) UpdateTodo(id, title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateTodo(id, title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status); err != nil {
		return services.Fail(err)
	}
	if t, err := s.App.DB.GetTodo(id); err == nil {
		s.App.SyncTodoSchedule(t)
	}
	return services.Ok(nil)
}

// SetTodoStatus 设置待办状态（看板拖拽）
func (s *TodoService) SetTodoStatus(id, status string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.SetTodoStatus(id, status); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// SetTodoSort 调整排序权重（看板拖拽落位 / 子任务上下移）。
// 与 SetTodoStatus 分开：拖动既要换列又要定位置，合在一个接口里会让
// 「只换状态」也带上 sort 语义，排查时不好判断谁改了顺序。
func (s *TodoService) SetTodoSort(id string, sort int) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if id == "" {
		return services.FailMsg("待办 ID 不能为空")
	}
	if err := s.App.DB.SetTodoSort(id, sort); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// ToggleTodo 切换完成状态
func (s *TodoService) ToggleTodo(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.ToggleTodo(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteTodo 删除待办（同时清理其重复调度记录）
func (s *TodoService) DeleteTodo(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteTodo(id); err != nil {
		return services.Fail(err)
	}
	// 派生的重复调度记录走硬删除：它不是用户建的，不该出现在回收站里
	_ = s.App.DB.DeleteScheduledTask("recur-" + id)
	return services.Ok(nil)
}

// ClearCompletedTodos 清除已完成项
func (s *TodoService) ClearCompletedTodos() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.ClearCompletedTodos(); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
