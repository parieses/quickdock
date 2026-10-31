package services

import (
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// ===== 命令面板 =====

// SearchAll 跨全部工作空间搜索项目（使用 FTS5）
//
// 命令面板已改走 SearchPalette（一次 IPC 拿三类结果）；本方法保留是因为
// 工作空间页（stores/workspace.ts）与 MCP 的 item_search 仍在单独用它。
func (a *AppService) SearchAll(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	items, err := a.DB.SearchAllItems(query)
	return wrap(items, err)
}

// PaletteSearchResult 命令面板后端检索的合并载荷。
//
// 三个字段一律给空切片而不是 nil：前端直接把结果塞进 ref，序列化成 null
// 会让模板在 `.length` 上炸；这里兜住比让每个调用点各写一次 `|| []` 省心。
type PaletteSearchResult struct {
	Items []db.CollectionItem `json:"items"`
	Notes []db.Note           `json:"notes"`
	Todos []db.Todo           `json:"todos"`
}

// SearchPalette 命令面板一次性检索项目 / 笔记 / 待办。
//
// 合并三路只为省掉「面板每敲一个字发 3 次 IPC、各查一遍库」——三条查询仍
// 各自独立执行：命令面板把后端检索当成本地匹配的补充，缺一段只是少几条
// 补充结果，远好过一段 SQL 出错把另外两段一起判废。失败的那段留空并记警告，
// 便于从日志判断「某类怎么总搜不到」。
//
// 空查询返回三个空切片：笔记页的 SearchNotesTree("") 语义是「列全量」，
// 那是它自己的用法，面板不该因为清空输入就拉一整棵树回来。
func (a *AppService) SearchPalette(query string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	out := PaletteSearchResult{
		Items: []db.CollectionItem{},
		Notes: []db.Note{},
		Todos: []db.Todo{},
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return Ok(out)
	}
	if v, err := a.DB.SearchAllItems(q); err != nil {
		logger.W("[palette-search] 项目检索失败: %v", err)
	} else if v != nil {
		out.Items = v
	}
	if v, err := a.DB.SearchNotes(q); err != nil {
		logger.W("[palette-search] 笔记检索失败: %v", err)
	} else if v != nil {
		out.Notes = v
	}
	// 20 条与 SearchTodos 原有上限保持一致：面板每个分组只展示前几条，
	// 拉多了纯属浪费序列化。
	if v, err := a.DB.SearchTodos(q, 20); err != nil {
		logger.W("[palette-search] 待办检索失败: %v", err)
	} else if v != nil {
		out.Todos = v
	}
	return Ok(out)
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

// navigablePages 命令面板「快速导航」允许跳转的页面 id。
//
// 必须白名单：这个方法会被前端任意字符串调用，直接广播等于让面板可以
// 把主窗口切到不存在的页面（内容区会空白）。与前端 pages/registry 的
// 「带 component 的内容页」一一对应 —— ssh / dsh 是独立窗口入口，
// 不在其中（面板不负责唤起它们，它们有自己的入口）。
var navigablePages = map[string]bool{
	"workspace":    true,
	"notes":        true,
	"todo":         true,
	"schedule":     true,
	"monitor":      true,
	"clipboard":    true,
	"environments": true,
	"ai":           true,
	"plugins":      true,
}

// NavigateMainWindow 命令面板选中「快速导航」项：交给主窗口切到对应页面。
// 与笔记/待办同一条链路（handOffToMainWindow），只是负载换成页面 id。
func (a *AppService) NavigateMainWindow(page string) *ApiResult {
	page = strings.TrimSpace(page)
	if !navigablePages[page] {
		return FailMsg("未知的导航页面: " + page)
	}
	if a.app == nil {
		return FailMsg("应用未就绪")
	}
	a.handOffToMainWindow("nav:goto", page)
	return Ok(nil)
}
