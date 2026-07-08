package services

// ===== 工作空间 =====

func (a *AppService) ListWorkspaces() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListWorkspaces()
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) CreateWorkspace(name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateWorkspace(name)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) DeleteWorkspace(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteWorkspace(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) UpdateWorkspace(id, name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateWorkspace(id, name); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) GetWorkspace(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.GetWorkspace(id)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) ReorderWorkspaces(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderWorkspaces(orderedIDs); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}
