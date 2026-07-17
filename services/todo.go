package services

// CreateTodo 新建待办（含起止时间、提醒时间、标签与重复配置）
func (a *AppService) CreateTodo(title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	t, err := a.DB.CreateTodo(title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags)
	if err != nil {
		return wrap(t, err)
	}
	a.syncTodoSchedule(t)
	return wrap(t, err)
}

// ListTodos 列出所有待办
func (a *AppService) ListTodos() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	todos, err := a.DB.ListTodos()
	return wrap(todos, err)
}

// CreateSubtask 新建子任务（归属指定父待办）
func (a *AppService) CreateSubtask(parentID, title string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	t, err := a.DB.CreateSubtask(parentID, title)
	if err != nil {
		return Fail(err)
	}
	return Ok(t)
}

// UpdateTodo 更新待办（含起止时间、提醒时间、标签、重复配置与状态）
func (a *AppService) UpdateTodo(id, title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateTodo(id, title, priority, dueDate, note, startTime, endTime, reminderTime, recurrence, tags, status); err != nil {
		return Fail(err)
	}
	if t, err := a.DB.GetTodo(id); err == nil {
		a.syncTodoSchedule(t)
	}
	return Ok(nil)
}

// SetTodoStatus 设置待办状态（看板拖拽）
func (a *AppService) SetTodoStatus(id, status string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetTodoStatus(id, status); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ToggleTodo 切换完成状态
func (a *AppService) ToggleTodo(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ToggleTodo(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// DeleteTodo 删除待办（同时清理其重复调度记录）
func (a *AppService) DeleteTodo(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteTodo(id); err != nil {
		return Fail(err)
	}
	_ = a.DB.DeleteScheduledTask("recur-" + id)
	return Ok(nil)
}

// ClearCompletedTodos 清除已完成项
func (a *AppService) ClearCompletedTodos() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ClearCompletedTodos(); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
