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

// 记忆整理的 token 要算进会话总量与输入框下方那个胶囊。
//
// 它不属于任何一条回复本身，但是真实计费：不并进来，用户看到的消耗永远比账单少，
// 而且「我就问了几句怎么花了这么多」无法解释。落点是「并进那条回复的消息行」——
// 前端的胶囊是把消息行的 token 求和算出来的，单开一张表它永远不认。
func TestSessionStatsIncludesMemoryUsage(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "含记忆整理"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	msg, err := d.SaveAssistantTurn(conv.ID, db.AssistantTurn{
		Content: "x", Model: "chat-model", ToolRounds: 2, ToolCalls: `[{"id":"1"}]`,
		Usage: db.TokenUsage{Prompt: 1000, Completion: 100, Cached: 400},
	})
	if err != nil || msg == nil {
		t.Fatalf("建消息失败: %v", err)
	}
	// 记忆整理在回答结束之后才跑完：后台把这次调用的用量并回那条回复。
	if ok, err := d.AddAIMessageUsage(msg.ID, db.TokenUsage{Prompt: 500, Completion: 120}); err != nil || !ok {
		t.Fatalf("并进用量失败: ok=%v err=%v", ok, err)
	}

	v, ok := svc.AISessionStats(conv.ID).Data.(AISessionStatsView)
	if !ok {
		t.Fatal("返回类型不对")
	}
	// 总量 = 消息 1100 + 记忆整理 620 = 1720；输出 = 100 + 120 = 220。
	if v.TotalTokens != 1720 {
		t.Errorf("总量应含记忆整理调用，实际 %d（期望 1720）", v.TotalTokens)
	}
	if v.Output != 220 {
		t.Errorf("输出应含记忆整理，实际 %d（期望 220）", v.Output)
	}
	// 未缓存 = (1000+500) - 400 = 1100。缓存读取不因记忆调用变化（它没命中缓存）。
	if v.UncachedInput != 1100 || v.CacheRead != 400 {
		t.Errorf("未缓存/缓存读取不对: %+v", v)
	}
	// 聊天口径不受影响：轮/步/请求次数只描述问答本身。
	if v.Rounds != 2 || v.Steps != 1 || v.RequestCount != 1 {
		t.Errorf("轮/步/请求次数不该被记忆调用污染: %+v", v)
	}
	// 并进消息行之后，模型明细仍只有聊天模型——记忆调用用哪个模型由 ai_usage 记录。
	if len(v.ModelDetails) != 1 || v.ModelDetails[0].Model != "chat-model" {
		t.Errorf("模型明细应只含聊天模型: %+v", v.ModelDetails)
	}
}
