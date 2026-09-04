package plugin

import (
	"database/sql"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// ---- 插件清单结构 ----

// PluginManifest 插件清单
type PluginManifest struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	NameI18n       map[string]string `json:"name_i18n,omitempty"` // 多语言名称: {locale: 名称}，如 {"en-US": "HTTP Status Codes"}
	Version        string            `json:"version"`
	Description    string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"` // 多语言描述
	Author         string            `json:"author,omitempty"`
	Icon           string            `json:"icon,omitempty"`
	Category       string            `json:"category,omitempty"`
	Platforms      []string          `json:"platforms,omitempty"` // 支持的平台: windows/darwin/linux
	Backend        BackendConfig     `json:"backend"`
	Frontend       FrontendConfig    `json:"frontend,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	Permissions    Permissions       `json:"permissions,omitempty"`
	Commands       []Command         `json:"commands,omitempty"`
}

// BackendConfig 后端配置
type BackendConfig struct {
	Runtime string   `json:"runtime"` // none | goja | native
	Entry   string   `json:"entry"`
	Args    []string `json:"args,omitempty"`
}

// FrontendConfig 前端配置
type FrontendConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

// Permissions 权限声明
type Permissions struct {
	Network    bool `json:"network,omitempty"`
	Filesystem bool `json:"filesystem,omitempty"`
	Clipboard  bool `json:"clipboard,omitempty"`
}

// Command 插件命令
type Command struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	TitleI18n    map[string]string `json:"title_i18n,omitempty"` // 多语言标题: {locale: 标题}
	Hotkey       string            `json:"hotkey,omitempty"`
	Keywords     []string          `json:"keywords,omitempty"`     // 搜索别名，用于命令面板快速查找
	Aliases      []string          `json:"aliases,omitempty"`      // 中文别名，如 ["计算器", "jsq"]，扩展搜索覆盖
	Prefix       string            `json:"prefix,omitempty"`       // Slash 命令前缀，如 "/translate"，输入 /tr 时只匹配该插件
	MatchPattern string            `json:"matchPattern,omitempty"` // 命令面板正则匹配：命中时自动传入输入文本
	AcceptsInput bool              `json:"acceptsInput,omitempty"` // 是否接收命令面板传入的参数（如端口号/状态码/算式），开启后 Ctrl+K 的文本会带入插件
}

// ---- JSON-RPC 通信结构 ----

// RPCRequest JSON-RPC 2.0 请求
// ID 使用 json.RawMessage 而非 int64：JSON-RPC 规范允许 id 为 string | number | null，
// 部分 JS/第三方插件会以字符串 id 通信，强类型 int64 会导致这类请求/响应被静默丢弃。
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse JSON-RPC 2.0 响应
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return e.Message
}

// ---- 插件运行时实例 ----

// PluginInstance 运行中的插件实例
type PluginInstance struct {
	Manifest PluginManifest
	Cmd      *exec.Cmd
	Stdin    io.WriteCloser
	Stdout   io.ReadCloser
	DB       *sql.DB   // goja 插件专属 SQLite 数据库

	sendMu   sync.Mutex                // 串行化 stdin 写入 ← P0 修复
	readMu   sync.Mutex
	NextID   int64
	Pending  map[string]chan *RPCResponse // 以 id 的 JSON 文本为键，兼容 string/number id

	readyCh  chan struct{}             // readLoop 就绪信号 ← P0 修复
	doneCh   chan struct{}             // 进程退出信号
	closeOnce sync.Once               // 确保 doneCh 只关闭一次 ← P1 修复
	stopped  atomic.Bool              // 用户主动停止标记（避免崩溃重启循环）
	// writeBroken stdin 写超时标记：悬挂写 goroutine 仍阻塞在 Write 上无法回收，
	// 置位后禁止再发起新写入（避免与悬挂写者并发写管道导致 JSON-RPC 帧交错），
	// 悬挂写者由 stopPlugin 杀进程 / 进程退出时回收。
	writeBroken atomic.Bool
	Dir      string                    // 插件安装目录
	Status   string                    // running | stopped | crashed | unresponsive
	statusMu sync.RWMutex              // 保护 Status 的并发读写（readLoop 在无锁 goroutine 中写）

	// 健康检查
	MissedPings    int       // 连续 ping 失败次数
	UnresponsiveAt time.Time  // 标记为 unresponsive 的时间

	// Goja VM（goja runtime 插件使用）
	VM *goja.Runtime
}

// NewPluginInstance 创建插件实例
func NewPluginInstance(manifest PluginManifest, dir string) *PluginInstance {
	return &PluginInstance{
		Manifest: manifest,
		Pending:  make(map[string]chan *RPCResponse),
		readyCh:  make(chan struct{}),
		doneCh:   make(chan struct{}),
		Dir:      dir,
		Status:   "created",
	}
}

// GetStatus 线程安全地读取插件状态
func (inst *PluginInstance) GetStatus() string {
	inst.statusMu.RLock()
	defer inst.statusMu.RUnlock()
	return inst.Status
}

// SetStatus 线程安全地设置插件状态
func (inst *PluginInstance) SetStatus(s string) {
	inst.statusMu.Lock()
	defer inst.statusMu.Unlock()
	inst.Status = s
}

// ---- 管理者查询结构 ----

// PluginInfo 暴露给前端的插件信息
type PluginInfo struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	NameI18n        map[string]string `json:"nameI18n,omitempty"`
	Version         string            `json:"version"`
	Description     string            `json:"description"`
	DescriptionI18n map[string]string `json:"descriptionI18n,omitempty"`
	Author          string            `json:"author"`
	Category        string            `json:"category"`
	Status          string            `json:"status"` // running | stopped | crashed
	HasFrontend     bool              `json:"hasFrontend"`
	UsageCount      int               `json:"usageCount"`
	Commands        []Command         `json:"commands"`
}
