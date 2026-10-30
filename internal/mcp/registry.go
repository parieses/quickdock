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
	// CategoryWeb / CategoryCode 不属于 MCP：这批工具注册在 Agent 注册表里
	// （见 services/ai/tools），只在面板上与宿主工具一起展示，故分类 id 仍由
	// 这里统一维护——前端的分组顺序只认一个来源，否则面板会漏掉整组。
	CategoryWeb  = "web"
	CategoryCode = "code"
)

// Tool 一个可被 AI 调用的工具。
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Level       int
	// Category 所属域（见 Category* 常量），供面板分组；未填则视为未分类。
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
// 等级门与单工具开关是共用的（maxLvl / disabled 只有一份）：面板里关掉
// file_edit，内置 AI 也就改不了文件，不存在「关了但 AI 还能用」的第二套权限。
var Agent Registrar = agentRegistrar{}

var (
	regMu    sync.RWMutex
	tools    = map[string]Tool{}
	agents   = map[string]Tool{}
	maxLvl   = LevelWrite // 默认暴露「只读 + 低危写」
	disabled = map[string]bool{}
)

type globalRegistrar struct{}

// Add 写入对外注册表（同名覆盖）。应在服务初始化期调用。
func (globalRegistrar) Add(t Tool) {
	regMu.Lock()
	defer regMu.Unlock()
	tools[t.Name] = t
}

type agentRegistrar struct{}

// Add 写入 agent 注册表（同名覆盖）。
func (agentRegistrar) Add(t Tool) {
	regMu.Lock()
	defer regMu.Unlock()
	agents[t.Name] = t
}

// Register 注册工具，等价于 Global.Add（保留给既有调用方）。
func Register(t Tool) { Global.Add(t) }

// SetMaxLevel 设置可暴露的最高危险等级（默认 LevelWrite）。
func SetMaxLevel(level int) {
	regMu.Lock()
	defer regMu.Unlock()
	maxLvl = level
}

// MaxLevel 返回当前可暴露的最高危险等级。
func MaxLevel() int {
	regMu.RLock()
	defer regMu.RUnlock()
	return maxLvl
}

// ToolLevel 按名字取工具的危险等级（agent 注册表优先）。
//
// 存在的理由只有一个：审批闸门在 execOneToolCall 里只看得到工具名，而「要不要
// 停下来问用户」完全由等级决定。
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

// SetDisabled 全量替换「被单独关掉的工具」名单，并立即生效。
//
// 与等级门是两道独立闸：等级门按危险度批量裁剪（一档一刀），这里是对单个
// 工具的显式选择（「这条我就是不想让 AI 碰」）。两者都与工具是否注册无关——
// 注册表里始终是全量，裁剪只发生在 List/Call。
func SetDisabled(names []string) {
	regMu.Lock()
	defer regMu.Unlock()
	disabled = make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			disabled[n] = true
		}
	}
}

// Disabled 返回当前被单独关掉的工具名（拷贝，按名称排序，便于落盘稳定与展示）。
func Disabled() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(disabled))
	for name := range disabled {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// List 返回当前允许暴露给 MCP 客户端的工具（按名称排序，保证 tools/list 稳定）。
//
// 只读 Global：agent 注册表里的东西（抓网页、改文件、跑命令、看 git）不属于
// 软件自身能力，外部客户端本来就有自己的文件与终端工具，再暴露一遍只会让模型
// 在两套同名能力之间反复挑。
func List() []toolDefinition {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]toolDefinition, 0, len(tools))
	names := make([]string, 0, len(tools))
	for name, t := range tools {
		if t.Level > maxLvl || disabled[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := tools[name]
		out = append(out, toolDefinition{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.annotations(),
		})
	}
	return out
}

// AIFunction 一条给模型 function calling 用的工具规格。
//
// 只带 OpenAI tools[] 需要的三样：等级与开关在 ExposedFunctions 里已经应用过，
// 模型不该看到「有个工具但你没资格」——那只会让它反复尝试并浪费一轮 token。
// 与 toolDefinition 的区别：这个类型是导出的，供 services/ai 跨包使用。
type AIFunction struct {
	Name        string
	Description string
	Parameters  map[string]any
	// Category 所属域。tools[] 本身用不到它，但内置 AI 要按域决定
	// 「这个模式该不该看到这批工具」（宿主工具只在教程模式开放），
	// 否则只能按工具名硬编码白名单，加一个工具就要改一次。
	Category string
	// Level 危险等级。同样不进 tools[]，但子 agent 要按 role 再收窄一次
	// （explore 只读、code 可写）。等级门已经按用户配置裁过一遍，
	// 这里暴露的是「裁剪之后还剩下的工具各自多重」，供编排层二次过滤。
	Level int
	// AgentOnly 来自 agent 注册表。这类工具在所有模式下都可用——
	// 它们不描述宿主状态，不涉及「AI 会不会把我的环境改坏」，因此不需要
	// 用「只在教程模式挂」来兜底。
	AgentOnly bool
}

// ExposedFunctions 返回当前允许内置 AI 使用的工具规格，按名称排序。
//
// 两个注册表合并：Global（宿主能力）+ Agent（在目录里干活的工具）。
// 等级门与单工具开关对两边一样生效，所以用户在面板里关掉 file_edit，
// 内置 AI 也就改不了文件——权限只有一处真相。
//
// 排序是为了让 tools[] 的序列化结果稳定：它在每轮请求里都作为前缀出现，
// 顺序一变，上游的前缀缓存就全部落空。
func ExposedFunctions() []AIFunction {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]AIFunction, 0, len(tools)+len(agents))
	out = append(out, exposedFrom(tools, false)...)
	out = append(out, exposedFrom(agents, true)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// exposedFrom 把一个注册表里通过等级门与开关的工具转成 AIFunction。
// 调用方持锁。
func exposedFrom(set map[string]Tool, agent bool) []AIFunction {
	out := make([]AIFunction, 0, len(set))
	for name, t := range set {
		if t.Level > maxLvl || disabled[name] {
			continue
		}
		out = append(out, AIFunction{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			Category:    t.Category,
			Level:       t.Level,
			AgentOnly:   agent,
		})
	}
	return out
}

// ToolInfo 工具元信息，含危险等级、分类与「在当前配置下是否开放」。
//
// 管理界面需要看到全量工具而不是 List()：注册在高危档的工具确实存在，
// 只是被等级门挡住。只展示 List() 会让用户以为「能力就这么多」，
// 也会让库里那些高危工具的描述（写着「请去开启高危等级」）显得凭空消失。
type ToolInfo struct {
	Name        string
	Description string
	Level       int
	Category    string
	// Disabled 用户是否单独关掉了它：面板要能区分「关掉了」和「没资格」，
	// 这两者的补救动作不同（重新开启 vs 调等级）。
	Disabled bool
	// Exposed 等级与单工具开关都通过，且属于 Global 注册表——
	// 也就是 MCP 客户端实际能看到。agent 工具恒为 false。
	Exposed bool
	// AgentOnly 只给内置 AI 用。面板要把这类工具标出来，
	// 否则用户会以为「我在面板里关掉的东西，外部客户端也在用」。
	AgentOnly bool
}

// Catalog 返回全部已注册工具（两个注册表合并，含被等级挡住/被单独关掉的），
// 按等级升序、同级按名称排序，保证管理界面的展示顺序稳定。
func Catalog() []ToolInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]ToolInfo, 0, len(tools)+len(agents))
	for _, set := range []struct {
		m     map[string]Tool
		agent bool
	}{{tools, false}, {agents, true}} {
		for name, t := range set.m {
			off := disabled[name]
			out = append(out, ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				Level:       t.Level,
				Category:    t.Category,
				Disabled:    off,
				Exposed:     t.Level <= maxLvl && !off && !set.agent,
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

// Call 执行工具（MCP 客户端路径）。只看 Global 注册表。
func Call(name string, args map[string]any) (any, error) {
	return callTool(context.Background(), name, args, false)
}

// CallAgent 执行工具（内置 AI 路径）。先查 agent 注册表，再查 Global。
//
// 等级门与单工具开关照样生效；两条路径的差别只有「能看到哪些工具」。
func CallAgent(name string, args map[string]any) (any, error) {
	return callTool(context.Background(), name, args, true)
}

// CallAgentContext 同 CallAgent，但把 ctx 交给实现了 HandlerCtx 的工具。
//
// 只有「会跑长活儿且需要被取消」的工具才在乎它（当前是 spawn_agent）。
// 不使用 HandlerCtx 的工具行为完全不变——这正是保留两条执行路径的原因：
// 让 ctx 变成可选能力，而不是逼每个工具都改签名。
func CallAgentContext(ctx context.Context, name string, args map[string]any) (any, error) {
	return callTool(ctx, name, args, true)
}

func callTool(ctx context.Context, name string, args map[string]any, fromAgent bool) (any, error) {
	regMu.RLock()
	t, ok := tools[name]
	_, inAgent := agents[name]
	if fromAgent && !ok {
		t, ok = agents[name]
	}
	allowed := ok && t.Level <= maxLvl
	off := disabled[name]
	regMu.RUnlock()
	if !ok {
		// 名字在另一个注册表里时给一条能看懂的话：外部客户端猜 file_edit
		// 是很容易的事，回「未知工具」会让人以为是版本不对。
		if inAgent {
			return nil, fmt.Errorf("工具 %s 仅内置 AI 可用，不对外部 MCP 客户端开放", name)
		}
		return nil, fmt.Errorf("未知工具: %s", name)
	}
	if off {
		return nil, fmt.Errorf("工具 %s 已被关闭（可在环境管理 → MCP 面板重新开启）", name)
	}
	if !allowed {
		return nil, fmt.Errorf("工具 %s 属于高危操作，当前未开放（请在环境管理页开启）", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	if t.HandlerCtx != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return t.HandlerCtx(ctx, args)
	}
	if t.Handler == nil {
		// 注册了名称与 schema 却没有实现：返回明确错误而不是 panic，
		// 否则一次误注册会让整个对话流跟着崩。
		return nil, fmt.Errorf("工具 %s 没有实现", name)
	}
	return t.Handler(args)
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
