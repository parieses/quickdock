package services

// ===== 场景 =====

func (a *AppService) ListScenes(workspaceID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListScenes(workspaceID)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) CreateScene(workspaceID, name, sceneType string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateScene(workspaceID, name, sceneType)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) UpdateScene(id string, updates map[string]interface{}) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateScene(id, updates); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) DeleteScene(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteScene(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) ReorderScenes(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderScenes(orderedIDs); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}
