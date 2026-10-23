package clipboard

import (
	"strings"
	"time"

	"quickdock/internal/aiutil"
	"quickdock/services"
	"quickdock/services/ai"
)

// 剪贴板场景的 AI 问答：把「用户选中的剪贴板文本」交给模型，拿回处理后的结果。
//
// 与 SSH 场景（services/sshx/ai.go）的关系：
//   - **共用** internal/aiutil 的围栏解析与分段结构（判定"什么算命令"只有一份实现）。
//   - **不共用**风险规则表：剪贴板场景的输出是给用户看的文本/代码，
//     不存在"在生产机上执行"这种前提，风险标注在这里没有意义。
//
// 三个预置动作（翻译 / 摘要 / 格式化）覆盖剪贴板里绝大多数内容的处理诉求。
// 用户也可以自由提问——两个入口共用同一套后端，只是提示词不同。
const (
	// ClipboardAiTranslate 翻译成中文（原文已是中文则译成英文）。
	ClipboardAiTranslate = "translate"
	// ClipboardAiSummarize 提取要点。
	ClipboardAiSummarize = "summarize"
	// ClipboardAiFormat 整理结构与排版。
	ClipboardAiFormat = "format"
)

const (
	// clipboardAiTextMaxRune 剪贴板正文送入模型的长度上限。
	//
	// 取 3000 与 SSH 屏显同量级：剪贴板里常是整段文档，超长时截断尾部
	// （保留最近复制的部分）比截断头部更符合直觉——用户刚复制的通常在末尾。
	clipboardAiTextMaxRune = 3000

	// clipboardAiQuestionMaxRune 用户自由提问的长度上限。
	clipboardAiQuestionMaxRune = 500

	// clipboardAiTimeoutSec 一次问答的超时。文本转换不需要长思考。
	clipboardAiTimeoutSec = 45

	// clipboardAiHistoryMaxTurns 多轮追问时最多带回的历史轮次。
	clipboardAiHistoryMaxTurns = 6
)

// clipboardAiSystemPrompt 剪贴板场景的系统提示词。
//
// 写法上的要点：
//   - **要求模型自己声明前提**，不要假装确定。剪贴板内容常常是残缺的
//     （截断的日志、没有上下文的报错），模型看不见的部分应当明说。
//   - **只输出处理结果**，不要复述原文。用户要的是译文/摘要，
//     前面再贴一遍原文纯属噪音。
//   - 代码类内容放围栏里（用 mdFence 拼接：反引号无法在 Go 原始字符串中转义）。
var clipboardAiSystemPrompt = strings.Join([]string{
	"你是一个文本处理助手，服务于一个剪贴板历史工具。",
	"",
	"用户会给你一段剪贴板内容，并说明想要什么处理结果。请遵守：",
	"",
	"1. 直接输出处理结果，不要复述原文，不要写「好的，以下是…」这类开场白。",
	"2. 禁止臆造：原文里没有的信息不要补充。原文残缺（被截断、缺少上下文）时，",
	"   在结果开头用一句话说明缺了什么，而不是自行猜测后当成事实。",
	"3. 需要输出代码、配置、命令或结构化数据时，放进 " + mdFence + " 代码块，",
	"   并在代码块语言标注里写明语言（如 " + mdFence + "json）。",
	"4. 保留原文的关键信息，不要为了简洁而丢掉数字、路径、报错码这类细节。",
	"5. 用中文回答（除非用户明确要求其它语言）。",
}, "\n")

// mdFence 代码块围栏标记。
//
// 用拼接而非直接写在原始字符串里：反引号无法在 Go 的原始字符串（反引号界定）
// 中转义，直接写会提前闭合字符串并让后续中文变成语法错误。
const mdFence = "```"

// clipboardAiActionPrompt 三个预置动作各自的补充指令。
//
// 分成三段而不是一个带变量的长句：每段都要能独立读懂，
// 将来单独调整某一段不会牵连其它两段。
var clipboardAiActionPrompt = map[string]string{
	ClipboardAiTranslate: "请把上面的内容翻译成自然流畅的中文；如果原文已经是中文，则翻译成英文。只输出译文。",
	ClipboardAiSummarize: "请把上面的内容提炼成要点摘要，用简洁的中文分条列出核心信息，不要展开解释。只输出摘要。",
	ClipboardAiFormat: "请把上面的内容整理清楚：修正错别字与标点，理顺段落结构，保留全部原始信息（数字、路径、报错码一个都不能丢），" +
		"该分条的地方分条，该用代码块的地方用代码块。只输出整理后的内容。",
}

// ClipboardAiAskRequest 剪贴板场景 AI 问答入参。
type ClipboardAiAskRequest struct {
	// Text 待处理的剪贴板文本（必填）。
	Text string `json:"text"`
	// Action 预置动作：translate / summarize / format。留空表示走自由提问。
	Action string `json:"action"`
	// Question 用户自由提问。与 Action 二选一：填了 Question 时以 Question 为准。
	Question string `json:"question"`
	// History 可选的历史问答，用于多轮追问。每项取 text 字段作为纯文本。
	History []string `json:"history"`
	// StreamID 流式标识。非空时后端经 Wails 事件 ai:ask-delta 逐段推送增量；空则退化一次性返回。
	StreamID string `json:"streamId"`
}

// ClipboardAiAskResult 剪贴板场景 AI 问答返回。
type ClipboardAiAskResult struct {
	// Answer 模型返回的完整正文（含围栏），前端据此渲染文字部分。
	Answer string `json:"answer"`
	// Segments 按模型原始顺序排列的内容分段。
	//
	// 与 SSH 场景共用 aiutil.ScenarioSegment 结构：text 段是说明文字，
	// cmd 段是围栏块（这里是代码/命令，不是"要执行的命令"）。
	Segments []aiutil.ScenarioSegment `json:"segments"`
	// Model 实际使用的模型名。
	Model string `json:"model"`
	// ElapsedMs 耗时毫秒。
	ElapsedMs int64 `json:"elapsedMs"`
}

// AskAI 就一段剪贴板文本向 AI 提问。
//
// 只做「组装上下文 + 调用 + 解析围栏」，不写任何数据：
// 不落库（不污染用户的 AI 对话历史）、不碰剪贴板内容本身。
func (a *ClipboardService) AskAI(req ClipboardAiAskRequest) *services.ApiResult {
	if a.App.AskAIFn == nil {
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

	if n := len([]rune(question)); n > clipboardAiQuestionMaxRune {
		question = string([]rune(question)[:clipboardAiQuestionMaxRune])
	}

	prompt := buildClipboardAiPrompt(text, action, question, req.History)

	// 流式：增量经 Wails 事件 ai:ask-delta 推给前端（batch ~40 rune 一次 IPC）。
	// 返回后再 flush 一次，避免结尾片段丢失。
	var deltaBuf strings.Builder
	emitDelta := func() {
		if deltaBuf.Len() == 0 {
			return
		}
		if a.App.App() != nil {
			a.App.App().Event.Emit("ai:ask-delta", map[string]interface{}{"id": req.StreamID, "delta": deltaBuf.String()})
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
	out, model, err := a.App.AskAIFnStream(clipboardAiSystemPrompt, prompt, clipboardAiTimeoutSec, onToken)
	if onToken != nil {
		emitDelta()
	}
	if err != nil {
		return services.Fail(err)
	}

	return services.Ok(ClipboardAiAskResult{
		Answer: out,
		// 规则表传 nil：剪贴板场景的围栏块是给人看的代码，没有执行风险。
		Segments:  aiutil.ExtractSegments(out, nil),
		Model:     model,
		ElapsedMs: time.Since(start).Milliseconds(),
	})
}

// buildClipboardAiPrompt 组装用户提示词。
//
// 结构固定为「正文 → 任务说明 → 本次问题」，与 SSH 场景同一个道理：
// **本次问题放最末尾**——模型的注意力落在最后，而 AIAsk 超预算时是
// **从头截断**（见 services/ai.AskMaxUserRune），末尾的问题最先被砍。
// 所以预算必须自己按优先级分配，不能依赖后端截断。
//
// 优先级：**本次问题 > 正文 > 历史**。
// 正文丢了模型没东西可处理，问题丢了模型不知道要干什么；
// 历史少了最多让多轮追问没那么连贯，超预算时丢最旧的。
func buildClipboardAiPrompt(text, action, question string, history []string) string {
	sections := []aiutil.Section{}
	// 正文：超长时保留**尾部**——用户刚复制的内容通常在末尾。
	if body := strings.TrimSpace(text); body != "" {
		if n := len([]rune(body)); n > clipboardAiTextMaxRune {
			body = aiutil.TrimRunes(body, clipboardAiTextMaxRune, true)
			body = "…（前面的内容已省略）\n" + body
		}
		sections = append(sections, aiutil.Section{
			Title:    "待处理的内容",
			Body:     mdFence + "\n" + body + "\n" + mdFence,
			KeepTail: true,
		})
	}

	// 任务说明：预置动作的指令 + 用户的自由提问。
	//
	// 两者**组合**而不是二选一：追问时（"再短一点"）只带用户那句，
	// 模型就不知道当前在做什么——它会看到一句没头没尾的要求。
	// 所以动作指令始终带上，用户提问追加在后面。
	var parts []string
	if p, ok := clipboardAiActionPrompt[action]; ok {
		parts = append(parts, p)
	}
	if question != "" {
		// 有动作指令时是"追加要求"，纯自由提问时就是问题本身——
		// 加个"补充要求："前缀在后者情况下读起来莫名其妙。
		if len(parts) > 0 {
			parts = append(parts, "在此基础上，再满足这一点："+question)
		} else {
			parts = append(parts, question)
		}
	}

	// 预算分配、历史打包、"指令必须活下来"都交给 aiutil.BuildPrompt。
	return aiutil.BuildPrompt(aiutil.Prompt{
		Max:             ai.AskMaxUserRune,
		Sections:        sections,
		History:         history,
		HistoryTitle:    "之前的对话",
		HistoryItemMax:  500,
		HistoryMaxTurns: clipboardAiHistoryMaxTurns,
		Task:            strings.Join(parts, "\n"),
		TaskTitle:       "本次要求",
	})
}
