package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"quickdock/internal/logger"
	"quickdock/internal/mcp"
	"quickdock/internal/platform"
)

// mcpBaseRel MCP 配置目录（相对数据目录）：~/.quickdock/runtime/mcp
const mcpBaseRel = "runtime/mcp"

// mcpVersion MCP 服务的“版本号”。它不是外部可安装的程序，而是内置于 QuickDock 的服务，
// 故固定为 builtin，环境管理页据此展示为「已安装·内置」，不出现下载/删除入口。
const mcpVersion = "builtin"

// mcpAppVersion 应用版本号，用于 MCP initialize 的 serverInfo。由 main 包注入。
var (
	mcpAppVersionMu sync.RWMutex
	mcpAppVersion   = "dev"
)

// SetMCPAppVersion 注入应用版本（main 包启动时调用）。
func SetMCPAppVersion(v string) {
	mcpAppVersionMu.Lock()
	defer mcpAppVersionMu.Unlock()
	if v != "" {
		mcpAppVersion = v
	}
}

func getMCPAppVersion() string {
	mcpAppVersionMu.RLock()
	defer mcpAppVersionMu.RUnlock()
	return mcpAppVersion
}

// MCPConfig MCP 服务的可编辑配置（JSON，前端配置弹窗直接编辑）。
type MCPConfig struct {
	Port int `json:"port"` // 监听端口，0=由系统分配
	// MaxLevel 允许暴露的工具危险等级上限：0=只读 1=只读+低危写 2=含高危。
	MaxLevel int `json:"maxLevel"`
	// DisabledTools 被用户单独关掉的工具名。与 MaxLevel 是两道独立闸：
	// 等级按危险度一档一刀，这里是「这条我就是不想让 AI 碰」的逐条选择。
	DisabledTools []string `json:"disabledTools,omitempty"`
}

func defaultMCPConfig() MCPConfig {
	return MCPConfig{Port: mcp.DefaultPort, MaxLevel: mcp.LevelWrite}
}

// MCPRuntime 内置 MCP 服务运行时：无安装/无版本，只有启停与配置。
// 实现 ServiceController（环境管理页启停）、ConfigProvider（端口与工具等级可编辑）、
// ConfigPortsProvider（把实际监听端口回显到状态列）。
type MCPRuntime struct {
	baseDir string
}

func NewMCPRuntime() *MCPRuntime {
	return &MCPRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), mcpBaseRel)}
}

func (m *MCPRuntime) Kind() Runtime        { return RuntimeMCP }
func (m *MCPRuntime) DisplayName() string  { return DisplayName(RuntimeMCP) }
func (m *MCPRuntime) DetectArgs() []string { return nil } // 不可从外部导入
func (m *MCPRuntime) ParseVersion(string) (string, error) {
	return "", fmt.Errorf("%s 为内置服务，不支持版本探测", DisplayName(RuntimeMCP))
}
func (m *MCPRuntime) SupportedPlatforms() []string {
	return []string{"windows", "darwin", "linux"}
}
func (m *MCPRuntime) Recommended() []string { return nil }
func (m *MCPRuntime) ExeFor(string) string  { return "" }

// InstalledVersions 固定返回内置版本，使环境管理页把它当作「已安装」而非「未安装」。
func (m *MCPRuntime) InstalledVersions() []Install {
	return []Install{{Version: mcpVersion, Scope: "system", Path: m.baseDir, Active: true}}
}

func (m *MCPRuntime) Install(context.Context, string, InstallCallback) error {
	return fmt.Errorf("%s 内置于 QuickDock，无需安装", DisplayName(RuntimeMCP))
}

func (m *MCPRuntime) DeleteVersion(string) error {
	return fmt.Errorf("%s 内置于 QuickDock，不可删除", DisplayName(RuntimeMCP))
}

// -------- 配置 --------

func (m *MCPRuntime) configPath() string { return filepath.Join(m.baseDir, "config.json") }

// ConfigPath 供通用配置读写使用。首次访问时落一份默认配置，避免前端配置弹窗「文件不存在」。
func (m *MCPRuntime) ConfigPath(string) string {
	_ = os.MkdirAll(m.baseDir, 0o755)
	if _, err := os.Stat(m.configPath()); err != nil {
		raw, _ := json.MarshalIndent(defaultMCPConfig(), "", "  ")
		_ = os.WriteFile(m.configPath(), raw, 0o644)
	}
	return m.configPath()
}

// loadConfig 读取配置，缺失/损坏时回退默认值（不因配置写坏就起不来服务）。
func (m *MCPRuntime) loadConfig() MCPConfig {
	cfg := defaultMCPConfig()
	data, err := os.ReadFile(m.configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.Port < 0 || cfg.Port > 65535 {
		cfg.Port = defaultMCPConfig().Port
	}
	if cfg.MaxLevel < mcp.LevelRead || cfg.MaxLevel > mcp.LevelRisk {
		cfg.MaxLevel = defaultMCPConfig().MaxLevel
	}
	return cfg
}

// -------- 服务启停 --------

// SetMaxLevel 持久化工具等级上限，并立即对注册表生效。
//
// 必须落盘：Start 每次都从 config.json 读回等级。只改内存的话，用户在面板里
// 选完「只读」、重启一次服务就被悄悄改回默认的「只读+低危写」——面板看起来
// 设置了，实际配置另有出处，这种静默回滚最难排查。
func (m *MCPRuntime) SetMaxLevel(level int) error {
	if level < mcp.LevelRead || level > mcp.LevelRisk {
		return fmt.Errorf("工具等级超出范围: %d", level)
	}
	cfg := m.loadConfig()
	cfg.MaxLevel = level
	if err := m.saveConfig(cfg); err != nil {
		return err
	}
	mcp.SetMaxLevel(level)
	return nil
}

// SetToolEnabled 单独开启/关闭某个工具，落盘后立即对注册表生效。
//
// 名单按名称去重后写回：面板反复点同一条不能把 config 堆成一串重复项。
// 未知工具名不做校验——注册表才是工具是否存在的权威，配置里残留一个已删除的
// 工具名最多是无害噪音，而在这里拦下来会让「先关工具、后删工具」的正常顺序报错。
func (m *MCPRuntime) SetToolEnabled(name string, enabled bool) error {
	if name == "" {
		return fmt.Errorf("缺少工具名")
	}
	cfg := m.loadConfig()
	next := make([]string, 0, len(cfg.DisabledTools)+1)
	for _, n := range cfg.DisabledTools {
		if n != name && n != "" {
			next = append(next, n)
		}
	}
	if !enabled {
		next = append(next, name)
	}
	cfg.DisabledTools = next
	if err := m.saveConfig(cfg); err != nil {
		return err
	}
	mcp.SetDisabled(cfg.DisabledTools)
	return nil
}

// saveConfig 写回配置并落一份目录（首次写入时目录可能还不存在）。
func (m *MCPRuntime) saveConfig(cfg MCPConfig) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.configPath(), raw, 0o644)
}

func (m *MCPRuntime) DefaultPort() int { return m.loadConfig().Port }

// ConfiguredPorts 回显真实端口（配置解析，未配置时用实际监听端口）。
func (m *MCPRuntime) ConfiguredPorts(string) []int {
	if p := m.loadConfig().Port; p > 0 {
		return []int{p}
	}
	if p := mcp.Default.Port(); p > 0 {
		return []int{p}
	}
	return nil
}

func (m *MCPRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	cfg := m.loadConfig()
	mcp.SetMaxLevel(cfg.MaxLevel)
	// 单工具开关同样每次启动从配置读回：面板关掉的工具不能因为重启服务又冒出来。
	mcp.SetDisabled(cfg.DisabledTools)
	addr, err := mcp.Default.Start(cfg.Port, getMCPAppVersion())
	if err != nil {
		return err
	}
	// 登记到会话内服务表：让启动前的端口冲突检测把「本服务自己在监听」识别为 Ours，
	// 否则重启会被自己的端口拦下。记录不含进程句柄（MCP 是进程内服务），killTracked 对它是空操作。
	svcMgr.mu.Lock()
	svcMgr.svcs[RuntimeMCP] = &runningService{version: mcpVersion}
	svcMgr.mu.Unlock()
	if onLog != nil {
		onLog("MCP 服务已启动: http://" + addr + "/mcp")
	}
	logger.I("[env] MCP 服务启动 port=%d tools=%d", mcp.Default.Port(), len(mcp.List()))
	return nil
}

func (m *MCPRuntime) Stop(string) error {
	if err := mcp.Default.Stop(); err != nil {
		return err
	}
	svcMgr.forget(RuntimeMCP)
	return nil
}

func (m *MCPRuntime) Status(string) ServiceStatus {
	running := mcp.Default.Running()
	st := ServiceStatus{Running: running, Version: getMCPAppVersion()}
	if p := mcp.Default.Port(); p > 0 {
		st.Port = p
		st.Ports = []int{p}
	}
	// 未运行但端口被别的程序占用时，前端能看出「为什么起不来」
	if !running {
		if p := m.loadConfig().Port; p > 0 {
			st.Port = p
			st.Ports = []int{p}
		}
	}
	return st
}

// ---- 编译期接口断言 ----
//
// 可选能力接口靠隐式 method set 满足，某天改掉一个方法签名不会报错、只会在运行时
// 静默失去该能力（按钮消失/日志空白）。这里逐一固定下来，让编译器替我们守着。
var _ ServiceController = (*MCPRuntime)(nil)   // 服务启停与状态
var _ ConfigProvider = (*MCPRuntime)(nil)      // 配置读写
var _ ConfigPortsProvider = (*MCPRuntime)(nil) // 配置内端口解析
