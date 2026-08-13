package services

import (
	"strings"

	"quickdock/internal/db"
)

// ---- 文档（目录下的 Markdown 笔记，轻量知识库） ----

// ListHttpDocs 返回某项目下全部文档。
func (a *AppService) ListHttpDocs(projectID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListHttpDocs(projectID)
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// CreateHttpDoc 新建文档。
func (a *AppService) CreateHttpDoc(input HttpDocInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	rec := &db.HttpDoc{
		ID:        input.ID,
		ProjectID: input.ProjectID,
		FolderID:  input.FolderID,
		Name:      strings.TrimSpace(input.Name),
		Content:   input.Content,
		Sort:      input.Sort,
	}
	if rec.Name == "" {
		rec.Name = "未命名文档"
	}
	if err := a.DB.CreateHttpDoc(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// UpdateHttpDoc 更新文档（仅名称/内容）。
func (a *AppService) UpdateHttpDoc(id string, input HttpDocInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		id = input.ID
	}
	if id == "" {
		return FailMsg("缺少文档 ID")
	}
	cur, err := a.DB.GetHttpDoc(id)
	if err != nil {
		return Fail(err)
	}
	if cur == nil {
		return FailMsg("文档不存在")
	}
	cur.Name = strings.TrimSpace(input.Name)
	if cur.Name == "" {
		cur.Name = "未命名文档"
	}
	cur.Content = input.Content
	cur.Sort = input.Sort
	if err := a.DB.UpdateHttpDoc(cur); err != nil {
		return Fail(err)
	}
	return Ok(cur)
}

// DeleteHttpDoc 删除文档。
func (a *AppService) DeleteHttpDoc(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteHttpDoc(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
