package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Level 工具危险等级，用于按用户配置裁剪暴露面。
const (
	LevelRead  = 0 // 只读：列表/查询/状态/日志
	LevelWrite = 1 // 低危写：服务启停、建待办、写剪贴板、打开工作空间
	LevelRisk  = 2 // 高危：任意命令、杀进程、删除数据、恢复备份、重启应用
)

// 工具分类：纯展示与命名空间用途，与危险等级是两条正交的轴。
// 等级管「能不能调」，分类管「看起来属于谁」——面板按分类分组，
// 分类 id 同时也是将来按域批量开关的作用域依据。
const (
	CategoryApp       = "app"
	CategoryEnv       = "env"
	CategoryFile      = "file"
	CategoryItem      = "item"
	CategoryNote      = "note"
	CategoryTodo      = "todo"
	CategoryClipboard = "clipboard"
	CategoryDiag      = "diag"
	CategorySchedule  = "schedule"
	CategoryMonitor   = "monitor"
	CategoryPlugin    = "plugin"
	CategoryRisk      = "risk"
	// CategorySSH 远程主机：会话与文件的读、以及受限的命令执行。
	// 单独成域是因为它有一条别的工具都没有的边界——主机级授权
	// （只有用户显式打开的主机才对 Agent 可见），不能混进通用的 file/env 分组。
	CategorySSH = "ssh"
	// CategoryKnowledge 本地文档知识库：按内容检索，只读。
	CategoryKnowledge = "knowledge"
	// CategoryDownload 下载器（aria2）任务：状态/列表/详情与控制已存在的任务。
	// 单独成域是为了让「建任务」这类写操作能按域收窄等级，不牵连只读查询。
	CategoryDownload = "download"
	// CategoryWeb / CategoryCode 不属于 MCP：这批工具注册在 Agent 注册表里
	// （见 services/ai/tools），只在面板上与宿主工具一起展示，故分类 id 仍由
	// 这里统一维护——前端的分组顺序只认一个来源，否则面板会漏掉整组。
	CategoryWeb  = "web"
	CategoryCode = "code"
	// CategoryExternal 外部 MCP server 转发进来的工具。单独一类：
	// 「别人写的 server」与宿主自带工具的信任级别不同，面板上要能一眼分开。
	CategoryExternal = "external"
)

// Tool 一个可被 AI 调用的工具。
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Level       int
	// Domain 所属域（见 services/mcp/catalog.go 的域定义），决定面板分组、
	// 场景开放程度与提示词段落。未填时 Add 会用 Category 补。
	Domain string
	// Category 历史字段：与 Domain 同值，保留给还没迁移的读取方。
	// 新代码一律读 Domain——一个字段背「分组 + 可见性开关」两份语义，
	// 正是上一轮每加一个域要改五处的根源。
	Category string
	// Handler 执行工具。返回 string 直接作为文本结果，其它类型经 JSON 序列化。
	Handler func(args map[string]any) (any, error)
	// HandlerCtx 带 ctx 的实现，优先于 Handler。
	//
	// 为什么需要它：绝大多数工具是「一问一答」的快操作，给不给 ctx 都一样；
	// 但有一类工具会起一段长活儿（spawn_agent 起子 agent），它必须在用户点
	// 停止时能被掐掉——否则主对话停了，后台还在烧 token，而界面上什么都看不到。
	//
	// 不用「把 ctx 塞进 args」代替：args 是模型可见/可伪造的 JSON，取消信号
	// 混在里面既不合协议，也会被工具卡片序列化显示出来。
	HandlerCtx func(ctx context.Context, args map[string]any) (any, error)
	// GrantKey 可选：「本次会话都允许」记在哪个 key 上、给不给记住按钮。
	//
	// 默认按工具名记忆。声明它让「记住」不越界：run_command 按命令族、
	// schedule_create 按任务名——否则用户批准一次「建这个巡检任务」，
	// 之后任意任务都能在无人过问的情况下被建出来。
	GrantKey func(args map[string]any) (key string, rememberable bool)
	// RiskNote 可选：审批卡上的风险说明。默认按等级给一句通用的。
	RiskNote func(args map[string]any) string
	// ArgBrief 可选：审批卡上的参数摘要。默认取 path / query 之类。
	ArgBrief func(args map[string]any) string
}

// normalize 补齐域字段。放在注册处而不是各个读取方：
// 读取方有三个（面板、装配、协议层），补三遍迟早漏一处。
func (t *Tool) normalize() {
	if t.Domain == "" {
		t.Domain = t.Category
	}
	if t.Category == "" {
		t.Category = t.Domain
	}
}

// annotations 生成 MCP 规范的 tool annotations：让客户端在弹确认框时知道
// 这次调用会不会改东西。由 Level 推导而非各工具手填——手填迟早会和 Level
// 打架（写操作标成 readOnly），客户端据此跳过确认就等于绕过了等级门。
func (t Tool) annotations() map[string]any {
	return map[string]any{
		"readOnlyHint":    t.Level == LevelRead,
		"destructiveHint": t.Level >= LevelRisk,
	}
}

// Registrar 工具注册入口。业务层只依赖这个接口，不直接摸全局变量：
// 将来要「一个进程跑多个 MCP 实例」或给单测塞独立注册表时，不必改业务代码。
type Registrar interface {
	Add(t Tool)
}

// Global 进程内唯一注册表，由 Default 服务与面板共用。
//
// 里面只放QuickDock 自身能力（环境、文件、快捷项、笔记、待办、剪贴板、诊断、
// 定时、监控、插件）。这是对外契约的一部分：客户端接上我们的 MCP，拿到的应该
// 是「这个软件能干什么」，而不是又一套通用文件读写与终端。
var Global Registrar = globalRegistrar{}

// Agent 内置 AI 专用工具的注册表（实现在 services/ai/tools）。
//
// 为什么是另一个注册表而不是同一张表加个标志位：这两批工具的归属不同。
// Global 的内容是产品对外的能力清单，Agent 的内容是「AI 和你在某个目录里干活时
// 顺手要用的东西」（抓网页、读目录、看 git、改文件、跑命令）。放在一起时，
// 「新增一个 agent 工具」和「新增一个对外工具」长得一模一样，迟早有人把写文件的
// 能力顺手注册进对外清单；分成两个注册表后，注册处写 Global 还是 Agent 这件事本身
// 就说明了归属。
//
// 两个注册表共用同一份**定义**（名字、等级、schema、Handler），但暴露策略分开：
// 外部 MCP 客户端一份（mcpPolicy，存在 MCP 配置里），内置 AI 一份（ai_tool_policy，
// 存在 AI 设置里）。面板里关掉 file_edit 只影响外部客户端，不会连带把 AI 的手也绑上——
// 这是用户明确要求的边界，别再改回「一处开关管两边」。
var Agent Registrar = agentRegistrar{}

var (
	regMu  sync.RWMutex
	tools  = map[string]Tool{}
	agents = map[string]Tool{}
	// mcpPolicy 外部 MCP 客户端的暴露策略（等级上限 + 单工具开关）。
	//
	// 只是**其中一份**：内置 AI 那份由 services/ai 读设置后通过 Scope.Policy 传进
	// Resolve，从不读写这里。两份策略各自落盘、各自界面，互不影响。
	mcpPolicy = NewPolicy(LevelWrite, nil)
)

type globalRegistrar struct{}

// Add 写入对外注册表（同名覆盖）。应在服务初始化期调用。
func (globalRegistrar) Add(t Tool) {
	if strings.TrimSpace(t.Name) == "" {
		return
	}
	t.normalize()
	regMu.Lock()
	defer regMu.Unlock()
	tools[t.Name] = t
}

type agentRegistrar struct{}

// Add 写入 agent 注册表（同名覆盖）。
func (agentRegistrar) Add(t Tool) {
	if strings.TrimSpace(t.Name) == "" {
		return
	}
	t.normalize()
	regMu.Lock()
	defer regMu.Unlock()
	agents[t.Name] = t
}

// Register 注册工具，等价于 Global.Add（保留给既有调用方）。
func Register(t Tool) { Global.Add(t) }

// RemoveExternal 移除全部外部 MCP 工具（名字以 prefix 开头），返回移除的数量。
//
// 只服务于一个场景：外部 server 的清单重载——server 的工具列表变了（加了的、
// 删了的），逐个 diff 远不如整组撤掉重注来得可靠；同名覆盖式 Add 又清不掉
// 已经消失的工具，残留的 Handler 还指向旧 client。单工具开关（disabled）一并
// 清掉：开关跟着工具走，工具没了开关留着只会让面板出现幽灵行。
//
// 名字以 prefix 开头才动：prefix 是 "mcp_<server>_" 这种带尾分隔符的完整前缀，
// 不会误伤同名的内置工具——那是权限边界，宁可靠调用方传完整前缀。
func RemoveExternal(prefix string) int {
	regMu.Lock()
	defer regMu.Unlock()
	n := 0
	for name := range tools {
		if strings.HasPrefix(name, prefix) {
			delete(tools, name)
			n++
		}
	}
	return n
}

// SetMaxLevel 设置外部 MCP 客户端的等级上限（不影响内置 AI）。
func SetMaxLevel(level int) {
	regMu.Lock()
	defer regMu.Unlock()
	mcpPolicy = NewPolicy(level, mcpPolicy.DisabledNames())
}

// MaxLevel 返回外部 MCP 客户端的等级上限。
func MaxLevel() int {
	regMu.RLock()
	defer regMu.RUnlock()
	return mcpPolicy.MaxLevel
}

// SetMCPPolicy 整份替换外部 MCP 策略（启动时从 config.json 读回）。
func SetMCPPolicy(p Policy) {
	regMu.Lock()
	defer regMu.Unlock()
	mcpPolicy = NewPolicy(p.MaxLevel, p.DisabledNames())
}

// MCPPolicy 当前外部 MCP 策略的拷贝（装配时传进 Scope.Policy）。
func MCPPolicy() Policy {
	regMu.RLock()
	defer regMu.RUnlock()
	return NewPolicy(mcpPolicy.MaxLevel, mcpPolicy.DisabledNames())
}

// ToolLevel 按名字取工具的危险等级（agent 注册表优先）。
//
// 存在的理由只有一个：审批闸门与去重逻辑只看得到工具名，而「要不要停下来问用户」
// 完全由等级决定。
//
// 查不到时返回 LevelRisk 而不是 LevelRead：说明名字写错或工具刚被移除，按最保守的
// 算——少问一次的代价（一条没被拦下的写操作）与多问一次的代价（多点一下）不对称。
func ToolLevel(name string) int {
	regMu.RLock()
	defer regMu.RUnlock()
	if t, ok := agents[name]; ok {
		return t.Level
	}
	if t, ok := tools[name]; ok {
		return t.Level
	}
	return LevelRisk
}

// ToolDomain 按名字取工具所属域（找不到返回空串）。
func ToolDomain(name string) string {
	regMu.RLock()
	defer regMu.RUnlock()
	if t, ok := agents[name]; ok {
		return t.Domain
	}
	if t, ok := tools[name]; ok {
		return t.Domain
	}
	return ""
}

// ToolByName 按名字取工具定义（agent 注册表优先）。
//
// 审批链路只有工具名，而「记住的粒度」「卡片上写什么风险」都声明在工具上——
// 没有这个入口，审批代码就只能继续按名字 switch，新增工具时又要回来改一次。
func ToolByName(name string) (Tool, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if t, ok := agents[name]; ok {
		return t, true
	}
	t, ok := tools[name]
	return t, ok
}

// SetDisabled 全量替换「被外部 MCP 单独关掉的工具」名单（不影响内置 AI）。
//
// 与等级门是两道独立闸：等级门按危险度批量裁剪（一档一刀），这里是对单个
// 工具的显式选择（「这条我就是不想让外部客户端碰」）。
func SetDisabled(names []string) {
	regMu.Lock()
	defer regMu.Unlock()
	mcpPolicy = NewPolicy(mcpPolicy.MaxLevel, names)
}

// Disabled 返回当前被外部 MCP 单独关掉的工具名（拷贝，按名称排序，便于落盘稳定与展示）。
func Disabled() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return mcpPolicy.DisabledNames()
}

// List 返回当前允许暴露给 MCP 客户端的工具（按名称排序，保证 tools/list 稳定）。
//
// 可见性由 Resolve 一处决定：策略（等级/开关）+ 域的 External 声明。以前这里是
// 「遍历 tools 再判一次等级」，与 AI 侧各有一套过滤，加一个域要记住改两处。
func List() []toolDefinition {
	sf := Resolve(ToolScope{Caller: CallerMCP, Policy: MCPPolicy()})
	out := make([]toolDefinition, 0, len(sf.Tools))
	for _, t := range sf.Tools {
		out = append(out, toolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.annotations(),
		})
	}
	return out
}

// ToolInfo 工具元信息，含危险等级、所属域与「在当前配置下是否开放」。
//
// 管理界面需要看到全量工具而不是 List()：注册在高危档的工具确实存在，
// 只是被等级门挡住。只展示 List() 会让用户以为「能力就这么多」，
// 也会让库里那些高危工具的描述（写着「请去开启高危等级」）显得凭空消失。
type ToolInfo struct {
	Name        string
	Description string
	Level       int
	// Domain 所属域（面板分组与场景策略的依据）。
	Domain string
	// Disabled 是否被外部 MCP 策略单独关掉。面板要能区分「关掉了」和「没资格」，
	// 这两者的补救动作不同（重新开启 vs 调等级）。
	Disabled bool
	// Exposed 等级与单工具开关都通过，且该域对外——也就是 MCP 客户端实际能看到。
	// agent 工具恒为 false。
	Exposed bool
	// AgentOnly 只给内置 AI 用。面板要把这类工具标出来，
	// 否则用户会以为「我在面板里关掉的东西，外部客户端也在用」。
	AgentOnly bool
}

// Catalog 返回全部已注册工具的清单，Disabled/Exposed 按**外部 MCP 侧**策略算。
//
// 内置 AI 的开关是另一份策略：那边的界面用 CatalogWith(aiToolPolicy)，
// 两份策略互不影响，不能在这里合成一个字段。
func Catalog() []ToolInfo {
	return CatalogWith(MCPPolicy())
}

// CatalogWith 用给定策略计算 Disabled/Exposed，供 AI 侧面板复用同一份清单。
//
// 之所以要参数而不是读全局：AI 设置页要显示「AI 侧这个工具开没开」，
// 读 MCP 全局策略会答错——那正是这次要修掉的串台。
func CatalogWith(pol Policy) []ToolInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]ToolInfo, 0, len(tools)+len(agents))
	for _, set := range []struct {
		m     map[string]Tool
		agent bool
	}{{tools, false}, {agents, true}} {
		for _, t := range set.m {
			off := pol.Disabled[t.Name]
			out = append(out, ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				Level:       t.Level,
				Domain:      t.Domain,
				Disabled:    off,
				Exposed:     t.Level <= pol.MaxLevel && !off && !set.agent && DomainExposed(t.Domain),
				AgentOnly:   set.agent,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level < out[j].Level
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Call 执行工具（外部 MCP 客户端路径）。
//
// 装配与执行都走统一入口：Resolve 用 MCP 自己的策略决定看得到什么，Invoke 用同一份
// Surface 判成员，再执行。内置 AI 不经过这里——它用自己那套策略，见 services/ai。
func Call(name string, args map[string]any) (any, error) {
	sf := Resolve(ToolScope{Caller: CallerMCP, Policy: MCPPolicy()})
	return Invoke(context.Background(), sf, name, args, nil)
}

// -------- JSON Schema 快捷构造 --------

// Schema 构造 inputSchema。props 形如 {"runtime": Str("运行时 id，如 node")}。
// props 为 nil 时必须输出空对象 {}：JSON Schema 不允许 "properties": null，
// 严格的 MCP 客户端会因此判定整个 tools/list 非法。
func Schema(desc string, required []string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if desc != "" {
		s["description"] = desc
	}
	return s
}

// Str / Int / Bool 属性快捷定义
func Str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func Int(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func Bool(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func Num(desc string) map[string]any  { return map[string]any{"type": "number", "description": desc} }
func Any(desc string) map[string]any  { return map[string]any{"description": desc} }
func StrArr(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// StrOrObj 声明「字符串或 JSON 对象」二选一的属性。
//
// 不用 Any（只写 description、不给 type）是有意的：多数客户端会把它当成"随便传"，
// 于是模型容易把结构化参数写成一段 JSON 文本塞进字符串里，到了宿主侧才发现解析不了。
// anyOf 让两端对形状有共识。
func StrOrObj(desc string) map[string]any {
	return map[string]any{
		"description": desc,
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "object"},
		},
	}
}

// Arg 从参数里取字符串/整数/布尔，缺失返回零值。
func Arg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func ArgInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// ArgBool 从参数里取布尔，缺失返回 false。
func ArgBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

// Cwd 取宿主注入的会话工作目录。
//
// 这个键由 AI 服务在调工具前塞进参数（见 services/ai 的 runToolCalls），
// 不在任何工具的 schema 里——模型看不见它，也就无从伪造；即使它自己塞一个
// _cwd，也会被宿主覆盖。MCP 客户端与手动调用时该键不存在，走原有的绝对路径逻辑。
func Cwd(args map[string]any) string {
	return strings.TrimSpace(Arg(args, "_cwd"))
}

// Scope 取宿主注入的「读取范围」，为空表示不限制（走全局读白名单）。
//
// 与 Cwd 分开是因为两者不同：Cwd 是「干活的地方」（相对路径补全、写操作的默认目录），
// 它总是一个真实目录；Scope 是「这次对话被允许看的范围」，只在 cwd 是用户亲手选的
// 项目目录时才存在——系统给无目录任务自动开的 ai_tasks/<id> 不是项目，
// 拿它当范围会让普通聊天连一个文件都读不到。
//
// 同样由 AI 服务注入、同样不在任何工具的 schema 里；MCP 客户端调用时不存在，
// 于是对外部客户端保持原有的宽松行为。
func Scope(args map[string]any) string {
	return strings.TrimSpace(Arg(args, "_scope"))
}

// Allowed 取「用户当场批准过的额外目录」，只对本次调用有效。
//
// 与 Scope 的关系：Scope 是这次对话声明的范围，Allowed 是本次调用被单独放行的一次例外
// （用户确认卡上点了允许）。宿主在审批通过后注入，工具执行完随参数一起丢掉——
// 一次同意不等于永久开放，否则「只读这一次」会悄悄变成「以后都能读」。
func Allowed(args map[string]any) []string {
	switch v := args["_allow"].(type) {
	case []string:
		return v
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// PathArg 取路径参数，若给的是相对路径则按会话工作目录补全；不做白名单校验。
//
// 单独抽出来是因为「补全」与「校验」是两件事：fsguard 负责后者，
// 而有些地方（如 run_command 的 cwd）需要先补全才能判它是不是个目录。
func PathArg(args map[string]any, key string) string {
	p := strings.TrimSpace(Arg(args, key))
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return p
	}
	if root := Cwd(args); root != "" {
		return filepath.Join(root, p)
	}
	return p
}

// JSON 把任意值序列化为缩进 JSON 文本（工具结果统一走文本块）。
func JSON(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return "ok"
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
