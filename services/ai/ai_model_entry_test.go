package ai

// 多模型档案与推理档位（方案 A）的最小回归。
//
// 三条底线：
//  1. 老档案（Models 为空）行为一字不变；
//  2. ModelID 能选中某个模型，并让它声明的窗口/输出上限生效；
//  3. 只有**配置过**档位才动请求体——给不认识的厂商多送字段就是 400。

import "testing"

func TestActiveModelEntryFallback(t *testing.T) {
	old := AIProfile{Model: "deepseek-v4.1-flash"}
	e, real := activeModelEntry(old)
	if real {
		t.Error("老档案不该被当成真实条目")
	}
	if e.ID != "deepseek-v4.1-flash" {
		t.Errorf("应由 Model 合成，实际 %q", e.ID)
	}

	multi := AIProfile{
		Model:   "deepseek-v4.1-flash",
		ModelID: "gpt-4o",
		Models: []AIModelEntry{
			{ID: "deepseek-v4.1-flash", ContextWindow: 1000000},
			{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384},
		},
	}
	e, real = activeModelEntry(multi)
	if !real || e.ID != "gpt-4o" {
		t.Errorf("ModelID 应优先，实际 %q real=%v", e.ID, real)
	}
	if got := modelContextWindowOf(multi); got != 128000 {
		t.Errorf("窗口应取条目值 128000，实际 %d", got)
	}
	if got := modelMaxOutputOf(multi); got != 16384 {
		t.Errorf("输出上限应取条目值 16384，实际 %d", got)
	}
}

// 老档案：没配 ModelID/Models 时，与改动前一致（按模型名表 + MaxTokens）。
func TestOldProfileUnchanged(t *testing.T) {
	cfg := AIProfile{Model: "deepseek-v4.1-flash", MaxTokens: 8192}
	if got := modelMaxOutputOf(cfg); got != 8192 {
		t.Errorf("显式 MaxTokens 应优先，实际 %d", got)
	}
	cfg.MaxTokens = 0
	if got := modelMaxOutputOf(cfg); got != 32768 {
		t.Errorf("缺省应按能力表给 32768，实际 %d", got)
	}
}

func TestApplyReasoningLevel(t *testing.T) {
	base := AIProfile{Models: []AIModelEntry{{
		ID:             "deepseek-v4.1-flash",
		ReasoningLevel: "high",
		ReasoningLevels: []AIReasoningLevel{
			{Name: "low", Param: "reasoning_effort", Value: "low"},
			{Name: "high", Param: "reasoning_effort", Value: "high"},
			{Name: "max", Param: "thinking_budget", Value: "32768"},
		},
	}}, ModelID: "deepseek-v4.1-flash"}

	body := map[string]any{}
	applyReasoningLevel(body, base)
	if body["reasoning_effort"] != "high" {
		t.Errorf("档位应写进请求体，实际 %v", body)
	}

	// 数字值要按数字送：thinking_budget 收字符串会 400。
	base.Models[0].ReasoningLevel = "max"
	body = map[string]any{}
	applyReasoningLevel(body, base)
	if body["thinking_budget"] != 32768 {
		t.Errorf("纯数字应转成 int，实际 %#v", body["thinking_budget"])
	}

	// 没配档位：一个字段都不许加（老档案走 ThinkingEnabled 老路径）。
	plain := AIProfile{Model: "gpt-4o"}
	body = map[string]any{}
	applyReasoningLevel(body, plain)
	if len(body) != 0 {
		t.Errorf("未配置档位时不该动请求体，实际 %v", body)
	}
}

// 「关闭思考」必须两条路径都关：只关 ThinkingEnabled 的话，配了推理档位的模型
// 仍会照档位思考，面板上点了关却还是先黑屏十几秒。
func TestWithoutThinkingClearsBothSwitches(t *testing.T) {
	cfg := AIProfile{
		ThinkingEnabled: true,
		ModelID:         "m1",
		Models: []AIModelEntry{{
			ID:             "m1",
			ReasoningLevel: "high",
			ReasoningLevels: []AIReasoningLevel{
				{Name: "high", Param: "reasoning_effort", Value: "high"},
			},
		}},
	}

	out := withoutThinking(cfg)
	if out.ThinkingEnabled {
		t.Error("ThinkingEnabled 应被关掉")
	}
	if out.Models[0].ReasoningLevel != "" {
		t.Errorf("推理档位应被清空，实际 %q", out.Models[0].ReasoningLevel)
	}

	// 请求体里只剩「显式关」这一个字段：老档案原本就在发 enable_thinking，
	// 这一轮必须发 false 才算真关。什么都不发的话，默认开思考的模型（Qwen3
	// 这类）会继续想，用户点了关闭还是能看到思考流。
	body := map[string]any{}
	applyThinkingSwitch(body, out)
	applyReasoningLevel(body, out)
	if v, ok := body["enable_thinking"]; !ok || v != false {
		t.Errorf("关闭思考应显式发 enable_thinking=false，实际 %v", body)
	}

	// 原档案不能被改：一次性的「这轮别想」不该影响对话页的下一次提问。
	if !cfg.ThinkingEnabled || cfg.Models[0].ReasoningLevel != "high" {
		t.Errorf("原档案被就地修改: %+v", cfg)
	}
}

// 档案本来就没走 enable_thinking（ThinkingEnabled=false）时，关闭思考一个字段
// 都不该发：OpenAI 官方对不认识的 body 字段直接 400，宁可关不掉也不能报错。
func TestWithoutThinkingSilentWhenNeverEnabled(t *testing.T) {
	out := withoutThinking(AIProfile{Model: "gpt-4o"})
	body := map[string]any{}
	applyThinkingSwitch(body, out)
	if len(body) != 0 {
		t.Errorf("未启用过 enable_thinking 的档案不该被塞字段，实际 %v", body)
	}
}

// 配了「关」档位就用档位自己的值（reasoning_effort=none / thinking_budget=0），
// 这比一个通用 false 更贴近厂商语义。
func TestWithoutThinkingUsesOffLevel(t *testing.T) {
	cfg := AIProfile{
		ThinkingEnabled: true,
		ModelID:         "m1",
		Models: []AIModelEntry{{
			ID:             "m1",
			ReasoningLevel: "high",
			ReasoningLevels: []AIReasoningLevel{
				{Name: "off", Param: "reasoning_effort", Value: "none"},
				{Name: "high", Param: "reasoning_effort", Value: "high"},
			},
		}},
	}
	out := withoutThinking(cfg)
	if out.Models[0].ReasoningLevel != "off" {
		t.Fatalf("应选中 off 档位，实际 %q", out.Models[0].ReasoningLevel)
	}
	body := map[string]any{}
	applyReasoningLevel(body, out)
	if body["reasoning_effort"] != "none" {
		t.Errorf("应带上 off 档位的值，实际 %v", body)
	}
}
