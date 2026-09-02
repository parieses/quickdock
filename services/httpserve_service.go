package services

// HTTPServeList 返回全部已配置的目录服务。
func (a *AppService) HTTPServeList() *ApiResult {
	return Ok(httpServe.List())
}

// HTTPServeCreate 新增一个目录服务（name/dir/port）。
func (a *AppService) HTTPServeCreate(name, dir string, port int) *ApiResult {
	s, err := httpServe.Create(name, dir, port)
	if err != nil {
		return Fail(err)
	}
	return Ok(s)
}

// HTTPServeStart 启动某目录服务（在指定端口提供静态文件）。
func (a *AppService) HTTPServeStart(id string) *ApiResult {
	if err := httpServe.Start(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// HTTPServeStop 停止某目录服务。
func (a *AppService) HTTPServeStop(id string) *ApiResult {
	if err := httpServe.Stop(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// HTTPServeDelete 删除某目录服务（先停止）。
func (a *AppService) HTTPServeDelete(id string) *ApiResult {
	if err := httpServe.Delete(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// HTTPServeStatus 查询某目录服务是否运行中。
func (a *AppService) HTTPServeStatus(id string) *ApiResult {
	return Ok(map[string]any{"running": httpServe.IsRunning(id)})
}
