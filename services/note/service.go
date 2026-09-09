// Package note 笔记树门面服务。
package note

import (
	"strings"

	"quickdock/internal/logger"
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
	if s.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
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
