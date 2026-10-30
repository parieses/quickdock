package ai

import (
	"errors"
	"strings"
	"testing"

	"quickdock/internal/db"
)

func TestContextWindowOf(t *testing.T) {
	// 最长匹配必须生效：gpt-5.4-mini 是 400K，不能被更短的 "gpt-5" 抢先匹配成 400K
	// 之外的档位，也不能被 "gpt-5.4" 的 1.05M 盖住。
	cases := []struct {
		model string
		want  int
	}{
		{"deepseek-v4-flash", 1000000},
		{"deepseek-chat", 128000},
		{"gpt-5.4-mini", 400000},
		{"gpt-5.4", 1050000},
		{"gpt-4o-mini", 128000},
		{"claude-opus-4.8", 1000000},
		{"claude-3-5-sonnet", 200000},
		{"qwen3-max", 262144},
		{"glm-5.2-flash", 1048576},
		{"kimi-k2.6", 262144},
		{"mystery-model-9000", aiContextDefaultWindow},
		{"", aiContextDefaultWindow},
	}
	for _, c := range cases {
		got := contextWindowOf(AIProfile{Model: c.model})
		if got != c.want {
			t.Errorf("contextWindowOf(%q) = %d, want %d", c.model, got, c.want)
		}
	}

	// 显式配置优先于一切推断。
	if got := contextWindowOf(AIProfile{Model: "deepseek-v4", ContextWindow: 8192}); got != 8192 {
		t.Errorf("显式 ContextWindow 未生效: %d", got)
	}
}

func TestContextOverflowDetectionAndLearning(t *testing.T) {
	// DeepSeek / OpenAI 兼容网关的典型超窗报文。
	err := errors.New(`HTTP 400: {"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1049647 tokens (985647 in the messages, 64000 in the completion). Please reduce the length of the messages or completion.","type":"invalid_request_error"}}`)
	w, ok := contextOverflow(err, "local-big-model")
	if !ok {
		t.Fatal("应判定为超窗")
	}
	if w != 1048576 {
		t.Fatalf("窗口解析错误: %d", w)
	}
	// 学到之后，同名模型不再需要靠内置表推断。
	if got := contextWindowOf(AIProfile{Model: "local-big-model"}); got != 1048576 {
		t.Fatalf("学到的窗口未生效: %d", got)
	}
	// 显式配置仍然优先于学到的值。
	if got := contextWindowOf(AIProfile{Model: "local-big-model", ContextWindow: 32768}); got != 32768 {
		t.Fatalf("显式配置应优先于学到的窗口: %d", got)
	}

	// 非超窗错误不能误判：把 401 当超窗去压缩历史，等于白白丢上下文。
	if _, ok := contextOverflow(errors.New("HTTP 401: invalid api key"), "m"); ok {
		t.Fatal("401 被误判为超窗")
	}
	if _, ok := contextOverflow(nil, "m"); ok {
		t.Fatal("nil 错误被误判为超窗")
	}
	// 有的网关只说「超了」不说数字：要能识别，但窗口返回 0。
	if w, ok := contextOverflow(errors.New(`HTTP 400: prompt is too long`), "m2"); !ok || w != 0 {
		t.Fatalf("无数字的超窗报文识别错误 ok=%v w=%d", ok, w)
	}
}

func TestContextBudgetScalesWithWindow(t *testing.T) {
	small := aiContextBudget(AIProfile{Model: "phi-3-mini", MaxTokens: 1024})
	mid := aiContextBudget(AIProfile{Model: "gpt-4o", MaxTokens: 8192})
	big := aiContextBudget(AIProfile{Model: "deepseek-v4", MaxTokens: 8192})

	if !(small < mid && mid < big) {
		t.Fatalf("预算未随窗口放大: small=%d mid=%d big=%d", small, mid, big)
	}
	if big > aiContextSoftCap {
		t.Fatalf("预算超过软上限: %d", big)
	}
	if small < aiContextMinBudget {
		t.Fatalf("预算低于下限: %d", small)
	}
	// 老版本的固定 3000 是这次要修的问题：128K 窗口下必须明显更高，
	// 否则第 4、5 轮就把前面全摘要掉了。
	if mid < 3000*10 {
		t.Fatalf("128K 窗口的预算过小: %d", mid)
	}
	// max_tokens 配成 100 万也不能把输入空间挤没。
	huge := aiContextBudget(AIProfile{Model: "deepseek-v4", MaxTokens: 1000000})
	if huge < aiContextMinBudget {
		t.Fatalf("超大 max_tokens 把预算挤没了: %d", huge)
	}
}

func TestKeepRecentCount(t *testing.T) {
	mk := func(n int, size int) []db.AIMessage {
		out := make([]db.AIMessage, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, db.AIMessage{Role: "user", Content: strings.Repeat("x", size)})
		}
		return out
	}
	// 预算充裕时保留更多条，预算紧张时至少留 8 条（「上一轮说过什么」得看得见）。
	lots := keepRecentCount(mk(100, 400), 200000)
	tight := keepRecentCount(mk(100, 400), 6000)
	if lots <= tight {
		t.Fatalf("预算大时应保留更多: lots=%d tight=%d", lots, tight)
	}
	if tight < aiKeepRecentMin {
		t.Fatalf("保留条数低于下限: %d", tight)
	}
	if lots > aiKeepRecentMax {
		t.Fatalf("保留条数超过上限: %d", lots)
	}
	// 历史本身比下限还短时不能越界。
	if got := keepRecentCount(mk(3, 100), 200000); got != 3 {
		t.Fatalf("短历史保留条数应为 3，实际 %d", got)
	}
}

func TestClipSummaryInput(t *testing.T) {
	short := "hello"
	if clipSummaryInput(short) != short {
		t.Fatal("未超限的输入不应被裁剪")
	}
	long := strings.Repeat("a", aiSummaryInputRune+5000)
	got := clipSummaryInput(long)
	n := len([]rune(got))
	// 裁剪后应略小于上限（拼接的省略标记算在内，给几十字符余量）。
	if n > aiSummaryInputRune+100 {
		t.Fatalf("裁剪后仍过长: %d", n)
	}
	if !strings.Contains(got, "中间省略") {
		t.Fatal("裁剪应带省略标记，否则模型会以为历史是连续的")
	}
}

func TestLoopBudgetNotSmallerThanLegacy(t *testing.T) {
	// 老实现是固定 24000。任何窗口下都不应比它更小，否则等于把本来
	// 能塞进上下文的工具结果提前折叠掉。
	for _, m := range []string{"phi-3-mini", "gpt-4o", "deepseek-v4"} {
		if b := aiLoopBudget(AIProfile{Model: m}); b < 24000 {
			t.Fatalf("%s 的循环预算低于历史的 24000: %d", m, b)
		}
	}
}
