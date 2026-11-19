package ai

// 一次性动作（消息级）：解释 / 翻译 / 总结 / 审阅。
//
// 为什么不再做成「模式」：这四个东西本质是「对话 + 一条约束」，而模式是
// **会话级属性**（落在 ai_conversations.mode）——用户翻完一段代码想接着
// 让它改文件，就得先切回去；切一次还会连带工具面与提示词一起跳变。
//
// 现在它们只在**这一条消息**上生效：拼一段 instruction 进去，不改会话属性、
// 不改工具面，发完即失效。会话级只剩两档能力（助手 / 只读），见 ai_mode.go。

import (
	"strings"
)

// AI 动作名（同时是落库 ai_messages.action 的值）。
const (
	aiActionChat      = "chat" // 默认：不带额外约束
	aiActionExplain   = "explain"
	aiActionTranslate = "translate"
	aiActionSummarize = "summarize"
	aiActionReview    = "review"
	// aiActionHelp 帮助（不是消息级动作意义上的「任务形态」，而是一次知识注入）：
	// 把功能清单作为这一条消息的附加说明，等价于用户点了「帮助」按钮再提问。
	aiActionHelp = "help"
)

// aiAction 一个动作的定义。
type aiAction struct {
	Name string
	// Prompt 动作提示词（//go:embed 来自 prompts/actions/*.md），空 = 无额外约束。
	Prompt string
	// Params 该动作支持的参数名（用于占位替换与前端表单）。
	Params []string
}

// aiActions 动作注册表。顺序即界面上的顺序。
var aiActions = []aiAction{
	{Name: aiActionChat},
	{Name: aiActionExplain, Prompt: loadActionPrompt("explain")},
	{Name: aiActionTranslate, Prompt: loadActionPrompt("translate"), Params: []string{"lang"}},
	{Name: aiActionSummarize, Prompt: loadActionPrompt("summarize"), Params: []string{"scope"}},
	{Name: aiActionReview, Prompt: loadActionPrompt("review")},
	// help 不出现在 chip 行里，只由「帮助」按钮触发；提示词直接复用功能清单。
	{Name: aiActionHelp, Prompt: loadModePrompt("tutorial")},
}

// aiActionByName 按名字找动作；认不出来就当普通对话（旧数据、脏数据都走这里）。
func aiActionByName(name string) aiAction {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, a := range aiActions {
		if a.Name == n {
			return a
		}
	}
	return aiAction{Name: aiActionChat}
}

// aiActionInstruction 生成要拼进这条消息的约束文本；不需要时返回空串。
//
// 拼在 **user 消息尾部**而不是 system：动作只对「这一次输入」有效，
// 放进 system 会污染后面每一轮（那正是旧的模式实现最别扭的地方）。
//
// params 为动作参数（如翻译的目标语言），未提供的用默认值兜底：
// 缺少参数时宁可给默认可用的行为，也不要让模型反问你「翻译成什么语言」。
func aiActionInstruction(action aiAction, params map[string]string) string {
	prompt := strings.TrimSpace(action.Prompt)
	if prompt == "" {
		return ""
	}
	for _, key := range action.Params {
		v := strings.TrimSpace(params[key])
		if v == "" {
			v = aiActionDefaultParam(action.Name, key)
		}
		prompt = strings.ReplaceAll(prompt, "{"+key+"}", v)
	}
	// 兜底：万一注册表里漏了 Params 但正文有占位符，别把 {lang} 原样发给模型。
	if strings.Contains(prompt, "{") {
		for _, key := range []string{"lang", "scope"} {
			if strings.Contains(prompt, "{"+key+"}") {
				prompt = strings.ReplaceAll(prompt, "{"+key+"}", aiActionDefaultParam(action.Name, key))
			}
		}
	}
	return "\n\n---\n" + prompt
}

// aiActionDefaultParam 参数缺省值。
func aiActionDefaultParam(action, key string) string {
	switch key {
	case "lang":
		// 不指定时按「译成中文」处理：这是中文用户的默认预期，
		// 且指令里已写了「中文则译英文」的对称规则，不会把中文译成中文。
		return "中文（若原文已是中文，则译为英文）"
	case "scope":
		return "用户本次提供的内容"
	}
	_ = action
	return ""
}

// aiActionNeedsTools 该动作是否需要工具面。
//
// 目前四个动作都是「纯文本产出」，理论上不需要工具；但仍保留工具面，
// 因为用户完全可能先要求「总结这个仓库」再让它读文件。真正收窄工具面的
// 只有只读档（安全边界），不是动作。
func aiActionNeedsTools(action aiAction) bool {
	return action.Name == aiActionChat
}

// aiActionNameFor 解析一次请求的动作名（含旧 mode 回填）。
//
// 单独成函数是因为 runAgent 里的 saveSegment 是**在动作解析之前定义的闭包**，
// 它读不到后声明的局部变量；用一个纯函数就不会有作用域问题，也方便测试。
func aiActionNameFor(action, mode string) string {
	if n := strings.TrimSpace(action); n != "" {
		return aiActionByName(n).Name
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case aiActionExplain, aiActionTranslate, aiActionSummarize, aiActionReview:
		return strings.ToLower(strings.TrimSpace(mode))
	}
	return aiActionChat
}

// aiPromptFor 按 mode 取 system prompt（新语义下的兼容层）。
//
// explain / translate / summarize 已经从「模式」降级为一次性动作，
// 它们不该再带来一份自己的 system prompt——约束现在拼在那条 user 消息里。
// 映射到 chat 的提示词（人设 + 工作方式 + 派活）即可。
//
// tutorial 与 plan 暂不改：tutorial 的功能清单是「知识包」，按需注入是后续项；
// plan 是旧只读会话，保留原提示词才能照原样回放。
func aiPromptFor(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case aiActionExplain, aiActionTranslate, aiActionSummarize:
		return aiModePrompts["chat"]
	}
	if p, ok := aiModePrompts[strings.ToLower(strings.TrimSpace(mode))]; ok && strings.TrimSpace(p) != "" {
		return p
	}
	return aiModePrompts["chat"]
}
