package services

// ===== 命令面板 =====

// SearchAll 跨全部工作空间搜索项目（使用 FTS5）
func (a *AppService) SearchAll(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.SearchAllItems(query)
	return wrap(items, err)
}

// GetMostUsedItems 返回最常使用的项目（命令面板「最近使用」）
func (a *AppService) GetMostUsedItems(limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.GetMostUsedItems(limit)
	return wrap(items, err)
}

// ListAllItems 返回全部项目（命令面板前端拼音/子串匹配的全量池）
func (a *AppService) ListAllItems() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.ListAllItems()
	return wrap(items, err)
}

// SaveUrlAsItem 将剪贴板中的 URL 保存为网页项目（命令面板智能路由用）
func (a *AppService) SaveUrlAsItem(rawURL string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	item, err := a.DB.SaveUrlAsItem(rawURL)
	if err != nil {
		return Fail(err)
	}
	return Ok(item)
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
