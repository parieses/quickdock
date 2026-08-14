package services

import (
	"encoding/json"
	"fmt"
	"strings"

	"quickdock/internal/db"

	"github.com/google/uuid"
)

// ---- HTTP 请求历史 ----

// httpHistoryMax list 默认返回条数
const httpHistoryLimit = 100

// RecordHttpRequestHistory 把一次请求+响应记录到历史（由 SendApiRequest 内部调用）。
func (a *AppService) RecordHttpRequestHistory(input *ApiRequestInput, resp *ApiResponse) {
	if a.DB == nil {
		return
	}
	h := &db.HttpRequestHistory{
		ProjectID:  input.ProjectID,
		Name:       input.Name,
		Method:     strings.ToUpper(strings.TrimSpace(input.Method)),
		URL:        input.URL,
		Headers:    input.Headers,
		Body:       input.Body,
		BodyType:   input.BodyType,
		AuthType:   input.AuthType,
		AuthToken:  input.AuthToken,
		AuthUser:   input.AuthUser,
		AuthPass:   input.AuthPass,
		StatusCode: resp.Status,
		OK:         resp.OK,
		DurationMs: resp.DurationMs,
		Size:       resp.Size,
	}
	_, _ = a.DB.RecordHttpHistory(h)
}

// ListHttpHistory 返回某项目（projectID 为空=全部）最近 N 条请求历史。
func (a *AppService) ListHttpHistory(projectID string, limit int) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListHttpHistory(projectID, limit)
	return wrap(list, err)
}

// DeleteHttpHistory 删除一条历史。
func (a *AppService) DeleteHttpHistory(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteHttpHistory(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ClearHttpHistory 清空某项目（projectID 为空=全部）的历史。
func (a *AppService) ClearHttpHistory(projectID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ClearHttpHistory(projectID); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// GetHttpHistoryAsApiRequest 把一条历史还原成 ApiRequestInput（供"重放/载入表单"）。
// 返回 (input, error)；未找到返回 (nil, fmt.Errorf())。用 string 避免 ApiResult 双层封装。
func (a *AppService) GetHttpHistoryAsApiRequest(id string) (*ApiRequestInput, string) {
	if a.DB == nil {
		return nil, "database not initialized"
	}
	h, err := a.DB.GetHttpHistory(id)
	if err != nil {
		return nil, err.Error()
	}
	if h == nil {
		return nil, "history not found"
	}
	input := &ApiRequestInput{
		ProjectID:  h.ProjectID,
		Name:       h.Name,
		Method:     h.Method,
		URL:        h.URL,
		Headers:    h.Headers,
		Body:       h.Body,
		BodyType:   h.BodyType,
		AuthType:   h.AuthType,
		AuthToken:  h.AuthToken,
		AuthUser:   h.AuthUser,
		AuthPass:   h.AuthPass,
	}
	return input, ""
}

// ---- curl 导出 ----

// BuildCurlCommand 把请求转成 curl 命令字符串（已应用环境变量替换、认证、header 与 body）。
// 供前端一键复制。
func (a *AppService) BuildCurlCommand(input ApiRequestInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	// 复用现有环境变量/项目头合并逻辑
	cp := input
	if err := a.applyProjectAndEnv(&cp); err != nil {
		return Fail(err)
	}
	cmd := buildCurlString(ApiRequestInput{
		Method:    cp.Method,
		URL:       cp.URL,
		Headers:   cp.Headers,
		Body:      cp.Body,
		BodyType:  cp.BodyType,
		AuthType:  cp.AuthType,
		AuthToken: cp.AuthToken,
		AuthUser:  cp.AuthUser,
		AuthPass:  cp.AuthPass,
	})
	return Ok(map[string]interface{}{"command": cmd})
}

// buildCurlString 从请求结构生成 curl 命令。
func buildCurlString(input ApiRequestInput) string {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = "GET"
	}
	var b strings.Builder
	b.WriteString("curl -X ")
	b.WriteString(method)
	b.WriteString(" ")
	b.WriteString(shellQuote(input.URL))

	// Headers
	if input.Headers != "" {
		var hdrs map[string]string
		if json.Unmarshal([]byte(input.Headers), &hdrs) == nil {
			for k, v := range hdrs {
				if strings.EqualFold(k, "Host") {
					continue
				}
				b.WriteString(" -H ")
				b.WriteString(shellQuote(k + ": " + v))
			}
		}
	}
	// Auth
	switch strings.ToLower(input.AuthType) {
	case "bearer":
		if input.AuthToken != "" {
			b.WriteString(" -H ")
			b.WriteString(shellQuote("Authorization: Bearer " + input.AuthToken))
		}
	case "basic":
		if input.AuthUser != "" || input.AuthPass != "" {
			b.WriteString(" -u ")
			b.WriteString(shellQuote(input.AuthUser + ":" + input.AuthPass))
		}
	}
	// Body
	if input.Body != "" && method != "GET" && method != "HEAD" {
		b.WriteString(" --data ")
		b.WriteString(shellQuote(input.Body))
	}
	return b.String()
}

// shellQuote 对参数做简单单引号包裹（含引号/空格安全）。
// 注意：Windows cmd 环境下更偏好双引号，这里用单引号靠近 POSIX；前端复制到
// Git Bash / WSL / Linux 均可。若需 cmd 双击粘贴，可在前端提示换用双引号。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ---- Postman 导入 ----

// Postman collection v2.1 解析结果
type postmanV21Request struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	URL     json.RawMessage   `json:"url"`
	Header  json.RawMessage   `json:"header"`
	Body    json.RawMessage   `json:"body"`
	Auth    json.RawMessage   `json:"auth"`
	Raw     string            `json:"raw"` // request.raw 简化形态
	VarName string            `json:"-"`
}

// ImportPostman 导入 Postman Collection v2.1 JSON：在当前项目下创建请求。
// 返回 (导入条数, 失败原因列表)。
func (a *AppService) ImportPostman(jsonStr string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	// 解析 collection
	var collection struct {
		Info struct {
			Name string `json:"name"`
		} `json:"info"`
		Item json.RawMessage `json:"item"`
		Variable json.RawMessage `json:"variable"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &collection); err != nil {
		return Fail(fmt.Errorf("不是合法的 Postman JSON: %v", err))
	}

	// 解析全局变量（可选）：先建一个默认项目
	projectName := strings.TrimSpace(collection.Info.Name)
	if projectName == "" {
		projectName = "Postman 导入"
	}
	proj, err := a.ensureHttpProject(projectName)
	if err != nil {
		return Fail(err)
	}

	count := 0
	var errs []string
	var flatten func(json.RawMessage) []postmanV21Request
	flatten = func(items json.RawMessage) []postmanV21Request {
		var out []postmanV21Request
		var arr []struct {
			Name   string          `json:"name"`
			Item   json.RawMessage `json:"item"`
			Method string          `json:"method"`
			URL    json.RawMessage `json:"url"`
			Header json.RawMessage `json:"header"`
			Body   json.RawMessage `json:"body"`
			Auth   json.RawMessage `json:"auth"`
		}
		_ = json.Unmarshal(items, &arr)
		for _, it := range arr {
			// 文件夹（含 item）递归
			if len(it.Item) > 0 {
				sub := flatten(it.Item)
				out = append(out, sub...)
				continue
			}
			req := postmanV21Request{
				Name:   it.Name,
				Method: it.Method,
				URL:    it.URL,
				Header: it.Header,
				Body:   it.Body,
				Auth:   it.Auth,
			}
			out = append(out, req)
		}
		return out
	}

	var reqs []postmanV21Request
	if len(collection.Item) > 0 {
		reqs = append(reqs, flatten(collection.Item)...)
	}

	for _, req := range reqs {
		var headers, body string
		var bodyType string
		var urlStr string

		// URL：可能是字符串或对象
		if len(req.URL) > 0 {
			var urlStrPlain string
			if json.Unmarshal(req.URL, &urlStrPlain) == nil && urlStrPlain != "" {
				urlStr = urlStrPlain
			} else {
				var uo struct {
					Raw string `json:"raw"`
				}
				if json.Unmarshal(req.URL, &uo) == nil {
					urlStr = uo.Raw
				}
			}
		}
		// headers
		var headerList []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if len(req.Header) > 0 {
			_ = json.Unmarshal(req.Header, &headerList)
			m := map[string]string{}
			for _, hd := range headerList {
				if hd.Key != "" {
					m[hd.Key] = hd.Value
				}
			}
			if mb, e := json.Marshal(m); e == nil {
				headers = string(mb)
			}
		}
		// body
		if len(req.Body) > 0 {
			var bodyObj struct {
				Mode string `json:"mode"`
				Raw  string `json:"raw"`
			}
			if json.Unmarshal(req.Body, &bodyObj) == nil {
				body = bodyObj.Raw
				bodyType = bodyObj.Mode
				if bodyType == "formdata" || bodyType == "urlencoded" {
					bodyType = "form"
				} else if bodyType == "file" {
					bodyType = "json"
				}
			}
		}
		if bodyType == "" {
			bodyType = "json"
		}
		if urlStr == "" {
			errs = append(errs, fmt.Sprintf("跳过无 URL 的请求: %s", req.Name))
			continue
		}
		name := req.Name
		if name == "" {
			name = urlStr
		}
		rec := &db.ApiRequest{
			ID:       newHTTPID(),
			ProjectID: proj.ID,
			Name:     name,
			Method:   req.Method,
			URL:      urlStr,
			Headers:  headers,
			Body:     body,
			BodyType: bodyType,
			Sort:     count,
		}
		if err := a.DB.CreateApiRequest(rec); err != nil {
			errs = append(errs, fmt.Sprintf("导入失败 %s: %v", name, err))
			continue
		}
		count++
	}
	result := map[string]interface{}{"imported": count, "projectId": proj.ID, "errors": errs}
	return Ok(result)
}

// ensureHttpProject 按名字查找项目，无则创建。
func (a *AppService) ensureHttpProject(name string) (*db.HttpProject, error) {
	if strings.TrimSpace(name) == "" {
		name = "未命名项目"
	}
	projects, err := a.DB.ListHttpProjects()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if strings.TrimSpace(projects[i].Name) == strings.TrimSpace(name) {
			p := projects[i]
			return &p, nil
		}
	}
	rec := &db.HttpProject{Name: name, Headers: "{}", Sort: len(projects)}
	if err := a.DB.CreateHttpProject(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// newHTTPID 生成 HTTP 相关实体的本地 id。
func newHTTPID() string {
	return uuid.New().String()
}
