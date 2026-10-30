package ai

// 用量采集。
//
// 一条链路只能记一次，所以记录点放在每个入口（对话流 / 一次性问 / 标题 / 摘要），
// 而不是塞进 streamAIChat 里——那条函数同时被「带会话的对话」和「SSH 面板的一次性
// 提问」调用，塞进去就拿不到正确的 kind，也没法区分有会话与无会话。
//
// 早期版本只在 AI 页的对话流里记，于是 SSH 面板、剪贴板 AI、笔记 AI、插件 host.ai.ask
// 的消耗全部消失，统计面板的钱数只有实际的一部分。

import (
	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// 用量来源。kind 落库后在设置页按来源展示，取值改动要同步前端 i18n。
const (
	usageKindChat    = "chat"    // AI 页的多轮对话
	usageKindAsk     = "ask"     // 一次性调用：SSH 面板 / 剪贴板 AI / 笔记 AI / 插件 host.ai.ask
	usageKindTitle   = "title"   // 自动生成会话标题
	usageKindSummary = "summary" // 上下文压缩摘要
)

// openAIUsage OpenAI 兼容接口返回的 usage 对象。
//
// 缓存命中两家写法不同，都得认：
//   - OpenAI / 大多数兼容网关：prompt_tokens_details.cached_tokens
//   - DeepSeek：另给 prompt_cache_hit_tokens（没有 details 结构）
//
// 两者是同一份数据，不能相加；谁有值用谁。
type openAIUsage struct {
	PromptTokens         int `json:"prompt_tokens"`
	CompletionTokens     int `json:"completion_tokens"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
	PromptTokensDetails  *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (o *openAIUsage) toUsage() db.TokenUsage {
	if o == nil {
		return db.TokenUsage{}
	}
	cached := o.PromptCacheHitTokens
	if o.PromptTokensDetails != nil && o.PromptTokensDetails.CachedTokens > 0 {
		cached = o.PromptTokensDetails.CachedTokens
	}
	// 个别网关会把 cached 报得比 prompt 还大（口径混乱），夹一下避免命中率 >100%
	if cached > o.PromptTokens {
		cached = o.PromptTokens
	}
	return db.TokenUsage{Prompt: o.PromptTokens, Completion: o.CompletionTokens, Cached: cached}
}

// recordAIUsage 落库一次调用的用量。
//
// 「记了多少 token」和「这个对话累计多少 token」是两个概念，这里把边界钉死：
//   - 明细永远写（统计面板按它算钱）；
//   - 只有 chat 才累加到会话行（对话列表那一栏），标题生成 / 摘要压缩不算
//     一问一答 —— 否则列表里的数字会比「各条回复下方之和」大出一截。
//
// 失败只写日志、绝不影响回答：统计是附属功能，不能因为它把已经生成的内容搞丢。
func (a *AIService) recordAIUsage(convID, kind string, cfg AIProfile, u db.TokenUsage) {
	if a == nil || a.App == nil || a.App.DB == nil || u.Empty() {
		return
	}
	if err := a.App.DB.RecordAIUsage(convID, cfg.Model, kind, u); err != nil {
		logger.W("[ai] 记录用量失败 kind=%s model=%s: %v", kind, cfg.Model, err)
	}
	if kind != usageKindChat || convID == "" {
		return
	}
	if err := a.App.DB.AddAIConversationUsage(convID, cfg.Model, u, true); err != nil {
		logger.W("[ai] 累加会话用量失败 conv=%s: %v", convID, err)
	}
}

// addTokenUsage 累加多轮调用的用量。
//
// 直接相加而不是取最后一次：每轮都是完整的一次计费（上下文整份重发），
// 只留最后一轮会让「这条回复花了多少」少算一大截，而用户拿这个数字估预算。
func addTokenUsage(a, b db.TokenUsage) db.TokenUsage {
	return db.TokenUsage{
		Prompt:     a.Prompt + b.Prompt,
		Completion: a.Completion + b.Completion,
		Cached:     a.Cached + b.Cached,
	}
}

// recordAIChatTurn 记录多轮工具循环里的一轮模型调用。
//
// 与 recordAIUsage 的唯一差别在会话行：
//   - 明细（ai_usage）每轮一行 —— 那是真实的多次计费，少记就会系统性低估花费；
//   - 会话行的问答次数只在第一轮 +1 —— 用户问的是一句话，不是五句。
//     列表里那一栏叫「问答次数」，按轮累加会让它失去意义。
func (a *AIService) recordAIChatTurn(convID string, cfg AIProfile, u db.TokenUsage, first bool) {
	a.recordAIChatTurnScoped(convID, "", "", cfg, u, first)
}

// recordAIChatTurnScoped 同 recordAIChatTurn，额外把这次调用归到某次编排的
// run / node 上（用于「这次编排花了多少」的归集）。
//
// 必须逐轮带 runID：一次编排有很多次模型调用，只在结束时补一条总量的话，
// 统计面板里就查不出「哪个子 agent 最贵」，中途失败也留不下任何花费记录。
func (a *AIService) recordAIChatTurnScoped(convID, runID, nodeID string, cfg AIProfile, u db.TokenUsage, first bool) {
	if a == nil || a.App == nil || a.App.DB == nil || u.Empty() {
		return
	}
	if err := a.App.DB.RecordAIUsageScoped(convID, runID, nodeID, cfg.Model, usageKindChat, u); err != nil {
		logger.W("[ai] 记录用量失败 kind=chat model=%s: %v", cfg.Model, err)
	}
	if convID == "" {
		return
	}
	if err := a.App.DB.AddAIConversationUsage(convID, cfg.Model, u, first); err != nil {
		logger.W("[ai] 累加会话用量失败 conv=%s: %v", convID, err)
	}
}
