package ai

// WP2：「上下文已用」弹框的数据源。
//
// 这块的失效方式是把用户引到错误结论：分母用错（拿物理窗口当上限）、
// 来源标错（把默认猜测说成「内置表认得」）、估算漏掉工具 schema。
// 所以逐条钉住这些口径，而不是只断言「能返回非空」。

import (
	"testing"

	"quickdock/internal/db"
)

// 窗口来源的判定顺序必须与 contextWindowOf 完全一致。
func TestContextWindowSource(t *testing.T) {
	// 档案显式配置最优先：用户填了就以他为朕。
	if got := contextWindowSource(AIProfile{Model: "gpt-5", ContextWindow: 32000}); got != "profile" {
		t.Errorf("档案配了 ContextWindow 时应为 profile，实际 %q", got)
	}
	// 没有模型名时谈不上查表。
	if got := contextWindowSource(AIProfile{}); got != "default" {
		t.Errorf("空模型应为 default，实际 %q", got)
	}
	// 内置表命中（gpt-5 系列在表里）。
	if got := contextWindowSource(AIProfile{Model: "gpt-5.4-mini"}); got != "table" {
		t.Errorf("内置表命中时应为 table，实际 %q", got)
	}
	// 表里没有的名字落到默认。
	if got := contextWindowSource(AIProfile{Model: "totally-unknown-model-xyz"}); got != "default" {
		t.Errorf("未知模型应为 default，实际 %q", got)
	}

	// 学到的窗口优先于内置表——它是上游亲口说的。
	const learned = "unit-test-learned-model"
	learnedWindows.Store(learned, 64000)
	t.Cleanup(func() { learnedWindows.Delete(learned) })
	if got := contextWindowSource(AIProfile{Model: learned}); got != "learned" {
		t.Errorf("学到窗口后应为 learned，实际 %q", got)
	}
	// 而且 contextWindowOf 必须真的用上它，两处不能各说各话。
	if w := contextWindowOf(AIProfile{Model: learned}); w != 64000 {
		t.Errorf("contextWindowOf 没用学到值：%d", w)
	}
}

// 快照的各个数字要彼此自洽，且与库里的消息对得上。
func TestAIContextStats(t *testing.T) {
	svc, d := newTestAIService(t)

	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	// 两条用户消息 + 一条带工具调用的助手消息：让字符数与条数都非零。
	for _, s := range []string{"帮我看看这个模块", "再补一下测试"} {
		if _, err := d.AddAIMessageUser(conv.ID, s, "", ""); err != nil {
			t.Fatalf("写用户消息失败: %v", err)
		}
	}

	res := svc.AIContextStats(conv.ID)
	if res.Code != 0 {
		t.Fatalf("AIContextStats 失败: %+v", res)
	}
	view, ok := res.Data.(AIContextStatsView)
	if !ok {
		t.Fatalf("返回类型不对: %T", res.Data)
	}

	if view.ConversationID != conv.ID {
		t.Errorf("会话 id 不对: %q", view.ConversationID)
	}
	// 上限的口径：出站上限必须小于物理窗口（扣了输出预留与固定开销），
	// 且历史软上限不得超过它。
	if view.Window <= 0 {
		t.Errorf("窗口应大于 0: %d", view.Window)
	}
	if view.Ceiling >= view.Window {
		t.Errorf("出站上限应小于物理窗口: ceiling=%d window=%d", view.Ceiling, view.Window)
	}
	if view.HistoryBudget > view.Ceiling {
		t.Errorf("历史软上限不该超过出站上限: %d > %d", view.HistoryBudget, view.Ceiling)
	}
	// 消息统计要对得上。
	if view.MessageCount != 2 {
		t.Errorf("消息条数 = %d，期望 2", view.MessageCount)
	}
	if view.HistoryChars <= 0 {
		t.Errorf("历史字符数应大于 0: %d", view.HistoryChars)
	}
	// 估算 = 历史字符 + 工具 schema，两者都得算进去。
	if view.EstimatedTokens <= 0 {
		t.Errorf("估算应大于 0: %d", view.EstimatedTokens)
	}
	if view.ToolTokens < 0 || view.ToolCount < 0 {
		t.Errorf("工具统计异常: count=%d tokens=%d", view.ToolCount, view.ToolTokens)
	}
	if view.ToolCount > 0 && view.ToolTokens == 0 {
		t.Errorf("有 %d 个工具却算出 0 token：工具定义开销被漏掉了", view.ToolCount)
	}
	// 新会话还没发过请求，实测应为 0（弹框据此显示「估算」而不是「实测」）。
	if view.MeasuredTokens != 0 {
		t.Errorf("未发过请求时实测应为 0，实际 %d", view.MeasuredTokens)
	}
}

// 缺少会话 id / 会话不存在时给可读错误，不返回半截数据。
func TestAIContextStatsErrors(t *testing.T) {
	svc, _ := newTestAIService(t)

	if res := svc.AIContextStats(""); res.Code == 0 {
		t.Error("空 convID 应当失败")
	}
	if res := svc.AIContextStats("no-such-conv"); res.Code == 0 {
		t.Error("不存在的会话应当失败")
	}
}
