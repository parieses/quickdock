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
)

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

// SendApiRequest 执行一次请求并返回响应（不保存）。
// 发送前应用项目共享头 + 环境变量（{{var}} 替换），请求头覆盖项目头同名 key。
func (a *AppService) SendApiRequest(input ApiRequestInput) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
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
