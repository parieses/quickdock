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
	"sort"
	"strings"
	"time"

	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	mcpsrv "quickdock/internal/mcp"
	pluginmgr "quickdock/internal/plugin"
	"quickdock/services"
	clipboardsvc "quickdock/services/clipboard"
	collectionsvc "quickdock/services/collection"
	credentialssvc "quickdock/services/credentials"
	diagsvc "quickdock/services/diag"
	downloadsvc "quickdock/services/download"
	envsvc "quickdock/services/env"
	frecencysvc "quickdock/services/frecency"
	itemsvc "quickdock/services/item"
	kbsvc "quickdock/services/kb"
	mailboxsvc "quickdock/services/mailbox"
	monitorsvc "quickdock/services/monitor"
	netdisksvc "quickdock/services/netdisk"
	notesvc "quickdock/services/note"
	pluginsvc "quickdock/services/plugin"
	portsvc "quickdock/services/port"
	scenesvc "quickdock/services/scene"
	shelfsvc "quickdock/services/shelf"
	sshxsvc "quickdock/services/sshx"
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
	// Monitor 监控门面：定时任务挂在 AppService 上，监控是独立子包，故单独注入。
	Monitor *monitorsvc.MonitorService
	// KB 知识库门面：只读（kb_search / kb_list），写入路径不开放给模型。
	KB *kbsvc.KBService
	// SSH 门面：与 SSH 页共用同一个实例（连接池 / 会话管理器不能另起一份）。
	// 只读 + 受限执行工具靠它的主机级授权兜住，默认一台主机都不开放。
	SSH *sshxsvc.SshService
	// Download 下载器门面：与页面共用同一个实例（轮询、通知去重状态不能另起一份）。
	Download *downloadsvc.DownloadService
	// 以下门面为补齐 UI 侧已有、MCP 先前缺失的只读查询而注入。
	// 复用 main 里的同一实例，避免另起一份导致状态/缓存与页面不一致。
	Env         *envsvc.EnvironmentService
	Netdisk     *netdisksvc.NetdiskService
	Mailbox     *mailboxsvc.MailboxService
	Credentials *credentialssvc.CredentialsService
	Shelf       *shelfsvc.ShelfService
	Scene       *scenesvc.SceneService
	Item        *itemsvc.ItemService
	version     string
}

// NewMCPService 创建 MCP 绑定服务，并注册业务工具。
//
// ssh 必须是 main 里与 SSH 页共用的那个实例：MCP 工具复用它的连接池与
// 会话管理器，另起一份会让「list_sessions 看不到已开终端」这类问题出现。
func NewMCPService(app *services.AppService, clip *clipboardsvc.ClipboardService, plugin *pluginsvc.PluginService, workspace *workspacesvc.WorkspaceService, todo *todosvc.TodoService, diag *diagsvc.DiagService, sys *systemsvc.SystemService, note *notesvc.NoteService, frecency *frecencysvc.FrecencyService, port *portsvc.PortService, collection *collectionsvc.CollectionService, monitor *monitorsvc.MonitorService, kb *kbsvc.KBService, download *downloadsvc.DownloadService, ssh *sshxsvc.SshService, env *envsvc.EnvironmentService, netdisk *netdisksvc.NetdiskService, mailbox *mailboxsvc.MailboxService, credentials *credentialssvc.CredentialsService, shelf *shelfsvc.ShelfService, scene *scenesvc.SceneService, item *itemsvc.ItemService, version string) *MCPService {
	s := &MCPService{App: app, Clip: clip, Plugin: plugin, Workspace: workspace, Todo: todo, Diag: diag, System: sys, Note: note, Frecency: frecency, Port: port, Collection: collection, Monitor: monitor, KB: kb, Download: download, SSH: ssh, Env: env, Netdisk: netdisk, Mailbox: mailbox, Credentials: credentials, Shelf: shelf, Scene: scene, Item: item, version: version}
	s.registerTools()
	return s
}

// -------- 前端绑定方法 --------

// MCPStatus 返回 MCP 服务运行状态、监听地址与工具开放情况。
//
// 三个计数只统计对外工具（Global 注册表），agent 工具单独给一个 agent 数：
// 混在一起算的话，面板上会显示「43 / 52」，用户的第一反应是「9 个被挡住了」，
// 然后去调等级——怎么调都不会变，因为那 9 个本来就不归 MCP 管。
func (s *MCPService) MCPStatus() *services.ApiResult {
	cat := mcpsrv.Catalog()
	exposed, total, agent := 0, 0, 0
	for _, t := range cat {
		if t.AgentOnly {
			if !t.Disabled && t.Level <= mcpsrv.MaxLevel() {
				agent++
			}
			continue
		}
		total++
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
		"total":    total,
		"blocked":  total - exposed,
		"agent":    agent,
	})
}

// MCPTools 列出全部已注册工具（名称/说明/危险等级/分类/是否开放），供环境管理页展示。
// 未开放的也要给前端：面板据此显示「被等级挡住」或「被单独关掉」的工具及其原因，
// 而不是留下空白。agent 工具一并返回并带 agentOnly 标记，面板据此打「仅内置 AI」徽章。
func (s *MCPService) MCPTools() *services.ApiResult {
	cat := mcpsrv.Catalog()
	out := make([]map[string]any, 0, len(cat))
	for _, t := range cat {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"level":       t.Level,
			// domain 是新的单一来源；category 保留给还没迁移的读取方（同值）。
			"domain":    t.Domain,
			"category":  t.Domain,
			"exposed":   t.Exposed,
			"disabled":  t.Disabled,
			"agentOnly": t.AgentOnly,
			// notReady：运行环境未就绪（Everything 没装/没跑）。这类工具开关再怎么开，
			// 装配时都会被拦掉，模型看不到——面板要能说明这件事，否则像故障。
			"notReady":     t.NotReady,
			"notReadyNote": t.NotReadyNote,
		})
	}
	return services.Ok(out)
}

// MCPDomains 返回能力域清单（面板分组顺序与标题的唯一来源）。
//
// 以前这份顺序写在前端 MCP_CATEGORY_ORDER 里，后端加一个域、前端没补就整组
// 显示成 id；两边各维护一份迟早漂。现在前端只按 order 排、按 label 取 i18n key。
func (s *MCPService) MCPDomains() *services.ApiResult {
	out := make([]map[string]any, 0, 20)
	for _, d := range mcpsrv.Domains() {
		out = append(out, map[string]any{
			"id":     d.ID,
			"label":  d.LabelKey,
			"order":  d.Order,
			"desc":   d.Summary,
			"public": d.External,
		})
	}
	return services.Ok(out)
}

// MCPDoctor 自检：每个工具一行，说明它在外部 MCP 侧到底开没开、没开为什么。
//
// 存在的理由是「排查为什么它调不到」不该靠读代码。三个面板（MCP / AI 设置 / 审批策略）
// 各显示一部分状态，用户看到「关着」时最常问的是「谁关的、哪一层关的」——
// 这里给一句话答案：超出等级上限 / 已单独关闭 / 不对外部客户端开放。
//
// AI 侧的开关不在这里：那是另一份策略（ai_tool_policy），由 AIToolPolicy 报。
func (s *MCPService) MCPDoctor() *services.ApiResult {
	sf := mcpsrv.Resolve(mcpsrv.ToolScope{Caller: mcpsrv.CallerMCP, Policy: mcpsrv.MCPPolicy()})
	reason := make(map[string]string, len(sf.Dropped))
	for _, d := range sf.Dropped {
		reason[d.Name] = d.Reason
	}
	open := make(map[string]bool, len(sf.Tools))
	for _, t := range sf.Tools {
		open[t.Name] = true
	}
	out := make([]map[string]any, 0, len(sf.Tools)+len(sf.Dropped))
	for _, t := range mcpsrv.Catalog() {
		out = append(out, map[string]any{
			"name":      t.Name,
			"domain":    t.Domain,
			"level":     t.Level,
			"public":    t.Exposed,
			"open":      open[t.Name],
			"reason":    reason[t.Name],
			"agentOnly": t.AgentOnly,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return services.Ok(map[string]any{
		"maxLevel": mcpsrv.MaxLevel(),
		"disabled": mcpsrv.Disabled(),
		"tools":    out,
	})
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

// mcpRuntime 取 MCP 运行时。令牌操作与工具开关都走它，省掉每个方法里重复一遍
// 「App / Env 可能为 nil」的判空（初始化顺序靠后的极端情况下确实会为 nil）。
func (s *MCPService) mcpRuntime() *envengine.MCPRuntime {
	if s.App == nil || s.App.Env == nil {
		return nil
	}
	return s.App.Env.MCPRuntime()
}

// ---- 访问令牌管理 ----

// MCPToken 返回当前令牌与「是否强制校验」，供环境管理页展示。
// 明文令牌只在本机进程内交给 WebView，不写日志、不进用量统计。
func (s *MCPService) MCPToken() *services.ApiResult {
	rt := s.mcpRuntime()
	if rt == nil {
		return services.FailMsg("env 未初始化")
	}
	st := rt.TokenState()
	return services.Ok(map[string]any{"token": st.Token, "requireToken": st.RequireToken})
}

// MCPSetTokenEnabled 开启/关闭强制校验。运行中改动立即生效，不用重启服务；
// 返回最新状态让面板重拉接入片段（开关一变，片段里的请求头也得跟着变）。
func (s *MCPService) MCPSetTokenEnabled(enabled bool) *services.ApiResult {
	rt := s.mcpRuntime()
	if rt == nil {
		return services.FailMsg("env 未初始化")
	}
	st := rt.SetRequireToken(enabled)
	return services.Ok(map[string]any{
		"token": st.Token, "requireToken": st.RequireToken,
		"running": mcpsrv.Default.Running(),
	})
}

// MCPRotateToken 轮换令牌：旧令牌立即失效，已配置的客户端必须重新复制接入片段。
func (s *MCPService) MCPRotateToken() *services.ApiResult {
	rt := s.mcpRuntime()
	if rt == nil {
		return services.FailMsg("env 未初始化")
	}
	st := rt.RotateToken()
	return services.Ok(map[string]any{
		"token": st.Token, "requireToken": st.RequireToken,
		"running": mcpsrv.Default.Running(),
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
	// 强制校验时六份片段必须一起带上令牌：漏一份就有一类客户端连不上，用户只会
	// 看到「接了没反应」，很难想到是少了个请求头。片段里含明文令牌，面板要提醒
	// 别把它贴到公开仓库。
	token, require := "", false
	if rt := s.mcpRuntime(); rt != nil {
		st := rt.TokenState()
		token, require = st.Token, st.RequireToken
	}
	headers, authHeader, cliHeader := "", "", ""
	if require && token != "" {
		headers = fmt.Sprintf(",\n      \"headers\": { \"Authorization\": \"Bearer %s\" }", token)
		authHeader = fmt.Sprintf(` -H "Authorization: Bearer %s"`, token)
		cliHeader = fmt.Sprintf(` --header "Authorization: Bearer %s"`, token)
	}
	serversJSON := fmt.Sprintf(`{
  "mcpServers": {
    "quickdock": {
      "type": "http",
      "url": "%s"%s
    }
  }
}`, ep, headers)
	vsCodeJSON := fmt.Sprintf(`{
  "servers": {
    "quickdock": {
      "type": "http",
      "url": "%s"%s
    }
  }
}`, ep, headers)
	cli := "claude mcp add --transport http quickdock " + ep + cliHeader
	curl := fmt.Sprintf("curl -s -X POST %s -H \"Content-Type: application/json\" -H \"Accept: application/json, text/event-stream\"%s -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}'", ep, authHeader)

	clients := []mcpClient{
		{ID: "claude-code", Label: "Claude Code", Config: cli},
		{ID: "claude-desktop", Label: "Claude Desktop", Path: claudeDesktopConfigPath(), Config: serversJSON},
		{ID: "cursor", Label: "Cursor", Path: "~/.cursor/mcp.json", Config: serversJSON},
		{ID: "cline", Label: "Cline / Roo Code", Path: "~/.codeium/windsurf/mcp_config.json（同类客户端通用 mcpServers 格式）", Config: serversJSON},
		{ID: "vscode", Label: "VS Code (Copilot)", Path: ".vscode/mcp.json", Config: vsCodeJSON},
		{ID: "curl", Label: "curl 连通性验证", Config: curl},
	}
	return services.Ok(map[string]any{"running": true, "requireToken": require, "clients": clients})
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
	// 自检也是走 HTTP 的真实请求，开启校验后同样得带令牌，否则面板会先报 401，
	// 用户还以为是服务坏了。
	token := ""
	if rt := s.mcpRuntime(); rt != nil {
		if st := rt.TokenState(); st.RequireToken {
			token = st.Token
		}
	}
	start := time.Now()
	init, err := mcpRPC(ep, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+mcpsrv.ProtocolVersion+`","capabilities":{},"clientInfo":{"name":"quickdock-self-check","version":"1"}}}`, token)
	if err != nil {
		return services.FailMsg("握手失败: " + err.Error())
	}
	protocol, _ := init["protocolVersion"].(string)
	serverName, serverVersion := "", ""
	if info, ok := init["serverInfo"].(map[string]any); ok {
		serverName, _ = info["name"].(string)
		serverVersion, _ = info["version"].(string)
	}
	list, err := mcpRPC(ep, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, token)
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
func mcpRPC(endpoint, body, token string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func (r *toolRegistrar) add(run func(args map[string]any) (any, error), level int, name, desc string, required []string, props map[string]any, opts ...toolOption) {
	t := mcpsrv.Tool{
		Name:        name,
		Description: desc,
		InputSchema: mcpsrv.Schema("", required, props),
		Level:       level,
		Category:    r.category,
		Handler:     run,
	}
	for _, o := range opts {
		o(&t)
	}
	r.addTool(t)
}

// toolOption 给 add 附加审批元数据。
//
// 用可变参数而不是加一个 hooks 结构体：66 个既有注册点一行都不用动，
// 只有真正需要细粒度审批的那几个工具在末尾多写一行。审批元数据跟着工具走，
// 而不是堆在 ai_approval.go 里按名字 switch——后者每加一个工具就要回去改一次，
// 而且改的地方离工具定义很远，容易漏。
type toolOption func(*mcpsrv.Tool)

func withGrantKey(fn func(args map[string]any) (key string, rememberable bool)) toolOption {
	return func(t *mcpsrv.Tool) { t.GrantKey = fn }
}

func withRiskNote(fn func(args map[string]any) string) toolOption {
	return func(t *mcpsrv.Tool) { t.RiskNote = fn }
}

// withReady 声明「运行环境就绪才把这条工具挂给模型」。
//
// 用在依赖外部环境的工具上（file_search 要 Everything 在跑）：不就绪时工具直接从
// 本次装配里消失，模型看不到就不会去调——比「挂上去、调用时报错、模型再换个工具」
// 少一整轮往返，也少一条看着像 bug 的红色报错卡。
//
// ready 会在每轮装配时被调用，必须轻量：拿运行时那边带缓存的判定，别在这里现探。
func withReady(ready func() bool, notReadyNote func() string) toolOption {
	return func(t *mcpsrv.Tool) {
		t.Ready = ready
		t.NotReadyNote = notReadyNote
	}
}

func (r *toolRegistrar) addTool(t mcpsrv.Tool) {
	r.reg.Add(t)
}

// toolProvider 一个域的工具集合：分类 id + 注册函数。
type toolProvider struct {
	Category string
	Register func(s *MCPService, r *toolRegistrar)
}

// toolProviders 装配清单。加一个域 = 新建 tools_<域>.go + 在下面加一行。
//
// 顺序不影响 tools/list 的输出（registry 的 List/Catalog 都按等级+名称排序），
// 只决定 init 期注册先后，故按「宿主 → 环境 → 文件 → 启动台 → 内容 → 诊断 → 自动化 → 扩展 → 高危」排列。
func toolProviders() []toolProvider {
	return []toolProvider{
		{Category: mcpsrv.CategoryApp, Register: registerAppTools},             // 宿主信息、监听端口、机器人通知
		{Category: mcpsrv.CategoryEnv, Register: registerEnvTools},             // 运行时状态/日志/版本、服务启停
		{Category: mcpsrv.CategoryFile, Register: registerFileTools},           // 文件检索、读取、按内容搜索
		{Category: mcpsrv.CategoryItem, Register: registerItemTools},           // 快捷项、最近使用、工作空间
		{Category: mcpsrv.CategoryNote, Register: registerNoteTools},           // 笔记与文件夹
		{Category: mcpsrv.CategoryTodo, Register: registerTodoTools},           // 待办
		{Category: mcpsrv.CategoryClipboard, Register: registerClipboardTools}, // 剪贴板历史与写入
		{Category: mcpsrv.CategoryKnowledge, Register: registerKBTools},        // 本地文档知识库（只读）
		{Category: mcpsrv.CategorySSH, Register: registerSSHTools},             // SSH 主机（默认一台都不授权）
		{Category: mcpsrv.CategoryDownload, Register: registerDownloadTools},   // 下载器状态/列表/控制
		{Category: mcpsrv.CategoryDiag, Register: registerDiagTools},           // 日志与崩溃记录
		{Category: mcpsrv.CategorySchedule, Register: registerScheduleTools},   // 定时任务与执行历史
		{Category: mcpsrv.CategoryMonitor, Register: registerMonitorTools},     // 监控状态、检测记录
		{Category: mcpsrv.CategoryPlugin, Register: registerPluginTools},       // 插件清单与命令执行
		{Category: mcpsrv.CategoryRisk, Register: registerRiskTools},           // 高危：当前恒不开放
	}
}

// registerTools 按装配清单注册全部工具。
//
// 先注册域再注册工具：装配（Resolve）要用域声明判断场景开放程度，
// 域缺席时工具会退化成「未分类且全开」，界面上完全看不出问题。
func (s *MCPService) registerTools() {
	registerDomains()
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
