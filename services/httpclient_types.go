package services

// ApiRequestInput 前端传入的请求（新建/更新/发送共用）。
// Headers 为 JSON map 字符串；Auth 信息仅本地保存，发送时按类型注入。
type ApiRequestInput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ProjectID     string `json:"projectId"`
	FolderID      string `json:"folderId"`
	EnvironmentID string `json:"environmentId"`
	Method        string `json:"method"`
	URL           string `json:"url"`
	Headers       string `json:"headers"`
	Body          string `json:"body"`
	BodyType      string `json:"bodyType"`
	AuthType      string `json:"authType"`
	AuthToken     string `json:"authToken"`
	AuthUser      string `json:"authUser"`
	AuthPass      string `json:"authPass"`
	Sort          int    `json:"sort"`
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
