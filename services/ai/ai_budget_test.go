package ai

// WP5：轮次预算分档。
//
// 分档的判据不是「模式」而是「谁在等」：
// 有人盯着等 = 可以多给（超了他会补一句「接着做」）；
// 没人盯着 = 早点收尾并说明，比默默烧完 120 轮好。

import (
	"testing"

	"quickdock/internal/db"
)

// token 预算口径：主循环按「新增输入 + 输出」，缓存命中不算钱。
func TestBillableTokens(t *testing.T) {
	cases := []struct {
		name string
		u    db.TokenUsage
		want int
	}{
		{"无缓存", db.TokenUsage{Prompt: 1000, Completion: 200, Cached: 0}, 1200},
		{"97%命中", db.TokenUsage{Prompt: 150000, Completion: 60, Cached: 149632}, 428},
		{"缓存异常大于prompt", db.TokenUsage{Prompt: 100, Completion: 5, Cached: 200}, 5},
		{"空", db.TokenUsage{}, 0},
	}
	for _, c := range cases {
		if got := billableTokens(c.u); got != c.want {
			t.Errorf("%s: billableTokens=%d, want %d", c.name, got, c.want)
		}
	}
	// 全量口径仍给派活用，不能跟着改。
	if got := tokensOf(db.TokenUsage{Prompt: 150000, Completion: 60, Cached: 149632}); got != 150060 {
		t.Errorf("tokensOf 应保持全量口径, got %d", got)
	}
}

// 主对话预算收紧后仍必须比定时任务宽松（「只收窄不放大」按新口径重标定过）。
func TestChatTurnBudgetTighterThanScheduled(t *testing.T) {
	cfg := AIProfile{ContextWindow: 1000000, ContextSoftCap: 120000}
	chat := aiChatTurnBudget(cfg)
	sched := aiContextBudget(cfg) * scheduledTurnBudgetFactor
	if sched >= chat {
		t.Errorf("定时任务预算应紧于主对话: sched=%d chat=%d", sched, chat)
	}
	if chat != 240000 || sched != 120000 {
		t.Errorf("预算口径重标定后数值异常: chat=%d sched=%d", chat, sched)
	}
}

func TestRoundBudgetTiers(t *testing.T) {
	chat := aiRoundBudgetFor("chat", false, false, 0)
	// 人工对话必须保持原值：这是唯一「用户会补话」的场景。
	if chat.Batch != aiToolMaxRounds || chat.Total != aiToolMaxTotalRounds {
		t.Errorf("人工对话预算被改了: %+v", chat)
	}

	sched := aiRoundBudgetFor("chat", true, false, 0)
	if sched.Total >= chat.Total {
		t.Errorf("定时任务的总预算必须比人工更紧: %d vs %d", sched.Total, chat.Total)
	}
	if sched.Batch >= chat.Batch {
		t.Errorf("定时任务的单批预算也要更紧: %d vs %d", sched.Batch, chat.Batch)
	}

	child := aiRoundBudgetFor("chat", false, false, 1)
	if child.Total >= chat.Total {
		t.Errorf("子 agent 的总预算必须比父级更紧: %d vs %d", child.Total, chat.Total)
	}

	readonly := aiRoundBudgetFor("chat", false, true, 0)
	if readonly.Total >= chat.Total {
		t.Errorf("只读场景的总预算必须更紧: %d vs %d", readonly.Total, chat.Total)
	}
}

// 任何一档都不能比原来的常量更宽松——这次改动不该让已有任务变贵。
func TestRoundBudgetNeverExceedsLegacy(t *testing.T) {
	cases := []aiRoundBudget{
		aiRoundBudgetFor("chat", false, false, 0),
		aiRoundBudgetFor("chat", true, false, 0),
		aiRoundBudgetFor("chat", false, true, 0),
		aiRoundBudgetFor("chat", false, false, 1),
		aiRoundBudgetFor("code", false, false, 0),
	}
	for _, rb := range cases {
		if rb.Total > aiToolMaxTotalRounds {
			t.Errorf("总预算超过了旧常量: %d > %d", rb.Total, aiToolMaxTotalRounds)
		}
		if rb.Batch > aiToolMaxRounds {
			t.Errorf("单批预算超过了旧常量: %d > %d", rb.Batch, aiToolMaxRounds)
		}
		if rb.Batch <= 0 || rb.Total < rb.Batch {
			t.Errorf("预算不合法（单批必须 >0 且不超过总预算）: %+v", rb)
		}
	}
}

// 外部传入的 MaxToolRounds 只能在分档结果上再收窄（这一条在 runAgent 里生效，
// 这里只钉住「分档值本身是上限」这个前提）。
func TestRoundBudgetChildAndScheduledSameTightness(t *testing.T) {
	child := aiRoundBudgetFor("chat", false, false, 2)
	sched := aiRoundBudgetFor("chat", true, false, 0)
	// 两者都该是「没人等」那一档；数值一致意味着只有一套口径要维护。
	if child != sched {
		t.Errorf("子 agent 与定时任务应共用同一档: %+v vs %+v", child, sched)
	}
}
