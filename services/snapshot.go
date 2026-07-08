package services

// ===== 快照备份 =====

func (a *AppService) CreateSnapshot(label, note string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateFullSnapshot(label, note)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) ListSnapshots() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListSnapshots()
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) GetSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.GetSnapshot(id)
	if err != nil {
		return dberr(err)
	}
	return Ok(data)
}

func (a *AppService) DeleteSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteSnapshot(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}

func (a *AppService) RestoreSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.RestoreSnapshot(id); err != nil {
		return dberr(err)
	}
	return Ok(nil)
}
