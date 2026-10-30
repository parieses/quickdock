package clipboard

import (
	"strings"
	"testing"

	"quickdock/services/ai"
)

// 剪贴板 AI 的核心风险是预算裁剪把用户的指令砍掉：
// AIAsk 超预算时是从头截断，而指令写在 prompt 末尾。
// 这组测试守的就是"问题必须活下来"这条底线。

func TestBuildClipboardAiPrompt(t *testing.T) {
	got := buildClipboardAiPrompt("hello world", ClipboardAiTranslate, "", nil)

	if !strings.Contains(got, "hello world") {
		t.Errorf("prompt 缺少待处理内容:\n%s", got)
	}
	if !strings.Contains(got, "翻译") {
		t.Errorf("prompt 缺少动作指令:\n%s", got)
	}
	// 指令在末尾：模型的注意力落在最后，且超预算时最后被砍的是它。
	if !strings.HasSuffix(strings.TrimSpace(got), clipboardAiActionPrompt[ClipboardAiTranslate]) {
		t.Errorf("动作指令应在 prompt 末尾:\n%s", got)
	}
}

func TestBuildClipboardAiPrompt_QuestionWins(t *testing.T) {
	// 纯自由提问（没有 action）：任务说明就是用户那句本身。
	got := buildClipboardAiPrompt("原始内容", "", "只提取人名", nil)
	if !strings.Contains(got, "只提取人名") {
		t.Errorf("prompt 缺少用户问题:\n%s", got)
	}
}

func TestBuildClipboardAiPrompt_ActionAndQuestionCombined(t *testing.T) {
	// 追问（"再短一点"）时，动作指令必须保留：
	// 只带用户那句的话，模型看到的是一句没头没尾的要求。
	got := buildClipboardAiPrompt("原始内容", ClipboardAiSummarize, "再短一点", nil)
	if !strings.Contains(got, clipboardAiActionPrompt[ClipboardAiSummarize]) {
		t.Errorf("追问时丢失了动作指令:\n%s", got)
	}
	if !strings.Contains(got, "再短一点") {
		t.Errorf("追问时丢失了用户问题:\n%s", got)
	}
	// 指令在前、追问在后：模型先知道要做什么，再知道额外要求。
	actIdx := strings.Index(got, clipboardAiActionPrompt[ClipboardAiSummarize])
	qIdx := strings.Index(got, "再短一点")
	if actIdx < 0 || qIdx < 0 || actIdx > qIdx {
		t.Errorf("动作指令应排在追问之前: act=%d q=%d", actIdx, qIdx)
	}
}

func TestBuildClipboardAiPrompt_TrimsLongText(t *testing.T) {
	// 超长正文必须裁剪，且保留尾部（用户刚复制的通常在末尾）。
	long := strings.Repeat("x", clipboardAiTextMaxRune+500) + "TAIL"
	got := buildClipboardAiPrompt(long, ClipboardAiSummarize, "", nil)

	if n := len([]rune(got)); n > ai.AskMaxUserRune {
		t.Errorf("prompt 超预算: %d", n)
	}
	if !strings.Contains(got, "TAIL") {
		t.Errorf("尾部内容被裁掉了:\n%s", got[len(got)-200:])
	}
	if strings.Count(got, "x") > clipboardAiTextMaxRune+100 {
		t.Errorf("超长正文未被有效裁剪")
	}
}

func TestBuildClipboardAiPrompt_QuestionSurvivesBudget(t *testing.T) {
	// 回归：正文 + 多轮历史很容易占满预算，
	// 而本次问题写在末尾，超预算时被砍掉的正是用户的问题本身。
	text := strings.Repeat("待处理内容 ", 300) // 约 1500 rune
	var h []string
	for i := 0; i < clipboardAiHistoryMaxTurns; i++ {
		h = append(h, strings.Repeat("历史回答 ", 80)) // 每轮约 400 rune
	}
	q := "把这段翻译成英文"
	got := buildClipboardAiPrompt(text, "", q, h)

	if !strings.HasSuffix(strings.TrimSpace(got), q) {
		t.Errorf("本次问题丢失（不在 prompt 末尾）:\n…%s", got[len(got)-200:])
	}
	if n := len([]rune(got)); n > ai.AskMaxUserRune {
		t.Errorf("prompt 超预算: %d rune > %d", n, ai.AskMaxUserRune)
	}
}

func TestBuildClipboardAiPrompt_LimitsHistoryTurns(t *testing.T) {
	var h []string
	for i := 0; i < clipboardAiHistoryMaxTurns+5; i++ {
		h = append(h, strings.Repeat("h", 10))
	}
	got := buildClipboardAiPrompt("内容", ClipboardAiSummarize, "", h)
	// 只取末尾若干轮：更早的历史对当前问题没有帮助，还会误导模型。
	if strings.Count(got, "hhhhhhhhhh") > clipboardAiHistoryMaxTurns {
		t.Errorf("历史轮次未被限制:\n%s", got)
	}
}

func TestBuildClipboardAiPrompt_EmptyTextStillKeepsTask(t *testing.T) {
	// 正文为空（理论上不该走到，但裁剪逻辑不该崩）：指令仍要保留。
	got := buildClipboardAiPrompt("", ClipboardAiFormat, "", nil)
	if !strings.Contains(got, clipboardAiActionPrompt[ClipboardAiFormat]) {
		t.Errorf("正文为空时指令丢失:\n%s", got)
	}
}
