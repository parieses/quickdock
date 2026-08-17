package services

import (
	"encoding/json"
	"strings"
)

// ---- 笔记树（由文本片段升级：文件夹 + Markdown 文档）----

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
	if err := a.DB.RenameSnippetNode(id, name); err != nil {
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
	if err := a.DB.MoveSnippetNode(id, newParentId); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// DeleteNoteNode 递归删除节点及其子树。
func (a *AppService) DeleteNoteNode(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteSnippetNode(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// parseTags 解析标签 JSON 数组字符串为 []string（供前端/测试用，前端可直接解析）
func parseTags(jsonStr string) []string {
	var out []string
	if jsonStr == "" {
		return out
	}
	_ = json.Unmarshal([]byte(jsonStr), &out)
	return out
}

// extractNoteTags 从笔记中扫描 #标签 标记（供"快速加标签"用，可选）。
func (a *AppService) extractNoteTags(content string) []string {
	var tags []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(content) {
		if strings.HasPrefix(field, "#") && len(field) > 1 {
			tag := strings.Trim(field, "#,.;:!?()[]")
			if tag != "" && !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	return tags
}
