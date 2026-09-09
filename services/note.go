package services

import (
	"strings"
)

// ---- 笔记树（文件夹 + Markdown 文档）----

const quickNoteKeyword = "__quicknote__"

// GetNote 读取快捷笔记内容（find-or-create 固定关键词笔记）
func (a *AppService) GetNote() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	n, err := a.DB.GetOrCreateNote(quickNoteKeyword)
	if err != nil {
		return Fail(err)
	}
	return Ok(n)
}

// SaveNote 保存快捷笔记内容（整段防抖保存，upsert 固定关键词笔记）
func (a *AppService) SaveNote(content string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	n, err := a.DB.GetOrCreateNote(quickNoteKeyword)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.UpdateNote(n.ID, content); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ListNotesTree 返回全部笔记节点（文件夹 + 文档），前端拼树。
func (a *AppService) ListNotesTree() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListNotesTree()
	return wrap(list, err)
}

// SearchNotesTree 按名称/内容/标签搜索笔记节点。
func (a *AppService) SearchNotesTree(q string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if strings.TrimSpace(q) == "" {
		list, _ := a.DB.ListNotesTree()
		return Ok(list)
	}
	list, err := a.DB.SearchNotes(q)
	return wrap(list, err)
}

// CreateNoteFolder 新建文件夹。
func (a *AppService) CreateNoteFolder(parentId, name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	node, err := a.DB.CreateNoteFolder(parentId, name)
	return wrap(node, err)
}

// CreateNoteDoc 新建笔记文档。parentId 可为空（根）。format: markdown | text。
func (a *AppService) CreateNoteDoc(parentId, name, content, format string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	node, err := a.DB.CreateNoteDoc(parentId, name, content, format)
	return wrap(node, err)
}

// RenameNoteNode 重命名文件夹/文档。
func (a *AppService) RenameNoteNode(id, name string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.RenameNoteNode(id, name); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// UpdateNoteDoc 更新文档内容与标签。tags 为 JSON 数组字符串（留空则清空）。
func (a *AppService) UpdateNoteDoc(id, content, tags string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.UpdateNoteDoc(id, content, tags); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// SetNoteDocFormat 设置笔记渲染格式（markdown | text）。
func (a *AppService) SetNoteDocFormat(id, format string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetNoteFormat(id, format); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// MoveNoteNode 移动节点到新父目录（拖拽）。
func (a *AppService) MoveNoteNode(id, newParentId string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.MoveNoteNode(id, newParentId); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// DeleteNoteNode 递归删除节点及其子树。
func (a *AppService) DeleteNoteNode(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteNoteNode(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
