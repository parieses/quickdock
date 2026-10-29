// Package mcp 暴露 MCP 服务给前端（环境管理页的启停/状态/工具清单），
// 并把 QuickDock 的业务能力注册为 MCP 工具，供 AI 客户端调用。
//
// 分层：internal/mcp 只管协议与传输（不含业务），本包负责把 AppService 的领域方法包装成工具。
// 危险等级：LevelRead 只读（默认全开）、LevelWrite 低危写（默认开）、LevelRisk 高危（默认不注册任何工具）。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	mcpsrv "quickdock/internal/mcp"
	pluginmgr "quickdock/internal/plugin"
	"quickdock/services"
	clipboardsvc "quickdock/services/clipboard"
	collectionsvc "quickdock/services/collection"
	diagsvc "quickdock/services/diag"
	frecencysvc "quickdock/services/frecency"
	notesvc "quickdock/services/note"
	pluginsvc "quickdock/services/plugin"
	portsvc "quickdock/services/port"
	systemsvc "quickdock/services/system"
	todosvc "quickdock/services/todo"
	workspacesvc "quickdock/services/workspace"
)

// builtinVersion MCP 在环境管理里的固定版本号（内置服务，无真实版本概念）
const builtinVersion = "builtin"

// MCPService 前端绑定服务：MCP 的启停、状态、工具清单与客户端配置。
type MCPService struct {
	App        *services.AppService
	Clip       *clipboardsvc.ClipboardService
	Plugin     *pluginsvc.PluginService
	Workspace  *workspacesvc.WorkspaceService
	Todo       *todosvc.TodoService
	Diag       *diagsvc.DiagService
	System     *systemsvc.SystemService
	Note       *notesvc.NoteService
	Frecency   *frecencysvc.FrecencyService
	Port       *portsvc.PortService
	Collection *collectionsvc.CollectionService
	version    string
}

// NewMCPService 创建 MCP 绑定服务，并注册业务工具。
func NewMCPService(app *services.AppService, clip *clipboardsvc.ClipboardService, plugin *pluginsvc.PluginService, workspace *workspacesvc.WorkspaceService, todo *todosvc.TodoService, diag *diagsvc.DiagService, sys *systemsvc.SystemService, note *notesvc.NoteService, frecency *frecencysvc.FrecencyService, port *portsvc.PortService, collection *collectionsvc.CollectionService, version string) *MCPService {
	s := &MCPService{App: app, Clip: clip, Plugin: plugin, Workspace: workspace, Todo: todo, Diag: diag, System: sys, Note: note, Frecency: frecency, Port: port, Collection: collection, version: version}
	s.registerTools()
	return s
}

// -------- 前端绑定方法 --------

// MCPStatus 返回 MCP 服务运行状态、监听地址与工具开放情况。
//
// tools / total / blocked 三个数是给面板解释「为什么工具比预期少」用的：
// 只报一个数时，用户看到工具变少会怀疑是服务出问题，而实际是等级门正常拦截。
func (s *MCPService) MCPStatus() *services.ApiResult {
	cat := mcpsrv.Catalog()
	exposed := 0
	for _, t := range cat {
		if t.Exposed {
			exposed++
		}
	}
	return services.Ok(map[string]any{
		"running":  mcpsrv.Default.Running(),
		"endpoint": mcpsrv.Default.Endpoint(),
		"port":     mcpsrv.Default.Port(),
		"maxLevel": mcpsrv.MaxLevel(),
		"tools":    exposed,
		"total":    len(cat),
		"blocked":  len(cat) - exposed,
	})
}

// MCPTools 列出全部已注册工具（名称/说明/危险等级/分类/是否开放），供环境管理页展示。
// 未开放的也要给前端：面板据此显示「被等级挡住」或「被单独关掉」的工具及其原因，
// 而不是留下空白。
func (s *MCPService) MCPTools() *services.ApiResult {
	cat := mcpsrv.Catalog()
	out := make([]map[string]any, 0, len(cat))
	for _, t := range cat {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"level":       t.Level,
			"category":    t.Category,
			"exposed":     t.Exposed,
			"disabled":    t.Disabled,
		})
	}
	return services.Ok(out)
}

// MCPStart 启动 MCP 服务（等同环境管理页点「启动」），返回监听地址。
func (s *MCPService) MCPStart() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Start(envengine.RuntimeMCP, builtinVersion, nil); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(mcpsrv.Default.Endpoint())
}

// MCPStop 停止 MCP 服务。
func (s *MCPService) MCPStop() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Stop(envengine.RuntimeMCP, builtinVersion); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(nil)
}

// MCPRestart 重启 MCP 服务：改了工具等级或端口后用它让客户端重新握手。
// 走 Manager.Restart 而不是前端「停一下再启动」：重启前会重跑端口冲突与配置校验，
// 两条独立调用会把这些守卫绕过。
func (s *MCPService) MCPRestart() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Restart(envengine.RuntimeMCP, builtinVersion, nil); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(mcpsrv.Default.Endpoint())
}

// MCPSetLevel 设置可暴露的工具危险等级上限：0=只读，1=只读+低危写。
//
// 等级会写回 runtime/mcp/config.json —— 只改内存的话重启服务就回滚成默认值。
// 正在运行的实例，工具集要等客户端重连（或重启服务）才更新，故返回 needsRestart
// 让前端提示「重启服务」而不是让用户对着没变化的客户端怀疑设置没生效。
func (s *MCPService) MCPSetLevel(level int) *services.ApiResult {
	if level < mcpsrv.LevelRead || level > mcpsrv.LevelWrite {
		return services.FailMsg("等级只能是 0（只读）或 1（只读+低危写）")
	}
	if s.App != nil && s.App.Env != nil {
		if rt := s.App.Env.MCPRuntime(); rt != nil {
			if err := rt.SetMaxLevel(level); err != nil {
				return services.FailMsg("保存工具等级失败: " + err.Error())
			}
		}
	}
	mcpsrv.SetMaxLevel(level)
	return services.Ok(map[string]any{
		"maxLevel":     mcpsrv.MaxLevel(),
		"running":      mcpsrv.Default.Running(),
		"needsRestart": mcpsrv.Default.Running(),
	})
}

// MCPSetToolEnabled 单独开启/关闭某个工具（面板上逐条控制），与危险等级是两道闸。
//
// 等级门解决「一刀切」，但仍有人只想摘掉一条（比如允许写剪贴板、但不让 AI 建笔记）。
// 关闭只影响 List/Call，工具仍留在注册表里，故面板能原样展示并重新开启。
// 与等级一样要落盘，且同样需要客户端重连才生效，故返回 needsRestart。
func (s *MCPService) MCPSetToolEnabled(name string, enabled bool) *services.ApiResult {
	if name == "" {
		return services.FailMsg("缺少工具名")
	}
	known := false
	for _, t := range mcpsrv.Catalog() {
		if t.Name == name {
			known = true
			break
		}
	}
	if !known {
		return services.FailMsg("未知工具: " + name)
	}
	rt := (*envengine.MCPRuntime)(nil)
	if s.App != nil && s.App.Env != nil {
		rt = s.App.Env.MCPRuntime()
	}
	if rt != nil {
		if err := rt.SetToolEnabled(name, enabled); err != nil {
			return services.FailMsg("保存工具开关失败: " + err.Error())
		}
	} else {
		// env 未就绪（理论上只在极端初始化顺序下发生）：至少让本次会话按用户选择生效，
		// 而不是静默忽略点击。
		next := make([]string, 0, len(mcpsrv.Disabled())+1)
		for _, n := range mcpsrv.Disabled() {
			if n != name {
				next = append(next, n)
			}
		}
		if !enabled {
			next = append(next, name)
		}
		mcpsrv.SetDisabled(next)
	}
	return services.Ok(map[string]any{
		"name":         name,
		"enabled":      enabled,
		"running":      mcpsrv.Default.Running(),
		"needsRestart": mcpsrv.Default.Running(),
	})
}

// mcpClient 一种 MCP 客户端的接入方式（配置片段 + 该写到哪个文件）。
type mcpClient struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Path   string `json:"path"`   // 配置应写到哪里；纯命令行方式为空
	Config string `json:"config"` // 可直接粘贴或执行的内容
}

// MCPClientConfig 生成各客户端的接入片段，未运行时全部为空串，由前端提示先启动。
//
// 这里不偷懒只给一份 mcpServers：VS Code 用 servers + type=http，Claude Code 走 CLI，
// 各家配置文件位置也不同。给成一份「通用 JSON」的结果是用户粘进去客户端不认，
// 却看不出错在哪。
func (s *MCPService) MCPClientConfig() *services.ApiResult {
	ep := mcpsrv.Default.Endpoint()
	if ep == "" {
		return services.Ok(map[string]any{"running": false, "clients": []mcpClient{}})
	}
	serversJSON := fmt.Sprintf(`{
  "mcpServers": {
    "quickdock": {
      "type": "http",
      "url": "%s"
    }
  }
}`, ep)
	vsCodeJSON := fmt.Sprintf(`{
  "servers": {
    "quickdock": {
      "type": "http",
      "url": "%s"
    }
  }
}`, ep)
	cli := "claude mcp add --transport http quickdock " + ep
	curl := fmt.Sprintf("curl -s -X POST %s -H \"Content-Type: application/json\" -H \"Accept: application/json, text/event-stream\" -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}'", ep)

	clients := []mcpClient{
		{ID: "claude-code", Label: "Claude Code", Config: cli},
		{ID: "claude-desktop", Label: "Claude Desktop", Path: claudeDesktopConfigPath(), Config: serversJSON},
		{ID: "cursor", Label: "Cursor", Path: "~/.cursor/mcp.json", Config: serversJSON},
		{ID: "cline", Label: "Cline / Roo Code", Path: "~/.codeium/windsurf/mcp_config.json（同类客户端通用 mcpServers 格式）", Config: serversJSON},
		{ID: "vscode", Label: "VS Code (Copilot)", Path: ".vscode/mcp.json", Config: vsCodeJSON},
		{ID: "curl", Label: "curl 连通性验证", Config: curl},
	}
	return services.Ok(map[string]any{"running": true, "clients": clients})
}

// MCPCheck 连通性自检：像真实客户端一样走一次 initialize + tools/list。
//
// 端口在监听不等于服务可用——等级把工具全滤掉、注册漏了、协议实现写错，
// 都表现为「连得上但客户端没反应」。让用户能在面板上一键拿到结论，
// 比让他去翻客户端日志快得多。
func (s *MCPService) MCPCheck() *services.ApiResult {
	ep := mcpsrv.Default.Endpoint()
	if ep == "" {
		return services.FailMsg("MCP 服务未启动")
	}
	start := time.Now()
	init, err := mcpRPC(ep, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+mcpsrv.ProtocolVersion+`","capabilities":{},"clientInfo":{"name":"quickdock-self-check","version":"1"}}}`)
	if err != nil {
		return services.FailMsg("握手失败: " + err.Error())
	}
	protocol, _ := init["protocolVersion"].(string)
	serverName, serverVersion := "", ""
	if info, ok := init["serverInfo"].(map[string]any); ok {
		serverName, _ = info["name"].(string)
		serverVersion, _ = info["version"].(string)
	}
	list, err := mcpRPC(ep, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if err != nil {
		return services.FailMsg("拉取工具清单失败: " + err.Error())
	}
	toolCount := 0
	if arr, ok := list["tools"].([]any); ok {
		toolCount = len(arr)
	}
	return services.Ok(map[string]any{
		"ok":              true,
		"endpoint":        ep,
		"protocolVersion": protocol,
		"serverName":      serverName,
		"serverVersion":   serverVersion,
		"tools":           toolCount,
		"latencyMs":       time.Since(start).Milliseconds(),
	})
}

// mcpRPC 向本机 MCP 端点发一条 JSON-RPC 请求，返回 result。
// 显式禁用环境代理：127.0.0.1 走代理会表现为「连不上自己的服务」，很难想到是代理配置的锅。
func mcpRPC(endpoint, body string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("响应不是合法 JSON: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("JSON-RPC %d: %s", out.Error.Code, out.Error.Message)
	}
	if out.Result == nil {
		return nil, errors.New("响应缺少 result")
	}
	return out.Result, nil
}

// claudeDesktopConfigPath 返回 Claude Desktop 配置文件位置（写进面板的「配置放哪」提示）。
func claudeDesktopConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Claude", "claude_desktop_config.json")
		}
		return `%APPDATA%\Claude\claude_desktop_config.json`
	case "darwin":
		return "~/Library/Application Support/Claude/claude_desktop_config.json"
	default:
		return "~/.config/Claude/claude_desktop_config.json"
	}
}

// -------- 工具注册 --------

// toolRegistrar 单个域的工具注册入口：把域写进每个 Tool.Category，并统一补 InputSchema。
//
// provider 拿到的是这个薄壳而不是全局注册表：注册去向（Global / 测试用实例）
// 由装配处决定，域文件里连 mcpsrv.Register 长什么样都不必知道。
// 等级只存在注册表的 Tool.Level 一处——面板展示读的是 mcpsrv.Catalog()，
// 早期在服务层另存一份 toolLevels 的做法让「等级」有了两个来源，加工具时漏改
// 一份就会显示成别的档位。
type toolRegistrar struct {
	reg      mcpsrv.Registrar
	category string
}

func (r *toolRegistrar) add(run func(args map[string]any) (any, error), level int, name, desc string, required []string, props map[string]any) {
	r.reg.Add(mcpsrv.Tool{
		Name:        name,
		Description: desc,
		InputSchema: mcpsrv.Schema("", required, props),
		Level:       level,
		Category:    r.category,
		Handler:     run,
	})
}

// toolProvider 一个域的工具集合：分类 id + 注册函数。
type toolProvider struct {
	Category string
	Register func(s *MCPService, r *toolRegistrar)
}

// toolProviders 装配清单。加一个域 = 新建 tools_<域>.go + 在下面加一行。
//
// 顺序不影响 tools/list 的输出（registry 的 List/Catalog 都按等级+名称排序），
// 只决定 init 期注册先后，故按「宿主 → 环境 → 启动台 → 内容 → 诊断 → 扩展 → 高危」排列。
func toolProviders() []toolProvider {
	return []toolProvider{
		{Category: mcpsrv.CategoryApp, Register: registerAppTools},             // 宿主信息、监听端口、机器人通知
		{Category: mcpsrv.CategoryEnv, Register: registerEnvTools},             // 运行时状态/日志/版本、服务启停、Everything 检索
		{Category: mcpsrv.CategoryItem, Register: registerItemTools},           // 快捷项、最近使用、工作空间
		{Category: mcpsrv.CategoryNote, Register: registerNoteTools},           // 笔记与文件夹
		{Category: mcpsrv.CategoryTodo, Register: registerTodoTools},           // 待办
		{Category: mcpsrv.CategoryClipboard, Register: registerClipboardTools}, // 剪贴板历史与写入
		{Category: mcpsrv.CategoryDiag, Register: registerDiagTools},           // 日志与崩溃记录
		{Category: mcpsrv.CategoryPlugin, Register: registerPluginTools},       // 插件清单与命令执行
		{Category: mcpsrv.CategoryRisk, Register: registerRiskTools},           // 高危：当前恒不开放
	}
}

// registerTools 按装配清单注册全部工具。
func (s *MCPService) registerTools() {
	for _, p := range toolProviders() {
		p.Register(s, &toolRegistrar{reg: mcpsrv.Global, category: p.Category})
	}
}

// unwrap 把 ApiResult 转成工具结果：Code!=0 视为错误，错误消息直接给模型看。
func unwrap(r *services.ApiResult) (any, error) {
	if r == nil {
		return nil, errors.New("服务返回为空")
	}
	if r.Code != 0 {
		return nil, errors.New(r.Msg)
	}
	return r.Data, nil
}

// envStart 启动服务；已在运行时不重复拉起（避免端口冲突检测把自身占用误判为冲突）。
func (s *MCPService) envStart(id, version string) (any, error) {
	if s.App.Env == nil {
		return nil, errors.New("环境管理未初始化")
	}
	ver := pickVersion(s.App.Env, id, version)
	if st, err := s.App.Env.Status(envengine.Runtime(id), ver); err == nil && st.Running {
		return id + " 已在运行（端口 " + strings.Trim(fmt.Sprint(st.Ports), "[]") + "）", nil
	}
	if err := s.App.Env.Start(envengine.Runtime(id), ver, nil); err != nil {
		return nil, err
	}
	st, _ := s.App.Env.Status(envengine.Runtime(id), ver)
	return map[string]any{"runtime": id, "version": ver, "running": st.Running, "ports": st.Ports}, nil
}

// allItems 取全部快捷项（供 item_open 按 id 定位）。
func (s *MCPService) allItems() ([]db.CollectionItem, error) {
	res := s.App.ListAllItems()
	if res.Code != 0 {
		return nil, errors.New(res.Msg)
	}
	items, _ := res.Data.([]db.CollectionItem)
	if len(items) == 0 {
		return nil, errors.New("快捷项为空")
	}
	return items, nil
}

// pickVersion 取要操作的版本：显式指定优先，否则用已装的第一个版本。
func pickVersion(m *envengine.Manager, id, version string) string {
	if version != "" {
		return version
	}
	if installs, err := m.InstalledVersions(envengine.Runtime(id)); err == nil && len(installs) > 0 {
		return installs[0].Version
	}
	return ""
}

// pluginRuntime 返回插件的后端运行时（native/goja/none）；查不到时返回空串。
// 供 plugin_execute 在调用前判断：runtime=none 的插件经 MCP 执行只会假成功，须直接拒绝。
func (s *MCPService) pluginRuntime(pluginID string) string {
	if s.Plugin == nil {
		return ""
	}
	res := s.Plugin.ListPlugins()
	if res == nil || res.Code != 0 {
		return ""
	}
	list, ok := res.Data.([]pluginmgr.PluginInfo)
	if !ok {
		return ""
	}
	for _, p := range list {
		if p.ID == pluginID {
			return p.Runtime
		}
	}
	return ""
}
