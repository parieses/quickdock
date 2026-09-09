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

// Tool 一个可被 AI 调用的工具。
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Level       int
	// Handler 执行工具。返回 string 直接作为文本结果，其它类型经 JSON 序列化。
	Handler func(args map[string]any) (any, error)
}

var (
	regMu  sync.RWMutex
	tools  = map[string]Tool{}
	maxLvl = LevelWrite // 默认暴露「只读 + 低危写」
)

// Register 注册工具（同名覆盖）。应在服务初始化期调用，非并发安全场景亦可。
func Register(t Tool) {
	regMu.Lock()
	defer regMu.Unlock()
	tools[t.Name] = t
}

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

// List 返回当前允许暴露的工具（按名称排序，保证 tools/list 稳定）。
func List() []toolDefinition {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]toolDefinition, 0, len(tools))
	names := make([]string, 0, len(tools))
	for name, t := range tools {
		if t.Level > maxLvl {
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
		})
	}
	return out
}

// Call 执行工具。未注册或超出等级限制时返回错误。
func Call(name string, args map[string]any) (any, error) {
	regMu.RLock()
	t, ok := tools[name]
	allowed := ok && t.Level <= maxLvl
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("未知工具: %s", name)
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
func Schema(desc string, required []string, props map[string]any) map[string]any {
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
func Str(desc string) map[string]any   { return map[string]any{"type": "string", "description": desc} }
func Int(desc string) map[string]any   { return map[string]any{"type": "integer", "description": desc} }
func Bool(desc string) map[string]any  { return map[string]any{"type": "boolean", "description": desc} }
func Num(desc string) map[string]any   { return map[string]any{"type": "number", "description": desc} }
func Any(desc string) map[string]any   { return map[string]any{"description": desc} }
func StrArr(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
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
