package services

// ===== 打开工具 =====

func (a *AppService) ListTools() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.ListTools()
	return wrap(data, err)
}

func (a *AppService) CreateTool(name, toolType, path, args string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	data, err := a.DB.CreateTool(name, toolType, path, args)
	return wrap(data, err)
}
