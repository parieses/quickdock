package ai

// 外部 MCP server 接入（阶段 D）。
//
// QuickDock 一直是纯 MCP server；这一步把它也变成 client：用户在 mcp.json 里
// 配好外部 server，其工具以 mcp_<server>_<tool> 的名字注册进内置注册表
// （mcpsrv.Global），之后等级门、单工具开关、审批闸门、面板分组全部原样生效——
// 外部工具不是一条旁路，只是「一批新的 Tool」。
//
// **工具名用下划线不用方案原文的冒号**（ext:<server>:<tool>）：OpenAI 的
// function name 约束是 ^[a-zA-Z0-9_-]{1,64}$，冒号会让整个请求被上游 400 拒绝，
// 这不是风格问题而是兼容性问题。
//
// **外部工具等级恒为 LevelWrite**：默认逐次审批（ask）。别人的 server 自称
// read-only 不可信——那行 annotations 是 server 说什么就是什么，而审批策略
// 只能信我们自己的推导。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"quickdock/internal/logger"
	"quickdock/internal/mcp"
	mcpclient "quickdock/internal/mcpclient"
	"quickdock/internal/platform"
	"quickdock/services"
)

const extManifestName = "mcp.json"

// extToolsMax 给模型的 tools[] 里外部工具的名额上限。
//
// 模型请求的 tools[] 太大，一部分上游直接 400。内置工具大约 50 个上下，
// 外部给到 80 个总额是安全线；超出的不挂——并在 system hint 里说明有更多
// 可用，模型可以提示用户去面板精简。
const extToolsMax = 80

// extServerState 一个外部 server 的运行时状态（前端展示用）。
type extServerState struct {
	Name   string                 `json:"name"`
	Config mcpclient.ServerConfig `json:"config"`
	// Status ok / error / disabled
	Status string `json:"status"`
	// ToolCount 注册成功的工具数（error 时为 0）。
	ToolCount int `json:"toolCount"`
	// Error 连接或列工具失败的最近一次原因。
	Error string `json:"error,omitempty"`
	// Tools 工具名清单（前端展开看）。
	Tools []string `json:"tools"`
}

// extManager 外部 server 的连接与注册状态。
//
// 挂在 AIService 上而不是包级单例： AIService 是单例没错，但状态跟着宿主走
// 比跟着包走可测——测试里 New 一个干净的 AIService 就是干净的外部工具面。
type extManager struct {
	mu      sync.Mutex
	clients map[string]mcpclient.Client // server 名 -> client（stdio 保活，HTTP 无状态）
	states  map[string]*extServerState
}

func newExtManager() *extManager {
	return &extManager{
		clients: map[string]mcpclient.Client{},
		states:  map[string]*extServerState{},
	}
}

// extManifestPath 数据目录下的 mcp.json。
func extManifestPath() string {
	dir, _ := platform.DataDirInfo()
	return filepath.Join(dir, extManifestName)
}

// readExtManifest 读 manifest。不存在返回空 map（不是错误——第一次用是常态）。
// 格式与 Claude Desktop / Cursor 兼容：顶层 "mcpServers" 包装。
func readExtManifest() (map[string]mcpclient.ServerConfig, error) {
	data, err := os.ReadFile(extManifestPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]mcpclient.ServerConfig{}, nil
		}
		return nil, err
	}
	var doc struct {
		MCPServers map[string]mcpclient.ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("mcp.json 解析失败: %w", err)
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]mcpclient.ServerConfig{}
	}
	return doc.MCPServers, nil
}

// writeExtManifest 写 manifest。
func writeExtManifest(servers map[string]mcpclient.ServerConfig) error {
	doc := struct {
		MCPServers map[string]mcpclient.ServerConfig `json:"mcpServers"`
	}{MCPServers: servers}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(extManifestPath(), append(data, '\n'), 0o600)
}

// extToolPrefix 一个 server 的工具名前缀（含尾分隔符）。
func extToolPrefix(server string) string {
	return "mcp_" + sanitizeExtName(server) + "_"
}

// sanitizeExtName server 名 -> 工具名安全片段。
var extNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeExtName(s string) string {
	return extNameUnsafe.ReplaceAllString(strings.TrimSpace(s), "_")
}

// loadExtServers 把 manifest 里的 server 全部连接、列工具、注册进注册表。
//
// 逐个 server 独立处理：一个挂了不影响其它。返回各 server 的最终状态。
func (m *extManager) loadExtServers() []extServerState {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 先整组撤掉旧的：server 工具列表会变（加了的、删了的），逐个 diff 远不如
	// 撤掉重注可靠；残留的 Handler 还指着旧 client。
	for name := range m.states {
		mcp.RemoveExternal(extToolPrefix(name))
	}
	for name, c := range m.clients {
		_ = c.Close()
		delete(m.clients, name)
	}
	m.states = map[string]*extServerState{}

	servers, err := readExtManifest()
	if err != nil {
		logger.W("[ext-mcp] %v", err)
		return nil
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := servers[name]
		st := &extServerState{Name: name, Config: cfg, Tools: []string{}}
		m.states[name] = st
		if cfg.Disabled {
			st.Status = "disabled"
			continue
		}
		if err := mcpclient.ValidateConfig(name, cfg); err != nil {
			st.Status = "error"
			st.Error = err.Error()
			continue
		}
		c := mcpclient.New(name, cfg)
		ctx, cancel := context.WithTimeout(context.Background(), mcpclient.InitTimeout)
		tools, err := c.ListTools(ctx)
		cancel()
		if err != nil {
			st.Status = "error"
			st.Error = err.Error()
			_ = c.Close()
			logger.W("[ext-mcp] %s 不可用: %v", name, err)
			continue
		}
		m.clients[name] = c
		st.Status = "ok"
		st.ToolCount = len(tools)
		for _, t := range tools {
			st.Tools = append(st.Tools, t.Name)
			m.registerExtTool(name, t)
		}
		logger.I("[ext-mcp] %s 已接入 %d 个工具", name, len(tools))
	}
	return m.allStatesLocked()
}

func (m *extManager) allStatesLocked() []extServerState {
	out := make([]extServerState, 0, len(m.states))
	for _, st := range m.states {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// registerExtTool 把一个外部工具注册进 Global 注册表。
func (m *extManager) registerExtTool(server string, t mcpclient.Tool) {
	full := extToolPrefix(server) + sanitizeExtName(t.Name)
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = t.Name
	}
	desc = fmt.Sprintf("[MCP:%s] %s", server, desc)
	schema := t.InputSchema
	if schema == nil {
		// 严格客户端要求 inputSchema 是合法 JSON Schema；server 没给就给个空对象。
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	mcp.Global.Add(mcp.Tool{
		Name:        full,
		Description: desc,
		InputSchema: schema,
		Level:       mcp.LevelWrite,
		Category:    mcp.CategoryExternal,
		HandlerCtx: func(ctx context.Context, args map[string]any) (any, error) {
			return m.callExt(ctx, server, t.Name, args)
		},
	})
}

// callExt 路由一次外部调用。注册表里的 Handler 只认这一条路。
func (m *extManager) callExt(ctx context.Context, server, tool string, args map[string]any) (any, error) {
	m.mu.Lock()
	c, ok := m.clients[server]
	st := m.states[server]
	m.mu.Unlock()
	if !ok || st == nil {
		return nil, fmt.Errorf("外部 server %s 未连接（可在 AI 设置里重载）", server)
	}
	res, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"text": res.Text}
	if res.IsError {
		// server 明确报告失败：作为结果回给模型而不是作为错误抛出——
		// 工具执行失败是模型能读懂并自行纠正的信息，抛错误会让这一轮直接中断。
		out["isError"] = true
	}
	return out, nil
}

// Close 关闭全部 client（退出时调）。
func (m *extManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.clients {
		_ = c.Close()
		delete(m.clients, name)
	}
}

// extToolsOverflowHint 模型 tools[] 超限时给模型的说明。
const extToolsOverflowHint = "\n\n[外部工具] 本次未挂载全部外部 MCP 工具（总数超过上限）。" +
	"没有列出的工具本次调不到；确实需要时，提示用户在「设置 → AI」精简已接入的外部服务器。\n"

// -------- AIService 接线 --------

// loadExtOnStart 启动时异步接入外部 server。
//
// 异步的原因：stdio server 冷启动秒级，串在启动路径上会让整个应用慢一拍；
// 失败也不影响任何内置功能——只是面板上那一行显示 error。
func (a *AIService) loadExtOnStart() {
	if a.ext == nil {
		return
	}
	go func() {
		defer logger.RecoverPanic("ext-mcp-load")
		a.ext.loadExtServers()
	}()
}

// AIGetExtServers 列出外部 server 与接入状态（bound）。
func (a *AIService) AIGetExtServers() *services.ApiResult {
	if a.ext == nil {
		return services.Ok([]extServerState{})
	}
	a.ext.mu.Lock()
	defer a.ext.mu.Unlock()
	return services.Ok(a.ext.allStatesLocked())
}

// extServerInput 前端提交的一个 server。
type extServerInput struct {
	Name     string            `json:"name"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	URL      string            `json:"url"`
	Disabled bool              `json:"disabled"`
}

// AISaveExtServers 覆盖式保存外部 server 清单并立即重载（bound）。
//
// 覆盖式（不是增量）与 mcp.json 的语义一致：它是用户手编的文件，前端给的是
// 用户编辑后的完整清单。校验整体通过才落盘——一半合法一半非法的清单是最糟的
// 中间态，用户永远说不清哪个没生效。
func (a *AIService) AISaveExtServers(items []extServerInput) *services.ApiResult {
	servers := map[string]mcpclient.ServerConfig{}
	for _, it := range items {
		name := strings.TrimSpace(it.Name)
		cfg := mcpclient.ServerConfig{
			Command:  strings.TrimSpace(it.Command),
			Args:     it.Args,
			Env:      it.Env,
			URL:      strings.TrimSpace(it.URL),
			Disabled: it.Disabled,
		}
		if err := mcpclient.ValidateConfig(name, cfg); err != nil {
			return services.FailMsg(fmt.Sprintf("服务器 %s：%v", name, err))
		}
		servers[name] = cfg
	}
	if len(servers) > 16 {
		return services.FailMsg("外部服务器最多 16 个：每个都是一份进程与工具面，再多就管不过来了")
	}
	if err := writeExtManifest(servers); err != nil {
		return services.Fail(err)
	}
	states := a.ext.loadExtServers()
	failed := 0
	for _, st := range states {
		if st.Status == "error" {
			failed++
		}
	}
	msg := fmt.Sprintf("已保存，接入 %d 个服务器", len(states)-failed)
	if failed > 0 {
		msg += fmt.Sprintf("（%d 个连接失败，详见状态）", failed)
	}
	return services.OkMsg(map[string]any{"states": states}, msg)
}

// AIReloadExtServers 不改配置、只重连（bound）。server 侧修好了之后用。
func (a *AIService) AIReloadExtServers() *services.ApiResult {
	return services.Ok(a.ext.loadExtServers())
}

// AIListExtTools 一个 server 当前挂载的工具详情（bound，展开行用）。
func (a *AIService) AIListExtTools(server string) *services.ApiResult {
	prefix := extToolPrefix(strings.TrimSpace(server))
	out := make([]map[string]any, 0, 8)
	for _, t := range mcp.Catalog() {
		if !strings.HasPrefix(t.Name, prefix) {
			continue
		}
		out = append(out, map[string]any{
			"name": t.Name, "desc": t.Description, "disabled": t.Disabled,
		})
	}
	return services.Ok(out)
}
