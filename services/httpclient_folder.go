package services

import (
	"strings"

	"quickdock/internal/db"
)

// ---- 目录（项目下的功能模块，支持多级嵌套） ----

// ListHttpFolders 返回某项目下全部目录。
func (a *AppService) ListHttpFolders(projectID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListHttpFolders(projectID)
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// CreateHttpFolder 新建目录。
func (a *AppService) CreateHttpFolder(input HttpFolderInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	rec := &db.HttpFolder{
		ID:        input.ID,
		ProjectID: input.ProjectID,
		ParentID:  input.ParentID,
		Name:      strings.TrimSpace(input.Name),
		Sort:      input.Sort,
	}
	if rec.Name == "" {
		rec.Name = "目录"
	}
	if err := a.DB.CreateHttpFolder(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// UpdateHttpFolder 更新目录（仅名称/父级/排序）。
func (a *AppService) UpdateHttpFolder(id string, input HttpFolderInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		id = input.ID
	}
	if id == "" {
		return FailMsg("缺少目录 ID")
	}
	// 防环：不允许把目录挂到自己的子孙下（前端已限制，此处再兜底）
	if input.ParentID != "" && input.ParentID == id {
		return FailMsg("目录不能移动到自身下")
	}
	rec := &db.HttpFolder{ID: id, ParentID: input.ParentID, Name: strings.TrimSpace(input.Name), Sort: input.Sort}
	if rec.Name == "" {
		rec.Name = "目录"
	}
	if err := a.DB.UpdateHttpFolder(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// DeleteHttpFolder 删除目录（级联删除子目录与请求）。
func (a *AppService) DeleteHttpFolder(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteHttpFolder(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ReorderHttpFolders 重排/移动目录（拖拽排序与跨目录、跨项目移动）。
// 跨项目移动时递归修正整棵子树的 project_id，避免孤儿数据。
func (a *AppService) ReorderHttpFolders(projectID, parentID string, ids []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderHttpFolders(projectID, parentID, ids); err != nil {
		return Fail(err)
	}
	for _, id := range ids {
		f, err := a.DB.GetHttpFolder(id)
		if err != nil {
			return Fail(err)
		}
		if f != nil && f.ProjectID != projectID {
			if err := a.DB.UpdateFolderSubtreeProject(id, projectID); err != nil {
				return Fail(err)
			}
		}
	}
	return Ok(nil)
}

// ReorderApiRequests 重排/移动请求（拖拽排序与跨目录、跨项目移动）。
func (a *AppService) ReorderApiRequests(projectID, folderID string, ids []string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ReorderApiRequests(projectID, folderID, ids); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
