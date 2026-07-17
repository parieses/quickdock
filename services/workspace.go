package services

// ===== 工作空间 =====

func (a *AppService) ListWorkspaces() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListWorkspaces()
	return wrap(data, err)
}

func (a *AppService) CreateWorkspace(name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateWorkspace(name)
	return wrap(data, err)
}

func (a *AppService) DeleteWorkspace(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	ws, err := a.DB.GetWorkspace(id)
	if err != nil {
		return Fail(err)
	}
	if ws.Name == "默认工作空间" {
		return FailMsg("默认工作空间不允许删除")
	}
	if err := a.DB.DeleteWorkspace(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) UpdateWorkspace(id, name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateWorkspace(id, name); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) GetWorkspace(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.GetWorkspace(id)
	return wrap(data, err)
}

func (a *AppService) ReorderWorkspaces(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.Reorder("workspaces", orderedIDs); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
