package ai

// AI 用量统计与花费估算。
//
// 数据源是 ai_usage 明细表（一次模型调用一行，见 db.RecordAIUsage）。
// 每行的日期、模型、来源都是当时写下的，所以按天、按模型、按来源、总额
// 四路统计口径一致，加起来必然相等 —— 这也是当初没有复用 ai_conversations
// 累加列的原因：那种 upsert 口径只能回答「这个会话一共多少」。
//
// 花费一律是估算：单价取内置价目表（见 ai_pricing.go），命中前缀缓存的
// 输入按缓存读取价单独折算（通常只有全价的 1/4 ~ 1/10，长会话命中率能到 99%，
// 不区分就会把花费算高一个数量级）。未收录的模型按 0 计但会单独汇报次数。

import (
	"sort"
	"time"

	"quickdock/services"
)

// AICostEntry 一行用量（Key 为模型名 / 日期 / 来源）
type AICostEntry struct {
	Key              string  `json:"key"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	TotalTokens      int     `json:"totalTokens"`
	EstimatedCost    float64 `json:"estimatedCost"` // 元
	// SavedByCache 命中缓存相对「全按输入价」省下的估算金额，仅用于展示
	SavedByCache float64 `json:"savedByCache"`
	Priced       bool    `json:"priced"` // false = 内置价目表未收录该模型
}

// AIUsageStats AI 用量统计结果（总额 + 按模型 + 按天 + 按来源）
type AIUsageStats struct {
	Days             int     `json:"days"` // 窗口天数（0 = 全部）
	Conversations    int     `json:"conversations"`
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CachedTokens     int     `json:"cachedTokens"`
	TotalTokens      int     `json:"totalTokens"`
	EstimatedCost    float64 `json:"estimatedCost"`
	SavedByCache     float64 `json:"savedByCache"`
	Currency         string  `json:"currency"`
	// UnpricedRequests：未收录模型上的调用次数（这些在总额里按 0 计）
	UnpricedRequests int           `json:"unpricedRequests"`
	Models           []AICostEntry `json:"models"`
	Daily            []AICostEntry `json:"daily"`
	// Sources 按来源拆分（对话 / 面板插件 / 标题 / 摘要），用来回答
	// 「我明明只在 SSH 里问了几句，为什么额度掉了」这类问题
	Sources []AICostEntry `json:"sources"`
}

// usageSince 窗口天数 → RFC3339 起点；days<=0 表示不限。
//
// 起点取当天 0 点再往前推 days-1 天，这样「近 7 天」包含今天在内正好 7 个自然日，
// 而不是「从此刻往前 168 小时」——后者会让今天的数字看着比昨天少一截。
func usageSince(days int) string {
	if days <= 0 {
		return ""
	}
	n := time.Now()
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).
		AddDate(0, 0, -(days - 1))
	return start.Format(time.RFC3339)
}

// AIApprovalToolStat 按工具聚合的一行审批结果。
type AIApprovalToolStat struct {
	Tool  string `json:"tool"`
	Allow int    `json:"allow"`
	Deny  int    `json:"deny"`
	Auto  int    `json:"auto"`
}

// AIApprovalStatsResult 给前端的审批统计。
//
// 字段名用 camelCase，与 AIUsageStats 一致——两者并排显示在同一个面板里，
// 一个 snake_case 一个 camelCase 会让前端类型定义变成两套规矩。
type AIApprovalStatsResult struct {
	Allow  int                  `json:"allow"`
	Deny   int                  `json:"deny"`
	Auto   int                  `json:"auto"`
	ByTool []AIApprovalToolStat `json:"byTool"`
}

// AIApprovalStats 统计指定窗口内的审批结果（days<=0 表示全部时间）。
//
// 独立一个方法而不是塞进 AIUsageStats 的返回值：那是成本数据，这是「闸门调得
// 对不对」的诊断数据，混在一起会让用量响应多出一块和钱无关的字段，
// 而前端两个面板的刷新时机本来就不同。
func (a *AIService) AIApprovalStats(days int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	sum, err := a.App.DB.AIApprovalSummary(usageSince(days))
	if err != nil {
		return services.Fail(err)
	}
	out := AIApprovalStatsResult{
		Allow: sum.Allow, Deny: sum.Deny, Auto: sum.Auto,
		ByTool: make([]AIApprovalToolStat, 0, len(sum.ByTool)),
	}
	for _, s := range sum.ByTool {
		out.ByTool = append(out.ByTool, AIApprovalToolStat{
			Tool: s.Tool, Allow: s.Allow, Deny: s.Deny, Auto: s.Auto,
		})
	}
	return services.Ok(out)
}

// usageBucket 聚合中间态
type usageBucket struct {
	requests, prompt, completion, cached int
	cost, saved                          float64
	// unpriced 这个桶里「模型没进价目表」的调用次数。
	// 每条分组只有一个模型，所以这个数要么等于 requests、要么等于 0；
	// 按天 / 按来源的桶会混合多个模型，靠它才能回答「这一天的钱算全了没有」。
	unpriced int
}

// AIUsageStats 统计指定窗口内的 AI 用量（days<=0 表示全部时间）。
func (a *AIService) AIUsageStats(days int) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	since := usageSince(days)

	groups, err := a.App.DB.ListAIUsageGroups(since)
	if err != nil {
		return services.Fail(err)
	}

	// 一次分组查询喂给四路聚合：分头查容易各自写错过滤条件，
	// 界面上就会出现「总额 ≠ 各模型之和」这种自相矛盾的数字。
	byModel := map[string]*usageBucket{}
	byDay := map[string]*usageBucket{}
	byKind := map[string]*usageBucket{}
	priced := map[string]bool{}
	total := &usageBucket{}
	unpriced := 0

	get := func(m map[string]*usageBucket, k string) *usageBucket {
		b := m[k]
		if b == nil {
			b = &usageBucket{}
			m[k] = b
		}
		return b
	}

	for _, g := range groups {
		cost, ok := estimateCost(g.Model, g.PromptTokens, g.CompletionTokens, g.CachedTokens)
		saved := cacheSaving(g.Model, g.CachedTokens)
		if ok {
			// 一个模型只要有一条能配上价就算「已计价」；配不上的单独计数，
			// 否则「￥3.2」看着像全部用量的价格，实际可能有一大半没算进去。
			priced[g.Model] = true
		} else {
			unpriced += g.Requests
		}
		for _, b := range []*usageBucket{get(byModel, g.Model), get(byDay, g.Day), get(byKind, g.Kind), total} {
			b.requests += g.Requests
			b.prompt += g.PromptTokens
			b.completion += g.CompletionTokens
			b.cached += g.CachedTokens
			b.cost += cost
			b.saved += saved
			if !ok {
				b.unpriced += g.Requests
			}
		}
	}

	// priced 是按模型记的，只能喂给 models 那一路。
	// toEntries 因此把判定做成入参：按天 / 按来源的 key 是日期和来源名，
	// 拿它们去查模型表必然查不到（曾经就恒为 false，界面上每一项都标成「未计价」）。
	// 那两路的正确判定是「这一桶里是否混进了没价的调用」。
	toEntries := func(m map[string]*usageBucket, isPriced func(k string) bool) []AICostEntry {
		out := make([]AICostEntry, 0, len(m))
		for k, b := range m {
			out = append(out, AICostEntry{
				Key: k, Requests: b.requests,
				PromptTokens: b.prompt, CompletionTokens: b.completion, CachedTokens: b.cached,
				TotalTokens:   b.prompt + b.completion,
				EstimatedCost: b.cost, SavedByCache: b.saved,
				Priced: isPriced(k),
			})
		}
		return out
	}

	models := toEntries(byModel, func(k string) bool { return priced[k] })
	// token 多的在前；同量时按名字稳定排（历史折算行可能没有模型名）
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].TotalTokens != models[j].TotalTokens {
			return models[i].TotalTokens > models[j].TotalTokens
		}
		return models[i].Key < models[j].Key
	})

	noneUnpriced := func(m map[string]*usageBucket) func(string) bool {
		return func(k string) bool {
			b := m[k]
			return b == nil || b.unpriced == 0
		}
	}
	daily := toEntries(byDay, noneUnpriced(byDay))
	// 日期升序：图表横轴从左到右是时间推进
	sort.SliceStable(daily, func(i, j int) bool { return daily[i].Key < daily[j].Key })

	sources := toEntries(byKind, noneUnpriced(byKind))
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Requests > sources[j].Requests })

	conversations, err := a.App.DB.CountAIUsageConversations(since)
	if err != nil {
		return services.Fail(err)
	}

	return services.Ok(AIUsageStats{
		Days:             days,
		Conversations:    conversations,
		Requests:         total.requests,
		PromptTokens:     total.prompt,
		CompletionTokens: total.completion,
		CachedTokens:     total.cached,
		TotalTokens:      total.prompt + total.completion,
		EstimatedCost:    total.cost,
		SavedByCache:     total.saved,
		Currency:         "CNY",
		UnpricedRequests: unpriced,
		Models:           models,
		Daily:            daily,
		Sources:          sources,
	})
}
