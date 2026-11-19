package ai

// 按模型自动决定单次输出的 token 上限（max_tokens）。
//
// 背景：以前 `MaxTokens` 缺省一律 8192。这个数对 gpt-4o-mini 够用，对
// claude/gpt-5 这类「思考 + 正文共用输出预算」的模型就偏小——一次长回答写到
// 一半被 length 截断，用户看到的是「话说一半停了」。反过来，把它配到 200k 又会
// 把输入空间挤掉（contextLimitsOf 里有 w/4 的硬夹）。
//
// 能不能「从模型自动获取」：**上游 API 基本不提供**。OpenAI 兼容的 /v1/models
// 只回 id，没有上下文/输出上限；OpenRouter 的 /models 会带 context_length 与
// top_provider.max_completion_tokens，Groq/Together 部分带 context_length。
// 所以工程上的正解是两层：
//
//  1. 静态能力表（这里）：按模型名子串匹配，覆盖主流系列，永远可用、零延迟；
//  2. 运行期学习（learnedWindows 已有同类机制）：真撞了 400 拿到上游报的窗口，
//     或 length 截断后抬高（raisedMaxTokensOnLength），都会反过来修正。
//
// 也就是说：不指望 API 告诉我们，而是「先给一个合理值，再让真实反馈修正它」。

import "strings"

// aiModelMaxOutput 静态能力表。
//
// 数值取「官方文档的 max output tokens」，并统一偏保守：宁可少要一点，
// 也不要因为预扣过大让上游直接 400（max_tokens 是预扣，不是上限）。
// match 为空表示兜底项。
type aiModelMaxOutput struct {
	match  []string
	tokens int
}

var aiModelMaxOutputs = []aiModelMaxOutput{
	// OpenAI
	{[]string{"gpt-5", "o3", "o4"}, 32768},
	{[]string{"gpt-4.1"}, 16384},
	{[]string{"gpt-4o", "gpt-4o-mini", "chatgpt-4o"}, 16384},
	{[]string{"gpt-4-turbo", "gpt-4-1106", "gpt-4-0125"}, 4096},
	{[]string{"gpt-4"}, 8192},
	{[]string{"gpt-3.5"}, 4096},
	// Anthropic（claude 的 max_tokens 必须显式给，且是硬上限）
	{[]string{"claude-4", "claude-sonnet-4", "claude-opus-4", "claude-3-7"}, 32768},
	{[]string{"claude-3-5"}, 8192},
	{[]string{"claude-3"}, 4096},
	// DeepSeek：thinking 与正文共用输出预算，reasoner 尤其要留够。
	// v4 系列是 1M 窗口 + 默认开思考，8192 不够写长文档（思考先吃掉一半），
	// 按官方建议给 32768——窗口 1M 时 /4 的夹逼也不会削它。
	{[]string{"deepseek-v4"}, 32768},
	{[]string{"deepseek-reasoner", "deepseek-r1"}, 8192},
	{[]string{"deepseek"}, 8192},
	// Google
	{[]string{"gemini-2.5"}, 65536},
	{[]string{"gemini-2"}, 8192},
	{[]string{"gemini-1.5"}, 8192},
	// 国产系列
	{[]string{"qwen", "qwq"}, 8192},
	{[]string{"kimi", "moonshot"}, 8192},
	{[]string{"glm"}, 4096},
	{[]string{"ernie", "hunyuan", "doubao", "spark"}, 8192},
	// 开源
	{[]string{"llama-3", "llama3"}, 8192},
	{[]string{"mistral", "mixtral"}, 8192},
}

// autoMaxTokensOf 按模型名给出建议的输出上限；识别不出来时用 aiDefaultMax。
//
// window 是模型上下文窗口（见 contextWindowOf）：输出预算不能超过窗口的 1/4，
// 否则「预扣」会把输入空间挤没。这条夹逼与 contextLimitsOf 保持一致。
func autoMaxTokensOf(model string, window int) int {
	name := strings.ToLower(strings.TrimSpace(model))
	out := aiDefaultMax
	// 最长命中：deepseek-v4.1-flash 必须赢过 deepseek，gpt-4o-mini 必须赢过 gpt-4o。
	// 与 ai_context.go 的 contextWindowOf 同一套规则——两张表按同一种方式匹配，
	// 才不会出现「窗口算对了、输出算错了」这种只在某个模型上复现的怪事。
	bestLen := -1
	for _, c := range aiModelMaxOutputs {
		for _, m := range c.match {
			if !strings.Contains(name, m) {
				continue
			}
			if len(m) > bestLen {
				out, bestLen = c.tokens, len(m)
			}
		}
	}
	if window > 0 && out > window/4 {
		out = window / 4
	}
	if out < 256 {
		// 窗口本身极小的模型：给一个能写出一句话的下限，别出现 0。
		out = 256
	}
	return out
}
