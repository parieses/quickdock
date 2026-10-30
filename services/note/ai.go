package note

import (
	"strings"
	"time"

	"quickdock/internal/aiutil"
	"quickdock/services"
	aisvc "quickdock/services/ai"
)

// 笔记浮窗的 AI 能力。
//
// 历史上的做法是「后端广播文本 → 主窗口 AI 页接收 → 用户在那儿提问」：
// 先把笔记浮窗隐藏、把主窗口拉起来，再把文本经 note:ask-ai 事件送过去。
// 那条链路的问题不是能不能用，而是三个窗口状态耦合在一起——
// 浮窗要不要隐藏、主窗口要不要聚焦、事件到达时 AI 页是否已挂载，
// 任何一环出岔子都表现为"点了没反应"，且各窗口的 JS 状态互不可见，很难查。
//
// 现在改成和 SSH / 剪贴板一致的就地提问：浮窗里直接弹出面板，
// 上下文（划选文本或整篇笔记）随请求走，一次调用拿回答案。
// 只保留"不落库"这条性质——笔记问的是一次性问题，不该进 AI 对话历史。

const (
	// noteAiTextMaxRune 笔记正文送入模型的长度上限。
	//
	// 比剪贴板（3000）略宽松：笔记是用户自己长期攒下的内容，结构完整，
	// 但也不能无上限——预算一共只有 ai.AskMaxUserRune，正文吃满就没地方放问题了。
	noteAiTextMaxRune = 3200
	// noteAiHistoryMaxTurns 多轮追问时最多带回的历史轮次。
	noteAiHistoryMaxTurns = 6
	// noteAiQuestionMaxRune 单次提问长度上限（用户输入，不是笔记正文）。
	noteAiQuestionMaxRune = 1000
	// noteAiTimeoutSec 超时秒数。写作类任务（润色/续写）比翻译慢，给宽一点。
	noteAiTimeoutSec = 90

	noteMdFence = "```"
)

// 预置动作。前后端共用同一组字符串，改名前先确认前端同步。
const (
	NoteAiSummarize = "summarize"
	NoteAiPolish    = "polish"
	NoteAiContinue  = "continue"
	NoteAiTranslate = "translate"
)

// noteAiActionPrompt 预置动作对应的指令。
//
// 每条都明确"只做这件事"，并要求保持原语言/不加解释：
// 不写死的话模型喜欢先来一段"好的，我来帮你…"，用户还得自己从寒暄里挑内容。
var noteAiActionPrompt = map[string]string{
	NoteAiSummarize: "总结这段笔记的要点，用简洁的条目列出，不要复述原文。",
	NoteAiPolish:    "润色这段笔记：修正错别字与语病，让表达更清楚，保持原意与原有结构，不要扩写。",
	NoteAiContinue:  "顺着这段笔记已有的思路与语气继续写下去，只输出新增的内容，不要重复已有部分。",
	NoteAiTranslate: "翻译这段内容。默认译成英文；若原文主要是英文则译成中文。只输出译文，不要解释。",
}

// noteAiSystemPrompt 笔记场景的系统提示词。
const noteAiSystemPrompt = `你是写作与整理助手，帮助用户处理他们自己的笔记内容。

规则：
1. 只处理用户给出的内容，不要引入外部事实，也不要编造原文没有的信息。
2. 保持原文的语言（除非用户明确要求翻译）。
3. 直接给出结果，不要寒暄、不要复述要求、不要解释你做了什么。
4. 需要给出代码或格式化文本时，用 Markdown 围栏代码块包起来。`

// NoteAiAskRequest 笔记场景的 AI 问答请求。
type NoteAiAskRequest struct {
	// Text 待处理的笔记内容（划选片段或整篇）。
	Text string `json:"text"`
	// Action 预置动作：summarize / polish / continue / translate。留空表示自由提问。
	Action string `json:"action"`
	// Question 用户自由提问。与 Action 组合：追问时两者一起送。
	Question string `json:"question"`
	// History 可选的历史问答，用于多轮追问。
	History []string `json:"history"`
	// StreamID 流式标识。非空时后端经 Wails 事件 ai:ask-delta 逐段推送增量。
	StreamID string `json:"streamId"`
}

// NoteAiAskResult 笔记场景 AI 问答返回。
type NoteAiAskResult struct {
	Answer string `json:"answer"`
	// Segments 按模型原始顺序排列的内容分段（文字 / 代码块）。
	Segments []aiutil.ScenarioSegment `json:"segments"`
	Model    string                   `json:"model"`
	// ElapsedMs 耗时毫秒。
	ElapsedMs int64 `json:"elapsedMs"`
}

// AskAI 就一段笔记内容向 AI 提问。
//
// 只做「组装上下文 + 调用 + 解析围栏」，不写任何数据：
// 不改笔记、不落库、不动窗口。浮窗与主窗口的耦合到此为止。
func (s *NoteService) AskAI(req NoteAiAskRequest) *services.ApiResult {
	if s.App.AskAIFn == nil {
		return services.FailMsg("AI 能力不可用")
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		return services.FailMsg("内容为空")
	}

	action := strings.TrimSpace(req.Action)
	question := strings.TrimSpace(req.Question)
	if action == "" && question == "" {
		return services.FailMsg("请指定操作或问题")
	}
	if n := len([]rune(question)); n > noteAiQuestionMaxRune {
		question = string([]rune(question)[:noteAiQuestionMaxRune])
	}

	prompt := buildNoteAiPrompt(text, action, question, req.History)

	// 流式：与 SSH / 剪贴板同一条通道（ai:ask-delta，按 id 区分），
	// 合批 ~40 字节，避免逐 token 推事件打满 Wails 事件总线。
	var deltaBuf strings.Builder
	emitDelta := func() {
		if deltaBuf.Len() == 0 {
			return
		}
		if s.App.App() != nil {
			s.App.App().Event.Emit("ai:ask-delta", map[string]interface{}{
				"id":    req.StreamID,
				"delta": deltaBuf.String(),
			})
		}
		deltaBuf.Reset()
	}
	var onToken func(string)
	if req.StreamID != "" {
		onToken = func(delta string) {
			deltaBuf.WriteString(delta)
			if deltaBuf.Len() >= 40 {
				emitDelta()
			}
		}
	}

	start := time.Now()
	out, model, err := s.App.AskAIFnStream(noteAiSystemPrompt, prompt, noteAiTimeoutSec, onToken)
	if onToken != nil {
		emitDelta()
	}
	if err != nil {
		return services.Fail(err)
	}

	return services.Ok(NoteAiAskResult{
		Answer: out,
		// 规则表传 nil：笔记里的围栏块是写作内容（代码示例、提纲），
		// 不是要在本机执行的命令，没有风险可标。
		Segments:  aiutil.ExtractSegments(out, nil),
		Model:     model,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// buildNoteAiPrompt 组装用户提示词。
//
// 结构：「笔记内容 → 历史 → 本次要求」，问题/指令永远在最后——
// 模型注意力落在末尾，而 AIAsk 超预算时从头截断，末尾最先被砍，
// 所以这里的预算分配必须自己算（见 aiutil.BuildPrompt）。
//
// 正文保留头部而不是尾部：笔记的开头通常是标题与结论，
// 与日志/终端（最新内容在末尾）正好相反。这也是它不能用
// 剪贴板那套 keepTail 的原因。
func buildNoteAiPrompt(text, action, question string, history []string) string {
	sections := []aiutil.Section{}

	if body := strings.TrimSpace(text); body != "" {
		if n := len([]rune(body)); n > noteAiTextMaxRune {
			body = aiutil.TrimRunes(body, noteAiTextMaxRune, false)
			body = body + "\n…（后面的内容已省略）"
		}
		sections = append(sections, aiutil.Section{
			Title: "笔记内容",
			Body:  noteMdFence + "\n" + body + "\n" + noteMdFence,
		})
	}

	// 动作指令与用户提问组合而不是二选一：
	// 追问（"再短一点"）时只带用户那句，模型就不知道当前在做什么。
	var parts []string
	if p, ok := noteAiActionPrompt[action]; ok {
		parts = append(parts, p)
	}
	if question != "" {
		if len(parts) > 0 {
			parts = append(parts, "在此基础上，再满足这一点："+question)
		} else {
			parts = append(parts, question)
		}
	}

	return aiutil.BuildPrompt(aiutil.Prompt{
		Max:             aisvc.AskMaxUserRune,
		Sections:        sections,
		History:         history,
		HistoryTitle:    "之前的对话",
		HistoryItemMax:  500,
		HistoryMaxTurns: noteAiHistoryMaxTurns,
		Task:            strings.Join(parts, "\n"),
		TaskTitle:       "本次要求",
	})
}
