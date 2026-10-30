package db

import "testing"

// TestAIUsageAccountingBoundary 用量口径的边界：
//
//   - ai_usage 明细：所有调用都该有（含标题 / 摘要 / 无会话的一次性调用）；
//   - ai_conversations 累加列：只有真正的一问一答才累加。
//
// 混在一起累加过的后果是「对话列表里的 token > 各条回复下方数字之和」，
// 用户一眼就能看出来数字不对。
func TestAIUsageAccountingBoundary(t *testing.T) {
	d := openTestDB(t)

	conv, err := d.CreateAIConversation(NewAIConversation{Title: "测试"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	// 一次问答：明细 + 会话累加
	chat := TokenUsage{Prompt: 1000, Completion: 200, Cached: 800}
	if err := d.RecordAIUsage(conv.ID, "deepseek-chat", "chat", chat); err != nil {
		t.Fatalf("记录明细失败: %v", err)
	}
	if err := d.AddAIConversationUsage(conv.ID, "deepseek-chat", chat, true); err != nil {
		t.Fatalf("累加会话失败: %v", err)
	}

	// 生成标题、压缩摘要：只进明细，不该污染会话
	_ = d.RecordAIUsage(conv.ID, "deepseek-chat", "title", TokenUsage{Prompt: 9000, Completion: 12})
	_ = d.RecordAIUsage(conv.ID, "deepseek-chat", "summary", TokenUsage{Prompt: 30000, Completion: 300})

	// SSH 面板那种无会话调用：没有任何 conv 相关写入
	_ = d.RecordAIUsage("", "deepseek-chat", "ask", TokenUsage{Prompt: 5000, Completion: 400})

	got, err := d.GetAIConversation(conv.ID)
	if err != nil || got == nil {
		t.Fatalf("读会话失败: %v", err)
	}
	if got.PromptTokens != chat.Prompt || got.CompletionTokens != chat.Completion || got.RequestCount != 1 {
		t.Errorf("会话累加被非问答调用污染: prompt=%d completion=%d requests=%d",
			got.PromptTokens, got.CompletionTokens, got.RequestCount)
	}

	groups, err := d.ListAIUsageGroups("")
	if err != nil {
		t.Fatalf("读明细失败: %v", err)
	}
	var prompt, completion, cached, requests int
	for _, g := range groups {
		prompt += g.PromptTokens
		completion += g.CompletionTokens
		cached += g.CachedTokens
		requests += g.Requests
	}
	// 明细必须包含全部四类调用
	if requests != 4 {
		t.Errorf("明细应含 4 次调用，实得 %d", requests)
	}
	if prompt != 1000+9000+30000+5000 || completion != 200+12+300+400 || cached != 800 {
		t.Errorf("明细汇总错误: prompt=%d completion=%d cached=%d", prompt, completion, cached)
	}

	// 会话数只数「有明细的会话」，无会话的一次性调用不算进来
	n, err := d.CountAIUsageConversations("")
	if err != nil {
		t.Fatalf("统计会话数失败: %v", err)
	}
	if n != 1 {
		t.Errorf("会话数应为 1，实得 %d", n)
	}
}
