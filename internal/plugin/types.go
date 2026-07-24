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
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Description string      `json:"description,omitempty"`
	Author      string      `json:"author,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Category    string      `json:"category,omitempty"`
	Backend     BackendConfig  `json:"backend"`
	Frontend    FrontendConfig `json:"frontend,omitempty"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Permissions  Permissions   `json:"permissions,omitempty"`
	Commands     []Command     `json:"commands,omitempty"`
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
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Hotkey       string   `json:"hotkey,omitempty"`
	Keywords     []string `json:"keywords,omitempty"`     // 搜索别名，用于命令面板快速查找
	Aliases      []string `json:"aliases,omitempty"`      // 中文别名，如 ["计算器", "jsq"]，扩展搜索覆盖
	Prefix       string   `json:"prefix,omitempty"`       // Slash 命令前缀，如 "/translate"，输入 /tr 时只匹配该插件
	MatchPattern string   `json:"matchPattern,omitempty"` // 命令面板正则匹配：命中时自动传入输入文本
	AcceptsInput bool     `json:"acceptsInput,omitempty"` // 是否接收命令面板传入的参数（如端口号/状态码/算式），开启后 Ctrl+K 的文本会带入插件
}

// ---- JSON-RPC 通信结构 ----

// RPCRequest JSON-RPC 2.0 请求
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse JSON-RPC 2.0 响应
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
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
	Pending  map[int64]chan *RPCResponse

	readyCh  chan struct{}             // readLoop 就绪信号 ← P0 修复
	doneCh   chan struct{}             // 进程退出信号
	closeOnce sync.Once               // 确保 doneCh 只关闭一次 ← P1 修复
	stopped  atomic.Bool              // 用户主动停止标记（避免崩溃重启循环）
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
		Pending:  make(map[int64]chan *RPCResponse),
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
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description"`
	Author      string    `json:"author"`
	Category    string    `json:"category"`
	Status      string    `json:"status"`      // running | stopped | crashed
	HasFrontend bool      `json:"hasFrontend"`
	UsageCount  int       `json:"usageCount"`
	Commands    []Command `json:"commands"`
}
