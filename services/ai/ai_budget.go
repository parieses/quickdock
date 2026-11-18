package ai

// 轮次预算按任务类型分档（WP5）。
//
// 原来 `aiToolMaxRounds = 12` / `aiToolMaxTotalRounds = 120` 是全局常量，
// 对三种完全不同的场景用同一把尺子：
//   - 人在电脑前盯着（超了可以补一句「接着做」）；
//   - 定时任务（没人看着，超了就是白烧钱，而且没人在乎它多查两轮）；
//   - 子 agent（它只被派了一件小事，12 轮批 × 10 批是父级的上限，不是它的）。
//
// 分档**只收不放**：任何一档都不超过原来的 120 轮，所以这次改动不会让
// 已经有账单在跑的任务变得更贵。`req.MaxToolRounds` 仍然可以再往下收窄，
// 与这里的结果取小。

// aiRoundBudget 一次提问的轮次预算。
type aiRoundBudget struct {
	// Batch 「一批」多少轮：到顶时看这一批有没有真进展，有就再给一批。
	Batch int
	// Total 总上限：病态兜底，不是进度墙。
	Total int
}

// aiRoundBudgetFor 按入口与场景给出预算。
//
// mode 目前只用来区分「有工具的对话」与纯聊天；真正的差异来自入口：
// 定时任务、子 agent、只读场景各自更紧。
func aiRoundBudgetFor(mode string, scheduled, readonly bool, depth int) aiRoundBudget {
	base := aiRoundBudget{Batch: aiToolMaxRounds, Total: aiToolMaxTotalRounds}

	// 子 agent：只被派了一件小事。给它父级的上限等于允许它把一件小活
	// 做成一场长排查，而父级还要等它回来。
	if depth > 0 {
		return aiRoundBudget{Batch: 8, Total: 48}
	}
	// 只读场景（分析 / 问答）：不需要「改-验-再改」的循环，8 轮够翻文件。
	//
	// 必须判在 scheduled 之前：调用点传的 scheduled 是
	// `ScheduledNoTools || ScheduledReadOnly`，只读必然让 scheduled 为真。
	// 这个顺序写反过一次，结果只读档实际拿到的是 8/48，而文档写的是 8/40。
	if readonly {
		return aiRoundBudget{Batch: 8, Total: 40}
	}
	// 定时任务 / 无人值守：没人能补一句「接着做」，所以宁可让它早收尾
	// 并在总结里说明「没做完」，也不要它默默烧完 120 轮。
	if scheduled {
		return aiRoundBudget{Batch: 8, Total: 48}
	}
	_ = mode // 目前各模式共用上面的分档；留参数是为了后续按模式细化。
	return base
}
