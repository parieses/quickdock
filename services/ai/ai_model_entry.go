package ai

// 一条接入下的多个模型 + 每个模型的推理档位（按主流 agent 工具的做法）。
//
// 设计取向（与用户确认过的方案 A）：
//   - 一个档案 = 一次「服务商接入」（baseURL + apiKey），下面可以启用多个模型；
//   - 每个模型可以有自己的上下文窗口、输出上限、输入类型；
//   - 推理档位是「档位名 → API 参数值」的两层映射：厂商之间的参数名不一样
//     （OpenAI 系 reasoning_effort、Anthropic 系 thinking.budget_tokens、
//     阿里系 enable_thinking），把参数名留给配置而不是写死在代码里。
//
// 兼容：Models 为空时，用 Model 合成一条等价条目，行为与改动前完全一致。

import (
	"strconv"
	"strings"
)

// AIReasoningLevel 一个推理档位。
//
// Param 为空表示「这个档位不额外传参」——例如「关」，用它表达「什么都不加」，
// 比在代码里到处判断 "off" 更直白。
type AIReasoningLevel struct {
	// Name 档位名：off / low / medium / high / max（界面上的选项）。
	Name string `json:"name"`
	// Param 请求体里的参数名，如 reasoning_effort / thinking_budget。
	Param string `json:"param"`
	// Value 参数值（字符串形态）："low"、"8192"。数字也按字符串存——
	// 不同厂商这里可能是枚举也可能是整数，统一成字符串再按需转。
	Value string `json:"value"`
}

// AIModelEntry 一条接入下的一个模型。
type AIModelEntry struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	// ContextWindow / MaxOutput 0 = 按模型名自动（见 ai_context.go / ai_model_caps.go）。
	ContextWindow int `json:"contextWindow,omitempty"`
	MaxOutput     int `json:"maxOutput,omitempty"`
	// InputTypes text / image；空 = text。
	InputTypes []string `json:"inputTypes,omitempty"`
	// ReasoningLevels 该模型支持的推理档位；空 = 沿用档案的 ThinkingEnabled 开关。
	ReasoningLevels []AIReasoningLevel `json:"reasoningLevels,omitempty"`
	// ReasoningLevel 当前选中的档位名（空 = 自动/不指定）。
	ReasoningLevel string `json:"reasoningLevel,omitempty"`
}

// activeModelEntry 解析「这条档案此刻用的是哪个模型的哪份能力声明」。
//
// 顺序：ModelID → Model → Models[0]，都没有就返回空条目（各字段按自动处理）。
// 返回值 copy=false 表示这是从 Model 合成出来的，没有真实的 Models 项。
func activeModelEntry(cfg AIProfile) (AIModelEntry, bool) {
	for _, m := range cfg.Models {
		if cfg.ModelID != "" && m.ID == cfg.ModelID {
			return m, true
		}
	}
	if cfg.Model != "" {
		for _, m := range cfg.Models {
			if m.ID == cfg.Model {
				return m, true
			}
		}
		return AIModelEntry{ID: cfg.Model}, false
	}
	if len(cfg.Models) > 0 {
		return cfg.Models[0], true
	}
	return AIModelEntry{}, false
}

// modelContextWindowOf / modelMaxOutputOf 能力解析：条目显式值优先，否则自动。
func modelContextWindowOf(cfg AIProfile) int {
	if e, _ := activeModelEntry(cfg); e.ContextWindow > 0 {
		return e.ContextWindow
	}
	return contextWindowOf(cfg)
}

func modelMaxOutputOf(cfg AIProfile) int {
	if e, _ := activeModelEntry(cfg); e.MaxOutput > 0 {
		return e.MaxOutput
	}
	if cfg.MaxTokens > 0 {
		return cfg.MaxTokens
	}
	return autoMaxTokensOf(modelNameOf(cfg), modelContextWindowOf(cfg))
}

// modelNameOf 用于查能力表的模型名。
func modelNameOf(cfg AIProfile) string {
	if e, _ := activeModelEntry(cfg); strings.TrimSpace(e.ID) != "" {
		return e.ID
	}
	return cfg.Model
}

// activeReasoningLevel 返回该模型当前选中的档位（没有就 false）。
func activeReasoningLevel(cfg AIProfile) (AIReasoningLevel, bool) {
	e, _ := activeModelEntry(cfg)
	name := strings.TrimSpace(e.ReasoningLevel)
	if name == "" {
		return AIReasoningLevel{}, false
	}
	for _, lv := range e.ReasoningLevels {
		if strings.EqualFold(lv.Name, name) {
			return lv, true
		}
	}
	return AIReasoningLevel{}, false
}

// applyReasoningLevel 把选中的档位写进请求体。
//
// 只在**配置过档位**时才动请求体：老档案（没配档位）继续走 ThinkingEnabled 的
// 老路径，不会因为这次改动给厂商多送一个它不认识的字段（那是 400 的常见来源）。
func applyReasoningLevel(body map[string]any, cfg AIProfile) {
	if body == nil {
		return
	}
	lv, ok := activeReasoningLevel(cfg)
	if !ok || strings.TrimSpace(lv.Param) == "" {
		return
	}
	v := strings.TrimSpace(lv.Value)
	if v == "" {
		return
	}
	// 纯数字就按数字送：thinking_budget 这类字段收 "8192" 会报类型错误。
	if n, err := strconv.Atoi(v); err == nil {
		body[lv.Param] = n
		return
	}
	body[lv.Param] = v
}
