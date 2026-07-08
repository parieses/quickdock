package services

import "quickdock/internal/db"

// ===== 集合 =====

func (a *AppService) ListCollections(sceneID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListCollections(sceneID)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) CreateCollection(workspaceID, sceneID, name, collType, openStrategy string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateCollection(workspaceID, sceneID, name, collType, openStrategy)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) UpdateCollection(id string, updates map[string]interface{}) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateCollection(id, updates); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) DeleteCollection(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteCollection(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) ReorderCollections(orderedIDs []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderCollections(orderedIDs); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) OpenItem(item db.CollectionItem) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.OpenItem(&item); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) OpenAllInCollection(collectionID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.OpenAllInCollection(collectionID); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}
