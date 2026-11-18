package ai

// WP20：推理分段。
//
// 用户报「只看到一个深度思考」。两种成因必须分开：
//   A. 模型确实只在首轮给了 reasoning —— 不是 bug；
//   B. 我们跨轮累加把 N 轮拼成一段 —— 是 bug。
// 所以 done 事件要带「有推理的轮数」与总字符数：A 的轮数是 1，B 的轮数大于 1
// 但界面上仍然只有一段。

import (
	"testing"
	"time"

	"quickdock/internal/db"
)

func TestDoneEventCarriesReasoningStats(t *testing.T) {
	ev := doneEvent("c1", 10*time.Millisecond, db.TokenUsage{Prompt: 1, Completion: 2}, "m", 3, 4, 2, 512)
	if ev["reasoningRounds"] != 2 {
		t.Errorf("reasoningRounds = %v，期望 2", ev["reasoningRounds"])
	}
	if ev["reasoningChars"] != 512 {
		t.Errorf("reasoningChars = %v，期望 512", ev["reasoningChars"])
	}
	// 原有字段不能被挤掉。
	for _, k := range []string{"convId", "promptTokens", "completionTokens", "toolRounds", "toolCalls", "model"} {
		if _, ok := ev[k]; !ok {
			t.Errorf("done 事件缺少原有字段 %q", k)
		}
	}
}

// 不传推理统计时（外部调用方、测试）字段直接不出现——前端据此隐藏，
// 而不是显示「思考 0 轮」这种看着像故障的数字。
func TestDoneEventWithoutReasoningStats(t *testing.T) {
	ev := doneEvent("c1", 0, db.TokenUsage{}, "m", 0, 0)
	if _, ok := ev["reasoningRounds"]; ok {
		t.Error("未提供统计时不该有 reasoningRounds 字段")
	}
	// 只给一个值时同样不写（避免半截数据）。
	ev = doneEvent("c1", 0, db.TokenUsage{}, "m", 0, 0, 5)
	if _, ok := ev["reasoningRounds"]; ok {
		t.Error("只给一个值时不该写入统计")
	}
}
