package ai

// 会话统计聚合：锁住弹框里最容易被算错的几个口径。
// 命中率的分母必须是 prompt（不是 prompt+completion），
// 未缓存输入 = prompt - cached，步数按 tool_calls 数组长度而非轮数。
import (
	"testing"

	"quickdock/internal/db"
)

func TestSessionStatsTokenMath(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "统计"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 两条 assistant 消息：prompt 1000/2000，completion 100/200，cached 800/1000。
	// 命中率 = 1800/3000 = 60%；未缓存 = 3000-1800 = 1200；输出 = 300；总量 = 3300。
	for _, row := range []struct {
		prompt, completion, cached int
		model                      string
	}{
		{1000, 100, 800, "m1"},
		{2000, 200, 1000, "m1"},
	} {
		msg, e := d.SaveAssistantTurn(conv.ID, db.AssistantTurn{
			Content: "x",
			Model:   row.model,
			Usage:   db.TokenUsage{Prompt: row.prompt, Completion: row.completion, Cached: row.cached},
		})
		if e != nil || msg == nil {
			t.Fatalf("建消息失败: %v", e)
		}
	}

	res := svc.AISessionStats(conv.ID)
	if res.Code != 0 {
		t.Fatalf("聚合失败: %+v", res)
	}
	v, ok := res.Data.(AISessionStatsView)
	if !ok {
		t.Fatalf("返回类型不对: %T", res.Data)
	}
	if v.TotalTokens != 3300 || v.Output != 300 || v.CacheRead != 1800 || v.UncachedInput != 1200 {
		t.Errorf("token 明细不对: %+v", v)
	}
	if v.CacheHitRate != 60 {
		t.Errorf("命中率应为 60%%，实际 %d", v.CacheHitRate)
	}
	if v.RequestCount != 2 {
		t.Errorf("调用次数应为 2，实际 %d", v.RequestCount)
	}
	if v.Model != "m1" {
		t.Errorf("提供方/模型应取用量最多的 m1，实际 %q", v.Model)
	}
	// 明细：单模型时一条，token = 两条之和，调用次数 = 2。
	if len(v.ModelDetails) != 1 || v.ModelDetails[0].Model != "m1" ||
		v.ModelDetails[0].Tokens != 3300 || v.ModelDetails[0].Requests != 2 {
		t.Errorf("单模型明细不对: %+v", v.ModelDetails)
	}
}

// 中途换模型的会话：弹框要能列出全部模型（用量降序），不能只显示 top1。
func TestSessionStatsModelDetails(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "多模型"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	for _, row := range []struct {
		model              string
		prompt, completion int
	}{
		{"m1", 1000, 100}, // 1100 tok
		{"m2", 3000, 200}, // 3200 tok —— 最多
		{"m3", 500, 50},   // 550 tok
		{"m2", 100, 10},   // m2 第二次：110 tok，累计 3310 tok / 2 次
	} {
		msg, e := d.SaveAssistantTurn(conv.ID, db.AssistantTurn{
			Content: "x", Model: row.model,
			Usage: db.TokenUsage{Prompt: row.prompt, Completion: row.completion},
		})
		if e != nil || msg == nil {
			t.Fatalf("建消息失败: %v", e)
		}
	}

	v, ok := svc.AISessionStats(conv.ID).Data.(AISessionStatsView)
	if !ok {
		t.Fatal("返回类型不对")
	}
	if len(v.ModelDetails) != 3 {
		t.Fatalf("应有 3 个模型，实际 %d: %+v", len(v.ModelDetails), v.ModelDetails)
	}
	// 按 token 降序：m2(3310) > m1(1100) > m3(550)。
	want := []struct {
		model    string
		tokens   int
		requests int
	}{
		{"m2", 3310, 2}, {"m1", 1100, 1}, {"m3", 550, 1},
	}
	for i, w := range want {
		got := v.ModelDetails[i]
		if got.Model != w.model || got.Tokens != w.tokens || got.Requests != w.requests {
			t.Errorf("第 %d 个模型明细不对: 期望 %+v, 实际 %+v", i, w, got)
		}
	}
	if v.Model != "m2" {
		t.Errorf("top1 应与明细第一行一致（m2），实际 %q", v.Model)
	}
}
