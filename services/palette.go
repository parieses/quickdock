package services

import (
	"strings"
)

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
	a.Flags.Palette.Store(false)
	if fn := a.GetPaletteWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}

// handOffToMainWindow 命令面板把一件事交给主窗口处理：隐藏面板 → 显示并聚焦
// 主窗口 → 广播带负载的事件。
//
// 必须经后端中转：面板浮窗与主窗口是两个 WebView，JS 状态互不可见；此前在面板内
// 直接 navigateTo 会把完整主界面渲染进面板小窗（样式错乱）。
func (a *AppService) handOffToMainWindow(event, payload string) {
	a.HidePaletteWindow()
	if a.MainWindow != nil {
		a.MainWindow.Show()
		a.MainWindow.Focus()
	}
	a.app.Event.Emit(event, payload)
}

// OpenNoteFromPalette 命令面板选中笔记：交给主窗口打开对应文档。
func (a *AppService) OpenNoteFromPalette(noteId string) *ApiResult {
	noteId = strings.TrimSpace(noteId)
	if noteId == "" {
		return FailMsg("笔记 ID 为空")
	}
	if a.app == nil {
		return FailMsg("应用未就绪")
	}
	a.handOffToMainWindow("note:open-request", noteId)
	return Ok(nil)
}

// OpenTodoFromPalette 命令面板选中待办：交给主窗口切到待办页并定位该条。
// 与笔记走同一条链路（事件名不同），主窗口不再额外拉一条 duplicated 实现。
func (a *AppService) OpenTodoFromPalette(todoId string) *ApiResult {
	todoId = strings.TrimSpace(todoId)
	if todoId == "" {
		return FailMsg("待办 ID 为空")
	}
	if a.app == nil {
		return FailMsg("应用未就绪")
	}
	a.handOffToMainWindow("todo:open-request", todoId)
	return Ok(nil)
}
