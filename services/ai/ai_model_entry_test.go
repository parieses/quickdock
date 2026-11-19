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
