// Package mcp 暴露 MCP 服务给前端（环境管理页的启停/状态/工具清单），
// 并把 QuickDock 的业务能力注册为 MCP 工具，供 AI 客户端调用。
//
// 分层：internal/mcp 只管协议与传输（不含业务），本包负责把 AppService 的领域方法包装成工具。
// 危险等级：LevelRead 只读（默认全开）、LevelWrite 低危写（默认开）、LevelRisk 高危（默认不注册任何工具）。
package mcp

import (
	"errors"
	"fmt"
	"runtime"
	"strings"

	clipboardsvc "quickdock/services/clipboard"
	pluginsvc "quickdock/services/plugin"

	"quickdock/internal/db"
	envmgr "quickdock/internal/env"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/platform"
	"quickdock/services"
)

// builtinVersion MCP 在环境管理里的固定版本号（内置服务，无真实版本概念）
const builtinVersion = "builtin"

// MCPService 前端绑定服务：MCP 的启停、状态、工具清单与客户端配置。
type MCPService struct {
	App     *services.AppService
	Clip    *clipboardsvc.ClipboardService
	Plugin  *pluginsvc.PluginService
	version string
}

// NewMCPService 创建 MCP 绑定服务，并注册业务工具。
func NewMCPService(app *services.AppService, clip *clipboardsvc.ClipboardService, plugin *pluginsvc.PluginService, version string) *MCPService {
	s := &MCPService{App: app, Clip: clip, Plugin: plugin, version: version}
	s.registerTools()
	return s
}

// -------- 前端绑定方法 --------

// MCPStatus 返回 MCP 服务运行状态与监听地址。
func (s *MCPService) MCPStatus() *services.ApiResult {
	return services.Ok(map[string]any{
		"running":  mcpsrv.Default.Running(),
		"endpoint": mcpsrv.Default.Endpoint(),
		"port":     mcpsrv.Default.Port(),
		"maxLevel": mcpsrv.MaxLevel(),
		"tools":    len(mcpsrv.List()),
	})
}

// MCPTools 列出当前开放的工具（名称/说明/危险等级），供环境管理页展示。
func (s *MCPService) MCPTools() *services.ApiResult {
	list := mcpsrv.List()
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"level":       toolLevels[t.Name],
		})
	}
	return services.Ok(out)
}

// MCPStart 启动 MCP 服务（等同环境管理页点「启动」），返回监听地址。
func (s *MCPService) MCPStart() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Start(envmgr.RuntimeMCP, builtinVersion, nil); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(mcpsrv.Default.Endpoint())
}

// MCPStop 停止 MCP 服务。
func (s *MCPService) MCPStop() *services.ApiResult {
	if s.App.Env == nil {
		return services.FailMsg("env 未初始化")
	}
	if err := s.App.Env.Stop(envmgr.RuntimeMCP, builtinVersion); err != nil {
		return services.FailMsg(err.Error())
	}
	return services.Ok(nil)
}

// MCPSetLevel 设置可暴露的工具危险等级上限：0=只读，1=只读+低危写。改后需重启 MCP 服务生效。
func (s *MCPService) MCPSetLevel(level int) *services.ApiResult {
	if level < mcpsrv.LevelRead || level > mcpsrv.LevelWrite {
		return services.FailMsg("等级只能是 0（只读）或 1（只读+低危写）")
	}
	mcpsrv.SetMaxLevel(level)
	return services.Ok(mcpsrv.MaxLevel())
}

// MCPClientConfig 生成客户端配置片段（Claude Desktop 的 JSON 与 Claude Code 的 CLI 命令），
// 未运行时返回空串，由前端提示先启动。
func (s *MCPService) MCPClientConfig() *services.ApiResult {
	ep := mcpsrv.Default.Endpoint()
	if ep == "" {
		return services.Ok(map[string]any{"running": false, "json": "", "cli": ""})
	}
	cfg := fmt.Sprintf("{\n  \"mcpServers\": {\n    \"quickdock\": {\n      \"type\": \"http\",\n      \"url\": \"%s\"\n    }\n  }\n}", ep)
	cli := fmt.Sprintf("claude mcp add --transport http quickdock %s", ep)
	return services.Ok(map[string]any{"running": true, "url": ep, "json": cfg, "cli": cli})
}

// -------- 工具注册 --------

// toolLevels 记录每个工具的危险等级，仅供 MCPTools 展示（注册表内部已持有，避免重复维护两份列表）。
var toolLevels = map[string]int{}

func (s *MCPService) register(run func(args map[string]any) (any, error), level int, name, desc string, required []string, props map[string]any) {
	toolLevels[name] = level
	mcpsrv.Register(mcpsrv.Tool{
		Name:        name,
		Description: desc,
		InputSchema: mcpsrv.Schema("", required, props),
		Level:       level,
		Handler:     run,
	})
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

func (s *MCPService) registerTools() {
	const read = mcpsrv.LevelRead
	const write = mcpsrv.LevelWrite
	str := mcpsrv.Str
	intp := mcpsrv.Int

	// ---- 只读 ----

	s.register(func(args map[string]any) (any, error) {
		return map[string]any{
			"app":        "QuickDock",
			"version":    s.version,
			"platform":   runtime.GOOS + "/" + runtime.GOARCH,
			"dataDir":    platform.DefaultDataDir(),
			"mcpRunning": mcpsrv.Default.Running(),
			"mcpUrl":     mcpsrv.Default.Endpoint(),
			"tools":      len(mcpsrv.List()),
		}, nil
	}, read, "app_info", "QuickDock 基本信息：版本、平台、数据目录、MCP 服务地址与已开放工具数", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		list := s.App.Env.List()
		out := make([]map[string]any, 0, len(list))
		for _, r := range list {
			versions := make([]string, 0, len(r.Installed))
			for _, ins := range r.Installed {
				versions = append(versions, ins.Version)
			}
			row := map[string]any{
				"id":         r.ID,
				"name":       r.Name,
				"group":      r.Group,
				"hasService": r.HasService,
				"enabled":    r.Enabled,
				"versions":   versions,
			}
			if r.HasService && len(versions) > 0 {
				if st, err := s.App.Env.Status(envmgr.Runtime(r.ID), versions[0]); err == nil {
					row["running"] = st.Running
					row["ports"] = st.Ports
				}
			}
			out = append(out, row)
		}
		return out, nil
	}, read, "env_list", "列出所有受管运行时：已装版本、是否支持服务启停、当前运行状态与端口", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "runtime")
		if id == "" {
			return nil, errors.New("缺少参数 runtime")
		}
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		st, err := s.App.Env.Status(envmgr.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")))
		if err != nil {
			return nil, err
		}
		return st, nil
	}, read, "env_status", "查询某运行时的运行状态（是否运行、PID、端口、版本）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id，如 node / php / redis / nginx / mcp"), "version": str("版本号，省略则用已装的第一个版本")})

	s.register(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		log, err := s.App.Env.LogGet(envmgr.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")))
		if err != nil {
			return nil, err
		}
		if log == "" {
			return "（无日志）", nil
		}
		return log, nil
	}, read, "env_log", "读取某运行时的服务日志", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})

	s.register(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		return s.App.Env.AvailableVersions(envmgr.Runtime(mcpsrv.Arg(args, "runtime"))), nil
	}, read, "env_versions", "列出某运行时可下载的版本（需联网，慢）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id")})

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.ListWorkspaces())
	}, read, "workspace_list", "列出所有工作空间", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		q := mcpsrv.Arg(args, "query")
		if q == "" {
			return unwrap(s.App.ListAllItems())
		}
		return unwrap(s.App.SearchAll(q))
	}, read, "item_search", "按名称/路径关键词搜索快捷项（项目、命令、目录、网址等），留空返回全部", nil,
		map[string]any{"query": str("关键词，留空返回全部")})

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.GetRecentUsage(mcpsrv.ArgInt(args, "limit", 10)))
	}, read, "recent_items", "列出最近使用过的快捷项", nil,
		map[string]any{"limit": intp("返回条数，默认 10")})

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.ListTodos())
	}, read, "todo_list", "列出所有待办", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.SearchNotesTree(mcpsrv.Arg(args, "query")))
	}, read, "note_search", "搜索笔记（标题与正文）", []string{"query"},
		map[string]any{"query": str("搜索关键词")})

	s.register(func(args map[string]any) (any, error) {
		if s.Clip == nil {
			return nil, errors.New("剪贴板服务未就绪")
		}
		return unwrap(s.Clip.ListClipboardEntries(mcpsrv.ArgInt(args, "limit", 20)))
	}, read, "clipboard_recent", "列出最近的剪贴板历史（文本条目）", nil,
		map[string]any{"limit": intp("返回条数，默认 20")})

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.ListListeningPorts())
	}, read, "port_list", "列出本机正在监听的端口与对应进程", nil, nil)

	// ---- 低危写 ----

	s.register(func(args map[string]any) (any, error) {
		return s.envStart(mcpsrv.Arg(args, "runtime"), mcpsrv.Arg(args, "version"))
	}, write, "env_start", "启动某运行时的服务（如 nginx / redis / mysql / mcp）。已是运行状态会直接返回成功", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("要启动的版本，省略则用已装的第一个版本")})

	s.register(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		if err := s.App.Env.Stop(envmgr.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version"))); err != nil {
			return nil, err
		}
		return "已停止 " + id, nil
	}, write, "env_stop", "停止某运行时的服务", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})

	s.register(func(args map[string]any) (any, error) {
		if s.App.Env == nil {
			return nil, errors.New("环境管理未初始化")
		}
		id := mcpsrv.Arg(args, "runtime")
		if err := s.App.Env.Restart(envmgr.Runtime(id), pickVersion(s.App.Env, id, mcpsrv.Arg(args, "version")), nil); err != nil {
			return nil, err
		}
		return "已重启 " + id, nil
	}, write, "env_restart", "重启某运行时的服务（改配置后常用）", []string{"runtime"},
		map[string]any{"runtime": str("运行时 id"), "version": str("版本号，省略则用已装的第一个版本")})

	s.register(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 item_search 获取）")
		}
		items, err := s.allItems()
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.ID == id {
				if r := s.App.OpenItem(it); r != nil && r.Code != 0 {
					return nil, errors.New(r.Msg)
				}
				return "已打开 " + it.Name, nil
			}
		}
		return nil, errors.New("未找到该快捷项: " + id)
	}, write, "item_open", "打开某个快捷项（项目/命令/目录），先用 item_search 找到 id", []string{"id"},
		map[string]any{"id": str("快捷项 id")})

	s.register(func(args map[string]any) (any, error) {
		title := mcpsrv.Arg(args, "title")
		if title == "" {
			return nil, errors.New("缺少参数 title")
		}
		return unwrap(s.App.CreateTodo(title, mcpsrv.Arg(args, "priority"), mcpsrv.Arg(args, "dueDate"),
			mcpsrv.Arg(args, "note"), "", "", "", "", ""))
	}, write, "todo_create", "新建待办", []string{"title"},
		map[string]any{
			"title":    str("待办标题"),
			"priority": str("优先级：low / normal / high，留空 normal"),
			"dueDate":  str("截止日期，格式 YYYY-MM-DD，可留空"),
			"note":     str("备注，可留空"),
		})

	s.register(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 todo_list 获取）")
		}
		return unwrap(s.App.SetTodoStatus(id, "done"))
	}, write, "todo_done", "把某待办标记为已完成", []string{"id"},
		map[string]any{"id": str("待办 id")})

	s.register(func(args map[string]any) (any, error) {
		if s.Clip == nil {
			return nil, errors.New("剪贴板服务未就绪")
		}
		text := mcpsrv.Arg(args, "text")
		if text == "" {
			return nil, errors.New("缺少参数 text")
		}
		if r := s.Clip.CopyText(text); r != nil && r.Code != 0 {
			return nil, errors.New(r.Msg)
		}
		return "已写入剪贴板", nil
	}, write, "clipboard_copy", "把文本写入系统剪贴板", []string{"text"},
		map[string]any{"text": str("要写入的文本")})

	// ---- P0 写入闭环：笔记（让 AI 能把产出塞回 QuickDock）----

	s.register(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			return nil, errors.New("缺少参数 name")
		}
		return unwrap(s.App.CreateNoteDoc(
			mcpsrv.Arg(args, "parentId"),
			name,
			mcpsrv.Arg(args, "content"),
			mcpsrv.Arg(args, "format"),
		))
	}, write, "note_create", "新建一篇笔记文档（markdown/text）。parentId 留空则创建到根目录", []string{"name"},
		map[string]any{
			"name":     str("笔记标题"),
			"content":  str("正文内容，可留空"),
			"format":   str("渲染格式：markdown 或 text，默认 markdown"),
			"parentId": str("父目录 id，留空则放到根目录"),
		})

	s.register(func(args map[string]any) (any, error) {
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 note_search 获取）")
		}
		return unwrap(s.App.UpdateNoteDoc(id, mcpsrv.Arg(args, "content"), mcpsrv.Arg(args, "tags")))
	}, write, "note_update", "更新笔记正文与标签。tags 为 JSON 数组字符串（如 [\"a\",\"b\"]），留空则清空", []string{"id"},
		map[string]any{
			"id":      str("笔记 id"),
			"content": str("新的正文内容，可留空"),
			"tags":    str("标签，JSON 数组字符串，留空清空"),
		})

	s.register(func(args map[string]any) (any, error) {
		content := mcpsrv.Arg(args, "content")
		if content == "" {
			return nil, errors.New("缺少参数 content")
		}
		return unwrap(s.App.SaveNote(content))
	}, write, "note_quick", "把一段文本写入固定「快捷笔记」（覆盖式 upsert）。适合让 AI 把产出直接塞回 QuickDock 当草稿", []string{"content"},
		map[string]any{"content": str("要保存的文本内容")})

	// ---- P1 诊断日志（对话式排障）----

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.ListLogFiles())
	}, read, "log_list", "列出应用日志文件（含 crash/ 子目录的崩溃记录），返回文件名、大小、修改时间", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			return nil, errors.New("缺少参数 name（用 log_list 获取）")
		}
		return unwrap(s.App.ReadLogFile(name, mcpsrv.ArgInt(args, "tail", 0)))
	}, read, "log_read", "读取日志文件内容；tail>0 只返回末尾 N 行，适合「给我最后 200 行」式排障", []string{"name"},
		map[string]any{
			"name": str("日志文件名，如 quickdock-2026-09-09.log 或 crash/panic-xxx.log"),
			"tail": intp("只取末尾 N 行，0 表示全文"),
		})

	s.register(func(args map[string]any) (any, error) {
		return unwrap(s.App.ListCrashFiles())
	}, read, "crash_list", "列出崩溃/异常记录（Go panic 与前端 JS 异常），新的在前", nil, nil)

	s.register(func(args map[string]any) (any, error) {
		name := mcpsrv.Arg(args, "name")
		if name == "" {
			return nil, errors.New("缺少参数 name（用 crash_list 获取）")
		}
		return unwrap(s.App.ReadCrashFile(name))
	}, read, "crash_read", "读取单个崩溃文件内容（供 AI 分析 panic/JS 异常栈）", []string{"name"},
		map[string]any{"name": str("崩溃文件名")})

	// ---- 插件清单（让 AI 知道可调用哪些插件命令）----

	s.register(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		return unwrap(s.Plugin.ListPlugins())
	}, read, "plugin_list", "列出已安装插件及其命令、后端运行时（native/goja/none）。让 AI 知道能进一步调用哪个命令", nil, nil)

	// ---- 高危（LevelRisk：默认 maxLvl=LevelWrite 不暴露，需在环境管理页开启高危等级）----

	s.register(func(args map[string]any) (any, error) {
		pid := mcpsrv.ArgInt(args, "pid", 0)
		if pid <= 0 {
			return nil, errors.New("缺少或无效的参数 pid")
		}
		return unwrap(s.App.KillProcess(pid))
	}, mcpsrv.LevelRisk, "process_kill", "结束指定 PID 的进程（高危：误杀会导致数据丢失）。需先在环境管理页开启高危等级", []string{"pid"},
		map[string]any{"pid": intp("要结束的进程 PID")})

	s.register(func(args map[string]any) (any, error) {
		cmd := mcpsrv.Arg(args, "cmd")
		if cmd == "" {
			return nil, errors.New("缺少参数 cmd")
		}
		return unwrap(s.App.ExecuteSystemCommand(cmd))
	}, mcpsrv.LevelRisk, "system_command", "执行系统指令（lock/shutdown/restart/sleep/emptytrash，高危）。需先在环境管理页开启高危等级", []string{"cmd"},
		map[string]any{"cmd": str("系统指令，如 shutdown / restart / lock / sleep / emptytrash")})
}

// envStart 启动服务；已在运行时不重复拉起（避免端口冲突检测把自身占用误判为冲突）。
func (s *MCPService) envStart(id, version string) (any, error) {
	if s.App.Env == nil {
		return nil, errors.New("环境管理未初始化")
	}
	ver := pickVersion(s.App.Env, id, version)
	if st, err := s.App.Env.Status(envmgr.Runtime(id), ver); err == nil && st.Running {
		return id + " 已在运行（端口 " + strings.Trim(fmt.Sprint(st.Ports), "[]") + "）", nil
	}
	if err := s.App.Env.Start(envmgr.Runtime(id), ver, nil); err != nil {
		return nil, err
	}
	st, _ := s.App.Env.Status(envmgr.Runtime(id), ver)
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
func pickVersion(m *envmgr.Manager, id, version string) string {
	if version != "" {
		return version
	}
	if installs, err := m.InstalledVersions(envmgr.Runtime(id)); err == nil && len(installs) > 0 {
		return installs[0].Version
	}
	return ""
}
