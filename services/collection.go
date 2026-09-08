package services

import "quickdock/internal/db"

// ===== 集合 =====

func (a *AppService) ListCollections(sceneID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListCollections(sceneID)
	return wrap(data, err)
}

func (a *AppService) CreateCollection(workspaceID, sceneID, name, collType, openStrategy string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateCollection(workspaceID, sceneID, name, collType, openStrategy)
	return wrap(data, err)
}

func (a *AppService) UpdateCollection(id string, updates map[string]interface{}) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateCollection(id, updates); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) DeleteCollection(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteCollection(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) ReorderCollections(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.Reorder("collections", orderedIDs); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) OpenItem(item db.CollectionItem) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.OpenItem(&item); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) OpenAllInCollection(collectionID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.OpenAllInCollection(collectionID); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
