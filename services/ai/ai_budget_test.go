package ai

// WP5：轮次预算分档。
//
// 分档的判据不是「模式」而是「谁在等」：
// 有人盯着等 = 可以多给（超了他会补一句「接着做」）；
// 没人盯着 = 早点收尾并说明，比默默烧完 120 轮好。

import "testing"

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
