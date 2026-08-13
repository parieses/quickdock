package services

import (
	"strings"

	"quickdock/internal/db"
)

// ---- 环境（项目下变量集合） ----

// ListHttpEnvironments 返回某项目下全部环境。
func (a *AppService) ListHttpEnvironments(projectID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListHttpEnvironments(projectID)
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// CreateHttpEnvironment 新建环境。
func (a *AppService) CreateHttpEnvironment(input HttpEnvInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	rec := &db.HttpEnvironment{ID: input.ID, ProjectID: input.ProjectID, Name: strings.TrimSpace(input.Name), Variables: input.Variables, Sort: input.Sort}
	if rec.Name == "" {
		rec.Name = "环境"
	}
	if rec.Variables == "" {
		rec.Variables = "[]"
	}
	if err := a.DB.CreateHttpEnvironment(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// UpdateHttpEnvironment 更新环境。
func (a *AppService) UpdateHttpEnvironment(id string, input HttpEnvInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		id = input.ID
	}
	if id == "" {
		return FailMsg("缺少环境 ID")
	}
	rec := &db.HttpEnvironment{ID: id, ProjectID: input.ProjectID, Name: strings.TrimSpace(input.Name), Variables: input.Variables, Sort: input.Sort}
	if rec.Variables == "" {
		rec.Variables = "[]"
	}
	if err := a.DB.UpdateHttpEnvironment(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// DeleteHttpEnvironment 删除环境。
func (a *AppService) DeleteHttpEnvironment(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteHttpEnvironment(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
