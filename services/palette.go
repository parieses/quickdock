package services

// ===== 命令面板 =====

// SearchAll 跨全部工作空间搜索项目（使用 FTS5）
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

// GetMostUsedItems 返回最常使用的项目（命令面板「最近使用」）
func (a *AppService) GetMostUsedItems(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.GetMostUsedItems(limit)
	if err != nil {
		return dberr(err)
	}
	return Ok(items)
}

// GetLastCopiedText 返回最近一次复制的文本
func (a *AppService) GetLastCopiedText() *ApiResult {
	return Ok(getLastClipboardText())
}

// HidePaletteWindow 隐藏命令面板窗口
func (a *AppService) HidePaletteWindow() {
	if a.PaletteMode != nil {
		a.PaletteMode.Store(false)
	}
	if fn := a.GetPaletteWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}
