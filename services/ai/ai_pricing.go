package ai

// AI 用量估算价目表。
//
// 定位是「估算」而不是「对账」：Quicker/Raycast 之类的统计面板给的是数量级参考，
// 而不是账单。厂商定价随时会调、还会按批次/缓存命中/上下文长度分档，
// 这里只取公开的基础单价（输入 / 输出，元 / 百万 token）做线性估算。
//
// 未收录的模型返回 false 而不是猜一个价：宁可显示「未知」，也不要给出一个
// 看起来很精确其实是编的数字 —— 用户是会拿它去估预算的。
//
// 维护方式：模型 id 常带版本号后缀（deepseek-chat-0502、gpt-4o-2024-08-06），
// 所以一律按最长前缀匹配，新增厂商只需加一行 prefix，不必穷举所有后缀。

import (
	"sort"
	"strings"
)

// ModelPrice 元 / 百万 token
//
// CachedInput 是「命中前缀缓存」那部分输入的单价。为 0 表示该厂商未单独定价
// （或未收录），此时按正常输入价计 —— 宁可高估，也不要把账单外的折扣当成已省。
type ModelPrice struct {
	Input       float64
	CachedInput float64
	Output      float64
}

// priceRule 一条价目：prefix 与模型 id 的最长匹配者胜出。
type priceRule struct {
	prefix string
	price  ModelPrice
}

// aiPriceRules 内置价目表（2026-10 整理，单位：元 / 百万 token）。
// 排序在 lookupPrice 里按前缀长度降序做，这里保持可读的顺序即可。
var aiPriceRules = []priceRule{
	// DeepSeek
	{"deepseek-reasoner", ModelPrice{4.0, 1.0, 16.0}},
	{"deepseek-chat", ModelPrice{2.0, 0.5, 8.0}},
	{"deepseek-coder", ModelPrice{2.0, 0.5, 8.0}},
	// OpenAI
	{"gpt-4o-mini", ModelPrice{1.1, 0.55, 4.3}},
	{"gpt-4o", ModelPrice{18.0, 9.0, 72.0}},
	{"gpt-4.1-mini", ModelPrice{2.8, 1.4, 11.0}},
	{"gpt-4.1", ModelPrice{14.0, 7.0, 56.0}},
	{"o3-mini", ModelPrice{7.5, 3.75, 30.0}},
	{"o1-mini", ModelPrice{7.5, 3.75, 30.0}},
	// Anthropic
	{"claude-3-5-haiku", ModelPrice{5.8, 0.58, 29.0}},
	{"claude-3-5-sonnet", ModelPrice{21.0, 2.1, 105.0}},
	{"claude-sonnet-4", ModelPrice{21.0, 2.1, 105.0}},
	{"claude-opus-4", ModelPrice{105.0, 10.5, 525.0}},
	{"claude-haiku-4", ModelPrice{5.8, 0.58, 29.0}},
	// 阿里通义
	{"qwen-max", ModelPrice{8.4, 0.0, 25.2}},
	{"qwen-plus", ModelPrice{0.8, 0.0, 2.0}},
	{"qwen-turbo", ModelPrice{0.3, 0.0, 0.6}},
	{"qwen-long", ModelPrice{0.5, 0.0, 2.0}},
	{"qwen-flash", ModelPrice{0.2, 0.0, 1.0}},
	{"qwen2.5", ModelPrice{0.8, 0.0, 2.0}},
	// 智谱 GLM
	{"glm-4.6", ModelPrice{3.0, 0.0, 11.0}},
	{"glm-4-flash", ModelPrice{0.0, 0.0, 0.0}}, // 免费版：0 元，命中即明确不计费
	{"glm-4.5", ModelPrice{2.0, 0.0, 8.0}},
	{"glm-4-plus", ModelPrice{12.0, 0.0, 60.0}},
	{"glm-4-air", ModelPrice{1.0, 0.0, 1.0}},
	{"glm-4", ModelPrice{7.2, 0.0, 7.2}},
	// 月之暗面 Kimi
	{"kimi-latest", ModelPrice{4.0, 0.0, 12.0}},
	{"moonshot-v1", ModelPrice{4.0, 0.0, 12.0}},
	// 字节豆包
	{"doubao-pro", ModelPrice{5.4, 0.0, 9.0}},
	{"doubao-lite", ModelPrice{0.6, 0.0, 1.2}},
	{"doubao-seed", ModelPrice{0.8, 0.0, 8.0}},
	// 腾讯混元 / 百度文心 / 讯飞星火 / MiniMax
	{"hunyuan-turbo", ModelPrice{1.5, 0.0, 5.0}},
	{"hunyuan-t1", ModelPrice{7.0, 0.0, 4.0}},
	{"hunyuan", ModelPrice{1.5, 0.0, 5.0}},
	{"ernie-4.0", ModelPrice{7.2, 0.0, 21.6}},
	{"ernie-3.5", ModelPrice{1.0, 0.0, 3.0}},
	{"generalv3.5", ModelPrice{1.0, 0.0, 3.0}}, // 讯飞星火 UL
	{"generalv3", ModelPrice{1.0, 0.0, 3.0}},
	{"abab6.5", ModelPrice{3.0, 0.0, 3.0}},
	{"minimax-text", ModelPrice{3.0, 0.0, 3.0}},
}

// lookupPrice 按最长前缀匹配价目；没配上返回 false。
var aiPriceRulesSorted = sortedPriceRules()

func sortedPriceRules() []priceRule {
	// 长前缀优先在首次使用时排一次就够：deepseek-chat-v3 这类后缀要命中基础条目，
	// 而 glm-4-flash 必须赢 glm-4 —— 否则免费模型会被按 glm-4 计费。
	out := append([]priceRule(nil), aiPriceRules...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].prefix) > len(out[j].prefix) })
	return out
}

func lookupPrice(model string) (ModelPrice, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ModelPrice{}, false
	}
	for _, r := range aiPriceRulesSorted {
		if strings.HasPrefix(m, r.prefix) {
			return r.price, true
		}
	}
	return ModelPrice{}, false
}

// estimateCost 估算一次调用的开销（元）。未知模型不计价，返回 false 让调用方自行标注。
//
// 缓存命中部分用 CachedInput 计价、其余输入用 Input：这是「估算」与「账单」
// 差一个数量级的主因 —— 长会话的输入里 90%+ 都是每轮重复的上下文。
// 未单独定价缓存（CachedInput=0）的厂商按全价算，宁可高估也不虚报折扣。
func estimateCost(model string, promptTokens, completionTokens, cachedTokens int) (float64, bool) {
	p, ok := lookupPrice(model)
	if !ok {
		return 0, false
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	cachedRate := p.CachedInput
	if cachedRate <= 0 {
		cachedRate = p.Input
	}
	missTokens := promptTokens - cachedTokens
	cost := float64(missTokens)/1e6*p.Input +
		float64(cachedTokens)/1e6*cachedRate +
		float64(completionTokens)/1e6*p.Output
	return cost, true
}

// cacheSaving 缓存命中相对「全按输入价」省下的钱（元），用于界面上的「已省」。
// 该厂商没有缓存价时返回 0（本来就没折扣可省）。
func cacheSaving(model string, cachedTokens int) float64 {
	if cachedTokens <= 0 {
		return 0
	}
	p, ok := lookupPrice(model)
	if !ok || p.CachedInput <= 0 || p.CachedInput >= p.Input {
		return 0
	}
	return float64(cachedTokens) / 1e6 * (p.Input - p.CachedInput)
}
