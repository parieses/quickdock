package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
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
	CategoryItem      = "item"
	CategoryNote      = "note"
	CategoryTodo      = "todo"
	CategoryClipboard = "clipboard"
	CategoryDiag      = "diag"
	CategoryPlugin    = "plugin"
	CategoryRisk      = "risk"
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
var Global Registrar = globalRegistrar{}

var (
	regMu    sync.RWMutex
	tools    = map[string]Tool{}
	maxLvl   = LevelWrite // 默认暴露「只读 + 低危写」
	disabled = map[string]bool{}
)

type globalRegistrar struct{}

// Add 写入注册表（同名覆盖）。应在服务初始化期调用。
func (globalRegistrar) Add(t Tool) {
	regMu.Lock()
	defer regMu.Unlock()
	tools[t.Name] = t
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

// List 返回当前允许暴露的工具（按名称排序，保证 tools/list 稳定）。
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
	// Exposed 等级与单工具开关都通过，客户端实际能看到。
	Exposed bool
}

// Catalog 返回全部已注册工具（含被等级挡住/被单独关掉的），按等级升序、
// 同级按名称排序，保证管理界面的展示顺序稳定。
func Catalog() []ToolInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]ToolInfo, 0, len(tools))
	for name, t := range tools {
		off := disabled[name]
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: t.Description,
			Level:       t.Level,
			Category:    t.Category,
			Disabled:    off,
			Exposed:     t.Level <= maxLvl && !off,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level < out[j].Level
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Call 执行工具。未注册、被单独关掉或超出等级限制时返回错误。
func Call(name string, args map[string]any) (any, error) {
	regMu.RLock()
	t, ok := tools[name]
	allowed := ok && t.Level <= maxLvl
	off := disabled[name]
	regMu.RUnlock()
	if !ok {
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

func ArgBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
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
