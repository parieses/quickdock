package aiutil

import (
	"strings"
	"testing"
)

// 这组测试守着「提示词装配」的唯一实现。
//
// 它存在的理由很具体：AIAsk 超预算时**从头截断**，而用户的问题排在最后——
// 于是屏显 + 历史一满，被砍掉的正是那句问题。以前每个域各写一份预算逻辑，
// 新域抄错一行就复发，且只在"多轮 + 长上下文"时才暴露，极难复现。
func TestBuildPrompt_KeepsOrderAndBudget(t *testing.T) {
	got := BuildPrompt(Prompt{
		Max: 1000,
		Sections: []Section{
			{Title: "连接信息", Body: "主机：root@prod"},
			{Title: "屏显", Body: "nginx: command not found", KeepTail: true},
		},
		History:   []string{"问：怎么装", "答：apt install nginx"},
		Task:      "这报错啥意思",
		TaskTitle: "本次问题",
	})

	if !strings.Contains(got, "## 连接信息") || !strings.Contains(got, "## 屏显") {
		t.Errorf("小节标题丢失:\n%s", got)
	}
	if !strings.Contains(got, "## 之前的对话") {
		t.Errorf("历史小节丢失:\n%s", got)
	}
	// 顺序：连接信息 → 屏显 → 历史 → 本次问题（模型读到的顺序必须稳定）
	idx := func(s string) int { return strings.Index(got, s) }
	if !(idx("## 连接信息") < idx("## 屏显") && idx("## 屏显") < idx("## 之前的对话") && idx("## 之前的对话") < idx("## 本次问题")) {
		t.Errorf("小节顺序不对:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "这报错啥意思") {
		t.Errorf("问题应在末尾:\n%s", got)
	}
}

func TestBuildPrompt_TaskNeverTrimmed(t *testing.T) {
	// 上下文远超预算时，问题必须完整活着。
	q := strings.Repeat("问", 50)
	got := BuildPrompt(Prompt{
		Max: 300,
		Sections: []Section{
			{Title: "屏显", Body: strings.Repeat("x", 5000), KeepTail: true},
		},
		Task: q,
	})
	if !strings.HasSuffix(strings.TrimSpace(got), q) {
		t.Errorf("问题被裁掉了:\n…%s", got[len(got)-120:])
	}
	if n := runeCount(got); n > 300 {
		t.Errorf("超预算: %d rune", n)
	}
}

func TestBuildPrompt_LaterSectionWins(t *testing.T) {
	// 数组越靠后越重要：预算不够时先砍前面的。
	got := BuildPrompt(Prompt{
		Max: 120,
		Sections: []Section{
			{Title: "旧上下文", Body: strings.Repeat("A", 200)},
			{Title: "重点", Body: strings.Repeat("B", 200)},
		},
		Task: "q",
	})
	if strings.Count(got, "B") == 0 {
		t.Errorf("靠后的段应当优先保留:\n%s", got)
	}
	if n := runeCount(got); n > 120 {
		t.Errorf("超预算: %d", n)
	}
}

func TestBuildPrompt_KeepTail(t *testing.T) {
	body := strings.Repeat("旧", 100) + "最新的报错"
	got := BuildPrompt(Prompt{
		Max:      80,
		Sections: []Section{{Title: "日志", Body: body, KeepTail: true}},
		Task:     "q",
	})
	if !strings.Contains(got, "最新的报错") {
		t.Errorf("KeepTail 应保留尾部:\n%s", got)
	}
}

func TestBuildPrompt_PinnedNotTrimmed(t *testing.T) {
	// 连接信息/环境约束是"回答口径"的前提，不能被大段日志挤掉。
	pinned := "当前连接的主机：root@prod\n（生产环境）变更前请先探查，并准备回滚"
	got := BuildPrompt(Prompt{
		Max: 400,
		Sections: []Section{
			{Title: "连接信息", Body: pinned, Pinned: true},
			{Title: "屏显", Body: strings.Repeat("x", 3000), KeepTail: true},
		},
		Task: "怎么办",
	})
	if !strings.Contains(got, "生产环境") {
		t.Errorf("Pinned 段被裁掉了:\n%s", got)
	}
}

func TestBuildPrompt_MaxShare(t *testing.T) {
	// 屏显限 50%：剩下的一半必须留给别的段。
	got := BuildPrompt(Prompt{
		Max: 400,
		Sections: []Section{
			{Title: "屏显", Body: strings.Repeat("x", 5000), KeepTail: true, MaxShare: 0.5},
			{Title: "补充", Body: strings.Repeat("y", 5000)},
		},
		Task: "q",
	})
	if strings.Count(got, "x") > 220 {
		t.Errorf("MaxShare=0.5 未生效（屏显占了 %d）", strings.Count(got, "x"))
	}
	if strings.Count(got, "y") == 0 {
		t.Errorf("后一段被前一段吃光了:\n%s", got)
	}
}

func TestBuildPrompt_HistoryNewestFirst(t *testing.T) {
	var h []string
	for i := 0; i < 10; i++ {
		h = append(h, "历史"+strings.Repeat("z", 5))
	}
	got := BuildPrompt(Prompt{Max: 200, History: h, HistoryMaxTurns: 3, Task: "q"})
	// 只带最近 3 轮：更早的历史对当前问题没有帮助，还会误导模型。
	if n := strings.Count(got, "历史zzzzz"); n > 3 {
		t.Errorf("历史轮次未被限制: %d", n)
	}
}

func TestBuildPrompt_DropWhenTight(t *testing.T) {
	// DropWhenTight：预算分不到时整段消失，而不是留半句残话。
	// 预算卡到"连标题都放不下"的程度（任务已占满）。
	got := BuildPrompt(Prompt{
		Max: 24,
		Sections: []Section{
			{Title: "可选背景", Body: strings.Repeat("b", 500), DropWhenTight: true},
		},
		Task: "q",
	})
	if strings.Contains(got, "## 可选背景") {
		t.Errorf("DropWhenTight 段应整段丢弃:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "q") {
		t.Errorf("问题丢失:\n%s", got)
	}
}

func TestBuildPrompt_UnlimitedWhenMaxZero(t *testing.T) {
	// Max<=0 表示不限：调用方自有上限时不该再被砍一刀。
	body := strings.Repeat("x", 5000)
	got := BuildPrompt(Prompt{Max: 0, Sections: []Section{{Title: "内容", Body: body}}, Task: "q"})
	if !strings.Contains(got, body) {
		t.Errorf("Max<=0 时不应裁剪")
	}
}

func TestBuildPrompt_EmptyBodySkipped(t *testing.T) {
	got := BuildPrompt(Prompt{
		Max:      200,
		Sections: []Section{{Title: "空段", Body: "   "}},
		Task:     "q",
	})
	if strings.Contains(got, "## 空段") {
		t.Errorf("空段不该渲染标题:\n%s", got)
	}
	if got != "## 本次要求\nq" {
		t.Errorf("只剩任务时应只有一块，实际 %q", got)
	}
}
