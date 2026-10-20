package plugin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// ---- 插件清单结构 ----

// PluginManifest 插件清单
type PluginManifest struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	NameI18n        map[string]string `json:"name_i18n,omitempty"` // 多语言名称: {locale: 名称}，如 {"en-US": "HTTP Status Codes"}
	Version         string            `json:"version"`
	Description     string            `json:"description,omitempty"`
	DescriptionI18n map[string]string `json:"description_i18n,omitempty"` // 多语言描述
	Changelog       string            `json:"changelog,omitempty"`        // 更新日志（纯文本/Markdown），市场详情页展示
	ChangelogI18n   map[string]string `json:"changelog_i18n,omitempty"`   // 多语言更新日志
	Author          string            `json:"author,omitempty"`
	Icon            string            `json:"icon,omitempty"`
	Category        string            `json:"category,omitempty"`
	Platforms       []string          `json:"platforms,omitempty"` // 支持的平台: windows/darwin/linux
	Backend         BackendConfig     `json:"backend"`
	Frontend        FrontendConfig    `json:"frontend,omitempty"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	Permissions     Permissions       `json:"permissions,omitempty"`
	Commands        []Command         `json:"commands,omitempty"`
}

// BackendConfig 后端配置
type BackendConfig struct {
	Runtime string   `json:"runtime"` // none | goja | native
	Entry   string   `json:"entry"`
	Args    []string `json:"args,omitempty"`
	// Timeout 宿主侧等待插件命令响应（plugin.execute / handleExecute）的最长秒数。
	// 0（默认）= 沿用全局 20s 兜底，用于快速探测插件崩溃。
	// 网络类插件（如 HTTP 客户端）可声明更大的值（如 120），避免慢接口被宿主误判为「插件响应超时」。
	Timeout int `json:"timeout,omitempty"`
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
	Network     NetworkPerm     `json:"network,omitempty"`    // 详见 netperm.go：bool（全放行）或域名数组白名单
	Filesystem  FilesystemPerm  `json:"filesystem,omitempty"` // 详见 fsperm.go：bool（仅对话框）或 {read,write} scope 对象
	Clipboard   bool            `json:"clipboard,omitempty"`
	Shell       ShellPerm       `json:"shell,omitempty"`       // 详见 shellperm.go：bool（全放行）或目标前缀数组
	Process     ProcessPerm     `json:"process,omitempty"`     // 详见 procperm.go：bool（全放行）或可执行程序名白名单
	ProcessKill ProcessKillPerm `json:"processKill,omitempty"` // 详见 prockillperm.go：bool（任意非系统进程）或目标进程名白名单

	// SelfAudit 是否跳过 native 插件的权限自证审计（安装期静态比对）。
	// 取值 "audit"（默认）/ "skip"。skip 时必须同时填 SkipReason。
	//
	// 为什么需要逃生舱：静态分析对「路径动态拼接」「程序名来自配置」
	// 这类写法必然漏判，一律拦截会把正常插件挡在门外。
	// 但逃生舱必须可见、可审计——因此 SkipReason 是必填项，
	// 且安装日志里会明确记录「本插件跳过了权限自证」。
	SelfAudit  string `json:"selfAudit,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

// AuditSkip 是 SelfAudit 的合法取值之一：跳过安装期权限自证。
const AuditSkip = "skip"

// AuditRun 是 SelfAudit 的默认取值：执行安装期权限自证。
const AuditRun = "audit"

// SelfAuditSkip 返回是否声明跳过权限自证。
// 只有精确等于 "skip" 才算，其余值（含空串、大小写变体）一律按「执行审计」处理——
// 宁可多审一次，也不让一个拼写错误悄悄关掉这道检查。
func (p *Permissions) SelfAuditSkip() bool {
	return strings.TrimSpace(p.SelfAudit) == AuditSkip
}

// Validate 校验逃生舱自身：声明 skip 就必须给理由，
// 理由不得为空、不得是占位性质的糊弄文字。
func (p *Permissions) Validate() error {
	switch strings.TrimSpace(p.SelfAudit) {
	case "", AuditRun:
	case AuditSkip:
		r := strings.TrimSpace(p.SkipReason)
		if r == "" {
			return fmt.Errorf("%w: permissions.selfAudit=skip 必须同时填写 permissions.skipReason（说明为何跳过权限自证）", ErrInvalidManifest)
		}
		if len([]rune(r)) < 4 {
			return fmt.Errorf("%w: permissions.skipReason 过于简单（至少 4 个字符），请如实说明原因", ErrInvalidManifest)
		}
	default:
		return fmt.Errorf(`%w: permissions.selfAudit 只能是 "audit" 或 "skip"，当前值 %q`, ErrInvalidManifest, p.SelfAudit)
	}
	return nil
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
	DB       *sql.DB // goja 插件专属 SQLite 数据库

	sendMu  sync.Mutex // 串行化 stdin 写入 ← P0 修复
	readMu  sync.Mutex
	NextID  int64
	Pending map[string]chan *RPCResponse // 以 id 的 JSON 文本为键，兼容 string/number id

	readyCh   chan struct{} // readLoop 就绪信号 ← P0 修复
	doneCh    chan struct{} // 进程退出信号
	closeOnce sync.Once     // 确保 doneCh 只关闭一次 ← P1 修复
	stopped   atomic.Bool   // 用户主动停止标记（避免崩溃重启循环）
	disabled  atomic.Bool   // 用户显式禁用标记：禁止 EnsureLoaded 惰性复活，禁用即彻底停摆
	// writeBroken stdin 写超时标记：悬挂写 goroutine 仍阻塞在 Write 上无法回收，
	// 置位后禁止再发起新写入（避免与悬挂写者并发写管道导致 JSON-RPC 帧交错），
	// 悬挂写者由 stopPlugin 杀进程 / 进程退出时回收。
	writeBroken atomic.Bool
	// lastActiveAt 最近一次"被使用"（窗口打开 / 命令执行）的时间戳（Unix 纳秒），
	// 用于闲置自动降级判断。仅作时长比较、非唯一 ID，允许纳秒精度。
	lastActiveAt atomic.Int64
	Dir          string       // 插件安装目录
	Status       string       // registered | starting | running | stopped | crashed
	statusMu     sync.RWMutex // 保护 Status 的并发读写（readLoop 在无锁 goroutine 中写）

	// Goja VM（goja runtime 插件使用）
	VM *goja.Runtime
	// vmMu 串行化对 VM 的调用：goja.Runtime 非线程安全，ExecuteCommand 可被前端并发触发，
	// 多条命令同时在同一 VM 上执行 fn() 会导致数据竞争/崩溃，故单实例并发命令须加锁排队。
	vmMu sync.Mutex
}

// NewPluginInstance 创建插件实例
func NewPluginInstance(manifest PluginManifest, dir string) *PluginInstance {
	inst := &PluginInstance{
		Manifest: manifest,
		Pending:  make(map[string]chan *RPCResponse),
		readyCh:  make(chan struct{}),
		doneCh:   make(chan struct{}),
		Dir:      dir,
		Status:   statusRegistered,
	}
	inst.lastActiveAt.Store(time.Now().UnixNano())
	return inst
}

// GetStatus 线程安全地读取插件状态。
// 显式禁用的插件一律表现为 stopped：禁用即停止态，命令面板/管理页据此统一处理，
// 避免「禁用后仍 running」撕裂（DisablePlugin 先 stopPlugin 再 SetDisabled，
// 但 EnsureLoaded 等并发路径可能读到中间态，这里兜底收敛为 stopped）。
func (inst *PluginInstance) GetStatus() string {
	if inst.disabled.Load() {
		return "stopped"
	}
	inst.statusMu.RLock()
	defer inst.statusMu.RUnlock()
	return inst.Status
}

// SetDisabled 设置插件显式禁用标记（供 service 层 DisablePlugin 标记，
// 禁止 EnsureLoaded 把已禁用插件惰性复活）。
func (inst *PluginInstance) SetDisabled(v bool) { inst.disabled.Store(v) }

// SetStatus 线程安全地设置插件状态
func (inst *PluginInstance) SetStatus(s string) {
	inst.statusMu.Lock()
	defer inst.statusMu.Unlock()
	inst.Status = s
}

// MarkActive 标记插件最近被使用（窗口打开 / 命令执行时调用），刷新闲置计时。
func (inst *PluginInstance) MarkActive() {
	inst.lastActiveAt.Store(time.Now().UnixNano())
}

// idleFor 返回距离上次被使用的时长；无活跃交互则持续增长。
func (inst *PluginInstance) idleFor() time.Duration {
	return time.Since(time.Unix(0, inst.lastActiveAt.Load()))
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
	Status          string            `json:"status"` // registered(就绪) | starting(启动中) | running(运行中) | stopped(已停止) | crashed(已崩溃)
	HasFrontend     bool              `json:"hasFrontend"`
	// Runtime 后端运行类型：none | goja | native。
	// none 表示插件无后端：命令由前端自行处理，宿主 ExecuteCommand 不执行任何 host 逻辑，
	// 故不可被 MCP/AI 真实调用。暴露此字段供 UI 与 MCP 识别，避免把前端自处理误认为宿主已执行。
	Runtime    string `json:"runtime"`
	UsageCount int    `json:"usageCount"`
	// 首次安装时间 / 最近安装或更新时间（RFC3339 本地时区），用于列表「最近安装/更新」排序。
	// 来源是 DB plugins 表的 installed_at / updated_at；内存 manager 不知情，由 service 层补齐。
	InstalledAt string    `json:"installedAt,omitempty"`
	UpdatedAt   string    `json:"updatedAt,omitempty"`
	Commands    []Command `json:"commands"`
	// Disabled 显式禁用标记：与 Status=stopped 区分「用户主动禁用」与「进程被停止/Kill」。
	// 管理页据此显示「已禁用」徽标并禁用打开按钮，命令面板据此排除（disabled 不进命令入口）。
	Disabled bool `json:"disabled"`
}
