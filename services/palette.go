package services

// ===== 命令面板 =====

// SearchAll 跨全部工作空间搜索项目
func (a *AppService) SearchAll(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.SearchAllItems(query)
	if err != nil {
		return dberr(err)
	}
	return Ok(items)
}

// HidePaletteWindow 隐藏命令面板窗口
func (a *AppService) HidePaletteWindow() {
	if a.PaletteMode != nil {
		a.PaletteMode.Store(false)
	}
	if win := a.PaletteWindow; win != nil {
		win.Hide()
	}
}
