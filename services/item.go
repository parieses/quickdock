package services

// ===== 项目 =====

func (a *AppService) ListItems(collectionID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListItems(collectionID)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) CreateItem(workspaceID, collectionID, name, itemType, value string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateItem(workspaceID, collectionID, name, itemType, value)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) UpdateItem(id string, updates map[string]interface{}) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateItem(id, updates); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) DeleteItem(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteItem(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) ReorderItems(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderItems(orderedIDs); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}
