package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"quickdock/internal/db"
)

// ListApiRequests 返回全部保存的请求。
func (a *AppService) ListApiRequests() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListApiRequests()
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// CreateApiRequest 保存新请求，返回带 ID 的记录。
func (a *AppService) CreateApiRequest(input ApiRequestInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := validateApiRequest(&input); err != nil {
		return Fail(err)
	}
	rec := toDbApiRequest(&input)
	if err := a.DB.CreateApiRequest(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// UpdateApiRequest 更新已有请求（按 ID）。
func (a *AppService) UpdateApiRequest(id string, input ApiRequestInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		id = input.ID
	}
	if id == "" {
		return FailMsg("缺少请求 ID")
	}
	input.ID = id
	if err := validateApiRequest(&input); err != nil {
		return Fail(err)
	}
	rec := toDbApiRequest(&input)
	if err := a.DB.UpdateApiRequest(rec); err != nil {
		return Fail(err)
	}
	return Ok(rec)
}

// DeleteApiRequest 删除保存的请求。
func (a *AppService) DeleteApiRequest(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteApiRequest(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// validateApiRequest 校验方法合法性与必填项，并归一化 Method/BodyType。
func validateApiRequest(input *ApiRequestInput) error {
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	if input.Method == "" {
		input.Method = http.MethodGet
	}
	switch input.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodPatch, http.MethodHead, http.MethodOptions:
	default:
		return fmt.Errorf("不支持的 HTTP 方法: %s", input.Method)
	}
	input.URL = strings.TrimSpace(input.URL)
	if input.URL == "" {
		return fmt.Errorf("URL 不能为空")
	}
	if input.BodyType == "" {
		input.BodyType = "json"
	}
	if input.Headers != "" {
		var m map[string]string
		if err := json.Unmarshal([]byte(input.Headers), &m); err != nil {
			return fmt.Errorf("Headers 不是合法 JSON: %w", err)
		}
	}
	return nil
}

func toDbApiRequest(input *ApiRequestInput) *db.ApiRequest {
	return &db.ApiRequest{
		ID:        input.ID,
		Name:      strings.TrimSpace(input.Name),
		ProjectID: input.ProjectID,
		FolderID:  input.FolderID,
		Method:    input.Method,
		URL:       input.URL,
		Headers:   input.Headers,
		Body:      input.Body,
		BodyType:  input.BodyType,
		AuthType:  input.AuthType,
		AuthToken: input.AuthToken,
		AuthUser:  input.AuthUser,
		AuthPass:  input.AuthPass,
		Sort:      input.Sort,
	}
}
