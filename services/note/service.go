// Package note 笔记树门面服务。
package note

import (
	"strings"

	"quickdock/services"
)

// NoteService 笔记树（文件夹 + Markdown 文档）绑定服务。
type NoteService struct {
	App *services.AppService
}

// NewNoteService 创建 NoteService。
func NewNoteService(app *services.AppService) *NoteService {
	return &NoteService{App: app}
}

// quickNoteKeyword 快捷笔记固定关键词。
const quickNoteKeyword = "__quicknote__"

func (s *NoteService) dbOK() *services.ApiResult {
	return services.CheckDB(s.App.DB)
}

// GetNote 读取快捷笔记内容（find-or-create 固定关键词笔记）。
func (s *NoteService) GetNote() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	n, err := s.App.DB.GetOrCreateNote(quickNoteKeyword)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(n)
}

// SaveNote 保存快捷笔记内容（整段防抖保存，upsert 固定关键词笔记）。
func (s *NoteService) SaveNote(content string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	n, err := s.App.DB.GetOrCreateNote(quickNoteKeyword)
	if err != nil {
		return services.Fail(err)
	}
	if err := s.App.DB.UpdateNote(n.ID, content); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// AskAI 快捷笔记「问 AI」：隐藏笔记独立窗口 → 显示并聚焦主窗口 →
// 广播 note:ask-ai 事件（携带笔记文本），由主窗口注入 AI 输入框并切到 AI 页。
// 必须经后端中转：笔记浮窗与主窗口是两个 WebView，JS 状态互不可见；
// 此前在浮窗内直接 navigateTo('ai') 会把完整主界面渲染进小浮窗（样式错乱）。
func (s *NoteService) AskAI(text string) *services.ApiResult {
	content := strings.TrimSpace(text)
	if content == "" {
		return services.FailMsg("内容为空")
	}
	app := s.App.App()
	if app == nil {
		return services.FailMsg("应用未就绪")
	}
	// 隐藏笔记浮窗并复位标志（与 ClipboardService.HideNoteWindow 逻辑一致，保证热键开关正确）
	if fn := s.App.GetNoteWindow; fn != nil {
		if win := fn(); win != nil {
			s.App.Flags.Note.Store(false)
			win.Hide()
		}
	}
	// 显示并聚焦主窗口（与 plugin_host 的唤起模式一致）
	if win := s.App.MainWindow; win != nil {
		win.Show()
		win.Focus()
	}
	app.Event.Emit("note:ask-ai", content)
	return services.Ok(nil)
}

// ListNotesTree 返回全部笔记节点（文件夹 + 文档），前端拼树。
func (s *NoteService) ListNotesTree() *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	list, err := s.App.DB.ListNotesTree()
	return services.Wrap(list, err)
}

// SearchNotesTree 按名称/内容/标签搜索笔记节点。
func (s *NoteService) SearchNotesTree(q string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(q) == "" {
		list, _ := s.App.DB.ListNotesTree()
		return services.Ok(list)
	}
	list, err := s.App.DB.SearchNotes(q)
	return services.Wrap(list, err)
}

// CreateNoteFolder 新建文件夹。
func (s *NoteService) CreateNoteFolder(parentId, name string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	node, err := s.App.DB.CreateNoteFolder(parentId, name)
	return services.Wrap(node, err)
}

// CreateNoteDoc 新建笔记文档。parentId 可为空（根）。format: markdown | text。
func (s *NoteService) CreateNoteDoc(parentId, name, content, format string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	node, err := s.App.DB.CreateNoteDoc(parentId, name, content, format)
	return services.Wrap(node, err)
}

// RenameNoteNode 重命名文件夹/文档。
func (s *NoteService) RenameNoteNode(id, name string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.RenameNoteNode(id, name); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// UpdateNoteDoc 更新文档内容与标签。tags 为 JSON 数组字符串（留空则清空）。
func (s *NoteService) UpdateNoteDoc(id, content, tags string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateNoteDoc(id, content, tags); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// UpdateNoteDocContent 只更新正文、保留原有标签。
// 供 MCP note_update 使用：模型常只回传正文，若走 UpdateNoteDoc 会把标签清空。
func (s *NoteService) UpdateNoteDocContent(id, content string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateNoteContent(id, content); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// UpdateNoteDocTags 只更新标签、保留原有正文（MCP note_update 只传 tags 时使用）。
func (s *NoteService) UpdateNoteDocTags(id, tags string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.UpdateNoteTags(id, tags); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// SetNoteDocFormat 设置笔记渲染格式（markdown | text）。
func (s *NoteService) SetNoteDocFormat(id, format string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.SetNoteFormat(id, format); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// MoveNoteNode 移动节点到新父目录（拖拽）。
func (s *NoteService) MoveNoteNode(id, newParentId string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.MoveNoteNode(id, newParentId); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteNoteNode 递归删除节点及其子树。
func (s *NoteService) DeleteNoteNode(id string) *services.ApiResult {
	if r := s.dbOK(); r != nil {
		return r
	}
	if err := s.App.DB.DeleteNoteNode(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
