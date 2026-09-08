package services

// ===== 快照备份 =====

func (a *AppService) CreateSnapshot(label, note string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateFullSnapshot(label, note)
	return wrap(data, err)
}

func (a *AppService) ListSnapshots() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListSnapshots()
	return wrap(data, err)
}

func (a *AppService) GetSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.GetSnapshot(id)
	return wrap(data, err)
}

func (a *AppService) DeleteSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteSnapshot(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

func (a *AppService) RestoreSnapshot(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.RestoreSnapshot(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
