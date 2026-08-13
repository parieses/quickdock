package services

import (
	"strings"

	"quickdock/internal/db"
)

// ---- 项目（Postman Collection） ----

// ListHttpProjects 返回全部项目。
func (a *AppService) ListHttpProjects() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListHttpProjects()
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// CreateHttpProject 新建项目。
func (a *AppService) CreateHttpProject(input HttpProjectInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	rec := &db.HttpProject{ID: input.ID, Name: strings.TrimSpace(input.Name), Headers: input.Headers, Sort: input.Sort}
	if rec.Name == "" {
		rec.Name = "未命名项目"
	}
	if rec.Headers == "" {
		rec.Headers = "{}"
	}
	if err := a.DB.CreateHttpProject(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// UpdateHttpProject 更新项目。
func (a *AppService) UpdateHttpProject(id string, input HttpProjectInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		id = input.ID
	}
	if id == "" {
		return FailMsg("缺少项目 ID")
	}
	rec := &db.HttpProject{ID: id, Name: strings.TrimSpace(input.Name), Headers: input.Headers, Sort: input.Sort}
	if rec.Headers == "" {
		rec.Headers = "{}"
	}
	if err := a.DB.UpdateHttpProject(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// DeleteHttpProject 删除项目（其下请求回退未分类，环境级联删除）。
func (a *AppService) DeleteHttpProject(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteHttpProject(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
