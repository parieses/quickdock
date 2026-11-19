package ai

// 一次性动作（消息级）的回归。
//
// 要点不是「提示词写得好不好」（那要靠实机），而是三条结构性约束：
//  1. 认不出来的动作要安全退化成普通对话，不能报错也不能带上别人的约束；
//  2. 参数占位必须被替换掉——漏一个就会把 `{lang}` 原样发给模型；
//  3. 默认参数要给出「不追问也能干活」的值。

import (
	"strings"
	"testing"
)

func TestAIActionLookup(t *testing.T) {
	if a := aiActionByName("translate"); a.Name != aiActionTranslate || a.Prompt == "" {
		t.Errorf("translate 应有提示词，实际 %+v", a)
	}
	// 大小写与空白都要能认出来（前端可能传来 " Explain "）。
	if a := aiActionByName("  Explain "); a.Name != aiActionExplain {
		t.Errorf("应忽略大小写与空白，实际 %q", a.Name)
	}
	// 认不出来 → 普通对话，且不带任何额外约束。
	for _, bad := range []string{"", "nope", "chat "} {
		a := aiActionByName(bad)
		if bad == "chat " && a.Name == aiActionChat {
			continue
		}
		if a.Name != aiActionChat {
			t.Errorf("%q 应退化成 chat，实际 %q", bad, a.Name)
		}
		if got := aiActionInstruction(a, nil); got != "" {
			t.Errorf("普通对话不该追加约束，实际 %q", got)
		}
	}
}

func TestAIActionInstructionParams(t *testing.T) {
	tr := aiActionByName("translate")

	// 指定参数：译文里不能残留占位符。
	got := aiActionInstruction(tr, map[string]string{"lang": "日文"})
	if strings.Contains(got, "{lang}") {
		t.Fatalf("占位符没替换: %q", got)
	}
	if !strings.Contains(got, "日文") {
		t.Errorf("参数值没进提示词: %q", got)
	}

	// 不传参数：用默认值，仍然不许残留占位符（否则模型会把 {lang} 当正文）。
	got = aiActionInstruction(tr, nil)
	if strings.Contains(got, "{lang}") {
		t.Fatalf("缺省参数时占位符没替换: %q", got)
	}
	if !strings.Contains(got, "中文") {
		t.Errorf("缺省应译成中文: %q", got)
	}

	sm := aiActionByName("summarize")
	if got := aiActionInstruction(sm, nil); strings.Contains(got, "{scope}") {
		t.Errorf("总结的范围占位符没替换: %q", got)
	}

	// 附加约束以分隔线开头：调用方直接拼在 user 消息尾部，不能和正文粘连。
	if !strings.HasPrefix(got, "\n\n---\n") {
		t.Errorf("约束应以分隔线开头，实际 %q", got[:min(12, len(got))])
	}
}

// 四个动作的提示词都必须非空且风格一致（都要求「只输出结果、不要解释过程」）。
func TestAIActionPromptsLoaded(t *testing.T) {
	want := []string{aiActionExplain, aiActionTranslate, aiActionSummarize, aiActionReview}
	for _, name := range want {
		a := aiActionByName(name)
		if strings.TrimSpace(a.Prompt) == "" {
			t.Errorf("%s 的提示词为空（检查 prompts/actions/%s.md）", name, name)
		}
	}
	if len(aiActions) != 6 {
		t.Errorf("动作注册表应为 6 项（5 个动作 + 帮助），实际 %d", len(aiActions))
	}
}

// 落地页快捷方式：新建会话时选的「翻译/解释/总结」必须变成**一次性动作**，
// 而不是把 mode 存进库（那会导致之后每一句都被翻译）。
func TestFirstTurnActionNormalization(t *testing.T) {
	if got := firstTurnMode("", "translate"); got != "chat" {
		t.Errorf("新建会话应落 chat，实际 %q", got)
	}
	if got := firstTurnAction("", "translate", ""); got != aiActionTranslate {
		t.Errorf("应把 mode 提升成动作，实际 %q", got)
	}
	// 已有会话：保持原样（老数据靠 aiActionNameFor 回填动作）。
	if got := firstTurnMode("conv-1", "translate"); got != "translate" {
		t.Errorf("老会话不该改写 mode，实际 %q", got)
	}
	if got := firstTurnAction("conv-1", "translate", ""); got != "" {
		t.Errorf("老会话不该凭空造动作，实际 %q", got)
	}
	// 显式传了动作就优先用它，不看 mode。
	if got := firstTurnAction("", "summarize", "review"); got != "review" {
		t.Errorf("显式动作应优先，实际 %q", got)
	}
	if got := firstTurnMode("", "chat"); got != "chat" {
		t.Errorf("普通对话不该被改，实际 %q", got)
	}
}
