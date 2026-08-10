package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"quickdock/internal/db"
)

// ApiRequestInput 前端传入的请求（新建/更新/发送共用）。
// Headers 为 JSON map 字符串；Auth 信息仅本地保存，发送时按类型注入。
type ApiRequestInput struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProjectID    string `json:"projectId"`
	FolderID     string `json:"folderId"`
	EnvironmentID string `json:"environmentId"`
	Method       string `json:"method"`
	URL          string `json:"url"`
	Headers      string `json:"headers"`
	Body         string `json:"body"`
	BodyType     string `json:"bodyType"`
	AuthType     string `json:"authType"`
	AuthToken    string `json:"authToken"`
	AuthUser     string `json:"authUser"`
	AuthPass     string `json:"authPass"`
	Sort         int    `json:"sort"`
}

// HttpProjectInput / HttpEnvInput 项目与环境（Postman 式分组）。
type HttpProjectInput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Headers string `json:"headers"`
	Sort    int    `json:"sort"`
}
type HttpEnvInput struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Variables string `json:"variables"`
	Sort      int    `json:"sort"`
}
type HttpFolderInput struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	ParentID  string `json:"parentId"`
	Name      string `json:"name"`
	Sort      int    `json:"sort"`
}

// HttpDocInput 目录下的 Markdown 文档（新建/更新共用）。
type HttpDocInput struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	FolderID  string `json:"folderId"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Sort      int    `json:"sort"`
}

// ApiResponse 发送结果（不落库）。
type ApiResponse struct {
	Status     int               `json:"status"`
	OK         bool              `json:"ok"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	DurationMs int64             `json:"durationMs"`
	Size       int               `json:"size"`
	Truncated  bool              `json:"truncated"`
}

const (
	httpClientMaxBody   = 16 << 20 // 响应体上限 16 MiB
	httpClientTimeout   = 60 * time.Second
	httpClientMaxRedirs = 10
)

// userHTTPClient UI 用 HTTP 客户端：独立的超时与重定向上限。
var userHTTPClient = &http.Client{
	Timeout: httpClientTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= httpClientMaxRedirs {
			return fmt.Errorf("重定向次数超过 %d 次", httpClientMaxRedirs)
		}
		return nil
	},
}

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

// SendApiRequest 执行一次请求并返回响应（不保存）。
// 发送前应用项目共享头 + 环境变量（{{var}} 替换），请求头覆盖项目头同名 key。
func (a *AppService) SendApiRequest(input ApiRequestInput) *ApiResult {
	if err := a.applyProjectAndEnv(&input); err != nil {
		return Fail(err)
	}
	resp, err := doUserHTTP(input)
	if err != nil {
		return Fail(err)
	}
	return Ok(resp)
}

// applyProjectAndEnv 把项目共享头与激活环境的变量作用到请求输入上（原地修改）。
func (a *AppService) applyProjectAndEnv(input *ApiRequestInput) error {
	// 1. 收集环境变量（仅启用项）
	vars := map[string]string{}
	if input.EnvironmentID != "" {
		if env, err := a.DB.GetHttpEnvironment(input.EnvironmentID); err == nil && env != nil {
			var list []struct {
				Key     string `json:"key"`
				Value   string `json:"value"`
				Enabled bool   `json:"enabled"`
			}
			if e2 := json.Unmarshal([]byte(env.Variables), &list); e2 == nil {
				for _, v := range list {
					if v.Enabled && v.Key != "" {
						vars[v.Key] = v.Value
					}
				}
			}
		}
	}

	// 2. 替换 URL / Body / 认证中的 {{var}}
	input.URL = substituteVars(input.URL, vars)
	input.Body = substituteVars(input.Body, vars)
	input.AuthToken = substituteVars(input.AuthToken, vars)
	input.AuthUser = substituteVars(input.AuthUser, vars)
	input.AuthPass = substituteVars(input.AuthPass, vars)

	// 3. 合并头：项目共享头为底，请求头覆盖同名 key
	reqHeaders := map[string]string{}
	if input.Headers != "" {
		if err := json.Unmarshal([]byte(input.Headers), &reqHeaders); err != nil {
			reqHeaders = map[string]string{}
		}
	}
	merged := map[string]string{}
	if input.ProjectID != "" {
		if proj, err := a.DB.GetHttpProject(input.ProjectID); err == nil && proj != nil && proj.Headers != "" {
			var projHeaders map[string]string
			if e2 := json.Unmarshal([]byte(proj.Headers), &projHeaders); e2 == nil {
				for k, v := range projHeaders {
					merged[k] = substituteVars(v, vars)
				}
			}
		}
	}
	for k, v := range reqHeaders {
		merged[k] = substituteVars(v, vars)
	}
	b, _ := json.Marshal(merged)
	input.Headers = string(b)
	return nil
}

// substituteVars 将文本中的 {{key}} 替换为 vars[key]（大小写敏感，未匹配保留原样）。
func substituteVars(s string, vars map[string]string) string {
	if s == "" || len(vars) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.Index(s[i+2:], "}}")
			if end >= 0 {
				key := s[i+2 : i+2+end]
				if val, ok := vars[key]; ok {
					b.WriteString(val)
					i = i + 2 + end + 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
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

// doUserHTTP 执行一次 HTTP 请求：仅放行 http/https，应用 auth 与自定义 header，
// 按 body_type 设置 Content-Type。安全策略与插件 http host API 一致（拦截 file:// 等）。
func doUserHTTP(input ApiRequestInput) (*ApiResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	rawURL := strings.TrimSpace(input.URL)
	if rawURL == "" {
		return nil, fmt.Errorf("URL 不能为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("URL 非法: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https 协议，收到: %s", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("URL 缺少主机名")
	}

	var bodyReader io.Reader
	if input.Body != "" && method != http.MethodGet && method != http.MethodHead {
		bodyReader = bytes.NewReader([]byte(input.Body))
	}
	req, err := http.NewRequest(method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	// 自定义 header
	if input.Headers != "" {
		var hdrs map[string]string
		if err := json.Unmarshal([]byte(input.Headers), &hdrs); err == nil {
			for k, v := range hdrs {
				if strings.EqualFold(k, "Host") {
					continue
				}
				req.Header.Set(k, v)
			}
		}
	}

	// 认证
	switch strings.ToLower(input.AuthType) {
	case "bearer":
		if input.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+input.AuthToken)
		}
	case "basic":
		if input.AuthUser != "" || input.AuthPass != "" {
			req.SetBasicAuth(input.AuthUser, input.AuthPass)
		}
	}

	// Content-Type（按 body 类型），仅当未显式设置
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		switch input.BodyType {
		case "form":
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		case "text":
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		case "xml":
			req.Header.Set("Content-Type", "application/xml; charset=utf-8")
		default: // json
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "QuickDock-HttpClient")
	}

	start := time.Now()
	resp, err := userHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(httpClientMaxBody)+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	truncated := false
	if len(data) > httpClientMaxBody {
		data = data[:httpClientMaxBody]
		truncated = true
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return &ApiResponse{
		Status:     resp.StatusCode,
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		Headers:    respHeaders,
		Body:       string(data),
		DurationMs: elapsed.Milliseconds(),
		Size:       len(data),
		Truncated:  truncated,
	}, nil
}
