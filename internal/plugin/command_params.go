package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// 命令参数声明（commands[].params）的加载期处理。
//
// 为什么宿主只做「浅校验 + 丢弃」而不是完整 JSON Schema 校验：
//   - 宿主不引 JSON Schema 库，完整校验要把 spec 的一大半搬进来，收益却只是
//     「报错更准确」——真正该拦住错误写法的地方是插件仓库的打包前检查（check_plugins.py）。
//   - 但也不能盲信：一份写歪的 schema 会经 plugin_list 直接进模型上下文，
//     模型照着一个不存在的键填参数，表现为「AI 调了没反应」，比没有声明更糟。
//
// 所以策略是：不合规 → 整条 params 丢掉 + 记一条警告（含具体原因），插件照常加载。
// 丢掉整条而非丢掉某个属性：半份 schema 会让模型以为参数比实际更少，猜错更难排查。

// allowedParamTypes 允许的属性类型。object/array 一律不收：宿主与插件之间传的是
// 扁平 input 对象，嵌套结构没有调用方支持，声明了只会诱导模型传一个没人读的子树。
var allowedParamTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
}

// sanitizeCommandParams 就地清理清单里的命令参数声明，返回需要写进日志的警告。
//
// 合规的声明会保留但只留下宿主持支持的键（type/description/default/enum），
// 其余关键字（oneOf/$ref/additionalProperties…）直接剔除：留着它们，模型会当真去用。
func sanitizeCommandParams(m *PluginManifest) []string {
	if m == nil || len(m.Commands) == 0 {
		return nil
	}
	var warns []string
	for i := range m.Commands {
		c := &m.Commands[i]
		if len(c.Params) == 0 {
			// 空对象等同没声明，置 nil 让下游 len() 判断统一
			c.Params = nil
			continue
		}
		if reason := checkParamSchema(c.Params); reason != "" {
			warns = append(warns, fmt.Sprintf(
				"命令 %q 的 params 声明不合规，已忽略该字段（%s）；插件其余功能不受影响",
				commandLabel(c), reason))
			c.Params = nil
			continue
		}
		normalizeParamSchema(c.Params)
	}
	return warns
}

// commandLabel 用于告警文案：优先命令 id，缺失时退回标题，避免出现空引号。
func commandLabel(c *Command) string {
	if id := strings.TrimSpace(c.ID); id != "" {
		return id
	}
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return "(未命名命令)"
}

// checkParamSchema 校验一份 params 声明，合规返回空串，不合规返回中文原因。
//
// 契约（与插件仓库的 check_plugins.py 保持一致）：
//
//	{"type":"object","properties":{ "<键>": {"type":"string|number|integer|boolean",
//	                                        "description":"必填",
//	                                        "default": <与 type 一致>,
//	                                        "enum": [<与 type 一致>...] } },
//	 "required": ["<properties 里存在的键>"]}
func checkParamSchema(params map[string]interface{}) string {
	t, _ := params["type"].(string)
	if strings.TrimSpace(t) != "object" {
		return fmt.Sprintf(`type 必须是 "object"，实际为 %v`, params["type"])
	}

	rawProps, ok := params["properties"].(map[string]interface{})
	if !ok || len(rawProps) == 0 {
		return "properties 缺失或为空（没有任何属性时不应声明 params）"
	}

	names := make([]string, 0, len(rawProps))
	for name := range rawProps {
		names = append(names, name)
	}
	sort.Strings(names) // 报错顺序稳定，便于对照日志与清单

	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return "properties 里存在空键名"
		}
		prop, ok := rawProps[name].(map[string]interface{})
		if !ok {
			return fmt.Sprintf("属性 %q 必须是对象", name)
		}
		pt, _ := prop["type"].(string)
		if !allowedParamTypes[pt] {
			return fmt.Sprintf("属性 %q 的 type %q 不被支持（仅 string/number/integer/boolean）", name, pt)
		}
		if d, _ := prop["description"].(string); strings.TrimSpace(d) == "" {
			return fmt.Sprintf("属性 %q 缺少 description（模型靠它判断该填什么）", name)
		}
		if v, ok := prop["default"]; ok && !paramValueMatches(v, pt) {
			return fmt.Sprintf("属性 %q 的 default 与 type %q 不匹配", name, pt)
		}
		if v, ok := prop["enum"]; ok {
			arr, ok := v.([]interface{})
			if !ok {
				return fmt.Sprintf("属性 %q 的 enum 必须是数组", name)
			}
			if len(arr) == 0 {
				return fmt.Sprintf("属性 %q 的 enum 不能为空数组", name)
			}
			for _, item := range arr {
				if !paramValueMatches(item, pt) {
					return fmt.Sprintf("属性 %q 的 enum 元素与 type %q 不匹配", name, pt)
				}
			}
		}
	}

	if rawReq, ok := params["required"]; ok {
		arr, ok := rawReq.([]interface{})
		if !ok {
			return `required 必须是字符串数组`
		}
		for _, item := range arr {
			key, ok := item.(string)
			if !ok {
				return `required 必须是字符串数组`
			}
			if _, exists := rawProps[key]; !exists {
				return fmt.Sprintf("required 引用了不存在的属性 %q", key)
			}
		}
	}
	return ""
}

// normalizeParamSchema 只保留宿主认识的键，并把 required 去重。
//
// 目的不是「净化」，而是别让模型看见宿主不会照做的关键字：声明里写着 oneOf 却
// 只被当成普通对象传过去，模型会以为分支约束生效了。
func normalizeParamSchema(params map[string]interface{}) {
	rawProps, _ := params["properties"].(map[string]interface{})
	cleanProps := make(map[string]interface{}, len(rawProps))
	for name, raw := range rawProps {
		prop, _ := raw.(map[string]interface{})
		clean := map[string]interface{}{
			"type":        prop["type"],
			"description": prop["description"],
		}
		if v, ok := prop["default"]; ok {
			clean["default"] = v
		}
		if v, ok := prop["enum"]; ok {
			clean["enum"] = v
		}
		cleanProps[name] = clean
	}

	clean := map[string]interface{}{
		"type":       "object",
		"properties": cleanProps,
	}
	if arr, ok := params["required"].([]interface{}); ok && len(arr) > 0 {
		seen := make(map[string]bool, len(arr))
		uniq := make([]interface{}, 0, len(arr))
		for _, item := range arr {
			key := item.(string)
			if seen[key] {
				continue
			}
			seen[key] = true
			uniq = append(uniq, key)
		}
		clean["required"] = uniq
	}

	for k := range params {
		delete(params, k)
	}
	for k, v := range clean {
		params[k] = v
	}
}

// paramValueMatches 判断 default / enum 元素是否与声明的 type 一致。
//
// JSON 解出来的数字统一是 float64，所以 integer 看「没有小数部分」而不是类型。
// json.Number 也接一下：将来若改用 Decoder.UseNumber() 解析清单，这里不必再改。
func paramValueMatches(v interface{}, typ string) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := jsonNumber(v)
		return ok
	case "integer":
		n, ok := jsonNumber(v)
		return ok && math.Trunc(n) == n
	}
	return false
}

func jsonNumber(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
