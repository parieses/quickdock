package services

// ===== 开机自启 =====

func (a *AppService) GetAutoStart() *ApiResult {
	if a.app == nil {
		return Ok(false)
	}
	enabled, err := a.app.Autostart.IsEnabled()
	if err != nil {
		return Ok(false)
	}
	return Ok(enabled)
}

func (a *AppService) SetAutoStart(enabled bool) *ApiResult {
	if a.app == nil {
		return FailMsg("应用未初始化")
	}
	var err error
	if enabled {
		err = a.app.Autostart.Enable()
	} else {
		err = a.app.Autostart.Disable()
	}
	if err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
