package note

import (
	"strings"
	"testing"

	aisvc "quickdock/services/ai"
)

// 笔记 prompt 的行为约束与 SSH / 剪贴板一致（装配逻辑已收敛到 aiutil.BuildPrompt），
// 这里测的是本域自己的取舍：正文保留**头部**（笔记开头是标题与结论）、
// 动作指令与追问要**组合**、指令永远在最后。
func TestBuildNoteAiPrompt_Basic(t *testing.T) {
	got := buildNoteAiPrompt("今天评审了白板的协作协议", NoteAiSummarize, "", nil)

	if !strings.Contains(got, "## 笔记内容") {
		t.Errorf("缺少笔记正文小节:\n%s", got)
	}
	if !strings.Contains(got, noteAiActionPrompt[NoteAiSummarize]) {
		t.Errorf("缺少预置动作指令:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), noteAiActionPrompt[NoteAiSummarize]) {
		t.Errorf("指令应在末尾（模型注意力落在最后）:\n%s", got)
	}
}

func TestBuildNoteAiPrompt_ActionAndQuestionCombine(t *testing.T) {
	// 追问时两者都要在：只带用户那句，模型不知道当前在做什么。
	got := buildNoteAiPrompt("原始内容", NoteAiPolish, "再短一点", nil)

	actIdx := strings.Index(got, noteAiActionPrompt[NoteAiPolish])
	qIdx := strings.Index(got, "再短一点")
	if actIdx < 0 || qIdx < 0 {
		t.Fatalf("指令或追问丢失:\n%s", got)
	}
	if actIdx > qIdx {
		t.Errorf("指令应排在追问之前: act=%d q=%d", actIdx, qIdx)
	}
}

func TestBuildNoteAiPrompt_KeepsHead(t *testing.T) {
	// 笔记与日志相反：开头是标题/结论，超长时保留**头部**。
	long := "HEAD" + strings.Repeat("x", noteAiTextMaxRune+800) + "TAIL"
	got := buildNoteAiPrompt(long, NoteAiSummarize, "", nil)

	if !strings.Contains(got, "HEAD") {
		t.Errorf("笔记开头被裁掉了:\n%s", got[:120])
	}
	if strings.Contains(got, "TAIL") {
		t.Errorf("超长笔记应裁掉尾部")
	}
	if !strings.Contains(got, "已省略") {
		t.Errorf("裁剪后应留下省略标记，否则模型会以为内容到此结束")
	}
}

func TestBuildNoteAiPrompt_QuestionSurvivesBudget(t *testing.T) {
	// 回归：正文 + 历史很容易占满预算，而被砍掉的正是写在末尾的问题。
	text := strings.Repeat("笔记内容 ", 600) // 约 3000 rune
	var h []string
	for i := 0; i < noteAiHistoryMaxTurns; i++ {
		h = append(h, strings.Repeat("历史回答 ", 80))
	}
	q := "这段怎么改得更口语化"
	got := buildNoteAiPrompt(text, "", q, h)

	if !strings.HasSuffix(strings.TrimSpace(got), q) {
		t.Errorf("本次问题丢失（不在末尾）:\n…%s", got[len(got)-160:])
	}
	if n := len([]rune(got)); n > aisvc.AskMaxUserRune {
		t.Errorf("prompt 超预算: %d > %d", n, aisvc.AskMaxUserRune)
	}
}

func TestBuildNoteAiPrompt_EmptyTextKeepsTask(t *testing.T) {
	// 正文为空理论上不该走到（前端会拦），但裁剪逻辑不该崩，指令仍要保留。
	got := buildNoteAiPrompt("", NoteAiTranslate, "", nil)
	if !strings.Contains(got, noteAiActionPrompt[NoteAiTranslate]) {
		t.Errorf("正文为空时指令丢失:\n%s", got)
	}
}
