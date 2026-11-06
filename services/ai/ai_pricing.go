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
	"time"
)

// usdToCNY 估算用的固定汇率。
//
// 界面只出人民币，而 DeepSeek / Agnes 的官方刊例是美元，这里统一按 7.2 折。
// 汇率浮动会带来百分之几的误差——对「今天大概花了多少」这种问题不影响结论，
// 真要精确到分只能去厂商后台看账单（个人 API Key 调不到那个接口）。
const usdToCNY = 7.2

// ModelPrice 元 / 百万 token
//
// CachedInput 是「命中前缀缓存」那部分输入的单价。为 0 表示该厂商未单独定价
// （或未收录），此时按正常输入价计 —— 宁可高估，也不要把账单外的折扣当成已省。
type ModelPrice struct {
	Input       float64
	CachedInput float64
	Output      float64
	// PeakMultiplier 峰时相对谷时的倍数。0 或 1 表示该厂商不分峰谷。
	//
	// DeepSeek 从 2026 年起按时段计价（工作日白天翻倍），同一批 token 记在
	// 上午 10 点和晚上 10 点差一倍钱。不区分会让估算整体偏低——用户的调用
	// 恰恰集中在工作时段。其余厂商留零值即可。
	PeakMultiplier float64
}

// cnyPrice 按人民币刊例价构造一条价目（不分峰谷，即国内厂商的常态）。
//
// 用构造函数而不是位置字面量：ModelPrice 一旦加字段（比如这次的 PeakMultiplier），
// 所有无字段名的字面量都得跟着改，而这里已经有四十条。
func cnyPrice(in, cached, out float64) ModelPrice {
	return ModelPrice{Input: in, CachedInput: cached, Output: out}
}

// usdPrice 按厂商的美元刊例价构造一条人民币价目。
// 参数顺序就是官方价目表的列序：输入 / 缓存命中输入 / 输出，最后一个才是峰时倍数。
func usdPrice(in, cached, out, peakMultiplier float64) ModelPrice {
	return ModelPrice{
		Input:          in * usdToCNY,
		CachedInput:    cached * usdToCNY,
		Output:         out * usdToCNY,
		PeakMultiplier: peakMultiplier,
	}
}

// priceRule 一条价目：prefix 与模型 id 的最长匹配者胜出。
type priceRule struct {
	prefix string
	price  ModelPrice
}

// aiPriceRules 内置价目表（单位：元 / 百万 token）。
//
// 维护纪律：**模型下线/改名的速度比价目表更新快**。deepseek-chat / deepseek-reasoner
// 在 2026-07-24 就已停用，官方现役只有 deepseek-flash 与 deepseek-v4-pro —— 名字对不上的
// 后果不是「差几毛钱」，而是整条调用记成「未计价」、金额显示 0。所以每次官方发新模型，
// 这里必须同批补上；历史数据里的旧名字也留着，删了会让旧账变成未计价。
//
// 排序在 lookupPrice 里按前缀长度降序做，这里保持可读的顺序即可。
var aiPriceRules = []priceRule{
	// DeepSeek（美元刊例，工作日 UTC 01–04 / 06–10 为峰时，价格翻倍）
	// 2026-11 官方价：flash 0.15/0.003/0.60，v4-pro 0.66/0.022/1.98（谷时）
	{"deepseek-flash", usdPrice(0.15, 0.003, 0.60, 2)},
	{"deepseek-v4-flash", usdPrice(0.15, 0.003, 0.60, 2)}, // 旧名，官方仍路由到 flash 同价计费
	{"deepseek-v4-pro", usdPrice(0.66, 0.022, 1.98, 2)},
	// 下面三条官方已于 2026-07-24 停用，只为历史明细还能算出钱
	{"deepseek-reasoner", cnyPrice(4.0, 1.0, 16.0)},
	{"deepseek-chat", cnyPrice(2.0, 0.5, 8.0)},
	{"deepseek-coder", cnyPrice(2.0, 0.5, 8.0)},
	// Agnes（美元刊例，不分峰谷；2.5-flash / 3.0-flash 目前限时免费，
	// 这里仍按刊例价折算——免费是优惠不是定价，用它预估预算才是本意）
	{"agnes-2.5-flash", usdPrice(0.05, 0.005, 0.15, 0)},
	{"agnes-3.0-flash", usdPrice(0.05, 0.005, 0.15, 0)},
	{"agnes-2.5-pro-beta", usdPrice(0.10, 0.01, 0.30, 0)},
	{"agnes-2.5-pro", usdPrice(0.45, 0.045, 0.90, 0)},
	{"agnes-3.0-pro", usdPrice(0.45, 0.045, 0.90, 0)},
	// OpenAI
	{"gpt-4o-mini", cnyPrice(1.1, 0.55, 4.3)},
	{"gpt-4o", cnyPrice(18.0, 9.0, 72.0)},
	{"gpt-4.1-mini", cnyPrice(2.8, 1.4, 11.0)},
	{"gpt-4.1", cnyPrice(14.0, 7.0, 56.0)},
	{"o3-mini", cnyPrice(7.5, 3.75, 30.0)},
	{"o1-mini", cnyPrice(7.5, 3.75, 30.0)},
	// Anthropic
	{"claude-3-5-haiku", cnyPrice(5.8, 0.58, 29.0)},
	{"claude-3-5-sonnet", cnyPrice(21.0, 2.1, 105.0)},
	{"claude-sonnet-4", cnyPrice(21.0, 2.1, 105.0)},
	{"claude-opus-4", cnyPrice(105.0, 10.5, 525.0)},
	{"claude-haiku-4", cnyPrice(5.8, 0.58, 29.0)},
	// 阿里通义
	{"qwen-max", cnyPrice(8.4, 0.0, 25.2)},
	{"qwen-plus", cnyPrice(0.8, 0.0, 2.0)},
	{"qwen-turbo", cnyPrice(0.3, 0.0, 0.6)},
	{"qwen-long", cnyPrice(0.5, 0.0, 2.0)},
	{"qwen-flash", cnyPrice(0.2, 0.0, 1.0)},
	{"qwen2.5", cnyPrice(0.8, 0.0, 2.0)},
	// 智谱 GLM
	{"glm-4.6", cnyPrice(3.0, 0.0, 11.0)},
	{"glm-4-flash", cnyPrice(0.0, 0.0, 0.0)}, // 免费版：0 元，命中即明确不计费
	{"glm-4.5", cnyPrice(2.0, 0.0, 8.0)},
	{"glm-4-plus", cnyPrice(12.0, 0.0, 60.0)},
	{"glm-4-air", cnyPrice(1.0, 0.0, 1.0)},
	{"glm-4", cnyPrice(7.2, 0.0, 7.2)},
	// 月之暗面 Kimi
	{"kimi-latest", cnyPrice(4.0, 0.0, 12.0)},
	{"moonshot-v1", cnyPrice(4.0, 0.0, 12.0)},
	// 字节豆包
	{"doubao-pro", cnyPrice(5.4, 0.0, 9.0)},
	{"doubao-lite", cnyPrice(0.6, 0.0, 1.2)},
	{"doubao-seed", cnyPrice(0.8, 0.0, 8.0)},
	// 腾讯混元 / 百度文心 / 讯飞星火 / MiniMax
	{"hunyuan-turbo", cnyPrice(1.5, 0.0, 5.0)},
	{"hunyuan-t1", cnyPrice(7.0, 0.0, 4.0)},
	{"hunyuan", cnyPrice(1.5, 0.0, 5.0)},
	{"ernie-4.0", cnyPrice(7.2, 0.0, 21.6)},
	{"ernie-3.5", cnyPrice(1.0, 0.0, 3.0)},
	{"generalv3.5", cnyPrice(1.0, 0.0, 3.0)}, // 讯飞星火 UL
	{"generalv3", cnyPrice(1.0, 0.0, 3.0)},
	{"abab6.5", cnyPrice(3.0, 0.0, 3.0)},
	{"minimax-text", cnyPrice(3.0, 0.0, 3.0)},
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
//
// peak 表示这批 token 是否消耗在厂商的峰时（见 isPeakBeijing）。对不分峰谷的
// 厂商它没有作用，所以调用方可以无脑传「这条记录发生在峰时吗」。
func estimateCost(model string, promptTokens, completionTokens, cachedTokens int, peak bool) (float64, bool) {
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
	return cost * peakMultiplier(p, peak), true
}

// cacheSaving 缓存命中相对「全按输入价」省下的钱（元），用于界面上的「已省」。
// 该厂商没有缓存价时返回 0（本来就没折扣可省）。
func cacheSaving(model string, cachedTokens int, peak bool) float64 {
	if cachedTokens <= 0 {
		return 0
	}
	p, ok := lookupPrice(model)
	if !ok || p.CachedInput <= 0 || p.CachedInput >= p.Input {
		return 0
	}
	return float64(cachedTokens) / 1e6 * (p.Input - p.CachedInput) * peakMultiplier(p, peak)
}

func peakMultiplier(p ModelPrice, peak bool) float64 {
	if peak && p.PeakMultiplier > 1 {
		return p.PeakMultiplier
	}
	return 1
}

// isPeakBeijing 按北京时区的小时与星期判断是否落在 DeepSeek 峰时。
//
// 官方口径（UTC）：周一至周五 01:00–04:00 与 06:00–10:00，换成北京时间就是
// 09:00–12:00 与 14:00–18:00 —— 正好是国内的上班时段，也是绝大多数人用
// AI 的时段，所以不区分就等于系统性低估。
//
// weekday 取 time.Weekday（0 = 周日）。中国法定节假日官方也算谷时，但节假日
// 表不在本地维护范围内：那几天会多半倍，属于可接受的估算误差。
func isPeakBeijing(hour, weekday int) bool {
	if weekday == 0 || weekday == 6 {
		return false
	}
	return (hour >= 9 && hour < 12) || (hour >= 14 && hour < 18)
}

// isPeakForPricing 判断「此刻」是否峰时（单次调用刚结束时用）。
func isPeakForPricing(t time.Time) bool {
	bj := t.UTC().Add(8 * time.Hour) // 北京时区无夏令时，加 8 小时即当地时间
	return isPeakBeijing(bj.Hour(), int(bj.Weekday()))
}

// parseUsageTime 解析库里存的 RFC3339 时间，解析失败退回「此刻」。
//
// 历史脏数据不该让整个消息列表报错；退化成当前时段最多让那条气泡的钱
// 差一倍，而拒绝加载会让用户看不到对话本身。
func parseUsageTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(s)); err == nil {
		return t
	}
	return time.Now()
}
