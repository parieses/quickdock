package ai

import (
	"math"
	"testing"
	"time"
)

// 最长前缀匹配是计价正确性的全部基础：命中错一条，界面上的钱数就是错的。
func TestLookupPriceLongestPrefixWins(t *testing.T) {
	cases := []struct {
		model  string
		wantOK bool
		input  float64
	}{{
		// 版本后缀不该让基础条目落空
		model: "deepseek-chat-v3-0324", wantOK: true, input: 2.0,
	}, {
		// 免费模型必须先命中 glm-4-flash，而不是被 glm-4 按 7.2 计费
		model: "glm-4-flash", wantOK: true, input: 0.0,
	}, {
		model: "glm-4.6", wantOK: true, input: 3.0,
	}, {
		model: "gpt-4o-mini-2024-07-18", wantOK: true, input: 1.1,
	}, {
		// 大小写/空格由调用方清洗，这里一并覆盖
		model: "  GPT-4o  ", wantOK: true, input: 18.0,
	}, {
		// 没收录就明确说不知道，绝不猜一个价
		model: "some-private-model", wantOK: false,
	}, {
		model: "", wantOK: false,
	}}
	for _, c := range cases {
		p, ok := lookupPrice(c.model)
		if ok != c.wantOK {
			t.Fatalf("lookupPrice(%q) ok=%v want %v", c.model, ok, c.wantOK)
		}
		if ok && p.Input != c.input {
			t.Errorf("lookupPrice(%q).Input=%v want %v", c.model, p.Input, c.input)
		}
	}
}

// 计价必须按输入/输出分别乘，且未收录模型返回 priced=false 而不是 0 元。
func TestEstimateCost(t *testing.T) {
	// deepseek-chat：输入 2 元/百万，输出 8 元/百万
	cost, ok := estimateCost("deepseek-chat", 1_000_000, 500_000, 0, false)
	if !ok {
		t.Fatal("deepseek-chat 应命中价目表")
	}
	if want := 2.0 + 4.0; math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost=%v want %v", cost, want)
	}
	if cost, ok := estimateCost("unknown-model", 1000, 1000, 0, false); ok || cost != 0 {
		t.Errorf("未收录模型应返回 (0,false)，实得 (%v,%v)", cost, ok)
	}
	// 免费模型命中即 0 元，但仍然是「已计价」——不能算进未收录次数
	if cost, ok := estimateCost("glm-4-flash", 1000, 1000, 0, false); !ok || cost != 0 {
		t.Errorf("免费模型应返回 (0,true)，实得 (%v,%v)", cost, ok)
	}
}

// 现役模型必须能命中价目表：命中失败的后果不是「差几毛钱」，而是整条调用
// 记成「未计价」、金额显示 0。deepseek-flash 是 2026 年的官方现役模型
// （V4.1-Flash），而 deepseek-chat / deepseek-reasoner 早在 7 月已下线 ——
// 只留旧名字，最近一个月的用量就全都不算钱。
func TestEstimateCostCurrentModels(t *testing.T) {
	// deepseek-flash 谷时：输入 $0.15/M、缓存 $0.003/M、输出 $0.60/M（折人民币 ×7.2）
	cost, ok := estimateCost("deepseek-flash", 1_000_000, 1_000_000, 0, false)
	if !ok {
		t.Fatal("deepseek-flash 应命中价目表")
	}
	if want := (0.15 + 0.60) * usdToCNY; math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost=%v want %v", cost, want)
	}
	// 峰时恰好翻倍
	if peakCost, _ := estimateCost("deepseek-flash", 1_000_000, 1_000_000, 0, true); math.Abs(peakCost-cost*2) > 1e-9 {
		t.Errorf("峰时 cost=%v want %v", peakCost, cost*2)
	}
	// 旧名仍按 flash 计价（官方就是这么路由的），不能落进未计价
	if _, ok := estimateCost("deepseek-v4-flash", 1000, 1000, 0, false); !ok {
		t.Error("deepseek-v4-flash 应命中价目表")
	}
	// 不分峰谷的厂商：peak=true 也不能悄悄涨价
	v0, _ := estimateCost("agnes-2.5-flash", 1_000_000, 0, 0, false)
	v1, _ := estimateCost("agnes-2.5-flash", 1_000_000, 0, 0, true)
	if math.Abs(v0-v1) > 1e-9 {
		t.Errorf("agnes 不分峰谷，峰时 %v 应等于谷时 %v", v1, v0)
	}
	// 同族里更长的前缀（-beta）必须赢过 -pro
	if p, ok := lookupPrice("agnes-2.5-pro-beta"); !ok || math.Abs(p.Input-0.10*usdToCNY) > 1e-9 {
		t.Errorf("agnes-2.5-pro-beta 应命中 beta 条目，实得 %+v ok=%v", p, ok)
	}
}

// 峰时判定是 DeepSeek 计价的一半：工作日北京 09–12、14–18 才算峰时。
func TestIsPeakBeijing(t *testing.T) {
	cases := []struct {
		hour, weekday int
		want          bool
	}{
		{9, 1, true},   // 周一上午
		{11, 5, true},  // 周五 11 点
		{14, 3, true},  // 周三下午
		{17, 2, true},  // 周二 17 点
		{12, 1, false}, // 午休
		{18, 1, false}, // 下班后
		{8, 1, false},  // 上班前
		{10, 0, false}, // 周日不翻倍
		{10, 6, false}, // 周六不翻倍
	}
	for _, c := range cases {
		if got := isPeakBeijing(c.hour, c.weekday); got != c.want {
			t.Errorf("isPeakBeijing(%d,%d)=%v want %v", c.hour, c.weekday, got, c.want)
		}
	}
	// UTC 02:00 周二 = 北京 10:00 周二 → 峰时
	if !isPeakForPricing(time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC)) {
		t.Error("UTC 02:00 周二 应为峰时")
	}
	// UTC 02:00 周六 = 北京 10:00 周六 → 谷时
	if isPeakForPricing(time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)) {
		t.Error("UTC 02:00 周六 应为谷时")
	}
}

// 窗口起点必须落在自然日边界上：按「此刻往前 N×24h」算的话，
// 今天的数据只会统计到「此刻」为止，界面上会显示今天比昨天少一截。
func TestUsageSinceAlignsToDayStart(t *testing.T) {
	if got := usageSince(0); got != "" {
		t.Errorf("days=0 应表示不限时间，实得 %q", got)
	}
	got, err := time.Parse(time.RFC3339, usageSince(7))
	if err != nil {
		t.Fatalf("返回的不是 RFC3339: %q", usageSince(7))
	}
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Errorf("起点应为零点，实得 %v", got)
	}
	// 近 7 天含今天在内共 7 个自然日 → 起点是 6 天前的零点
	want := time.Now().AddDate(0, 0, -6).Format("2006-01-02")
	if got.Format("2006-01-02") != want {
		t.Errorf("起点=%s want %s", got.Format("2006-01-02"), want)
	}
}

// 缓存命中必须按缓存价折算：长会话 99% 命中时，按全价算能把花费算高一个数量级。
func TestEstimateCostWithCache(t *testing.T) {
	// deepseek-chat：输入 2 元/M，缓存读取 0.5 元/M，输出 8 元/M
	cost, ok := estimateCost("deepseek-chat", 1_000_000, 500_000, 900_000, false)
	if !ok {
		t.Fatal("deepseek-chat 应命中价目表")
	}
	// 未命中 10 万 ×2 + 命中 90 万 ×0.5 + 输出 50 万 ×8 = 0.2+0.45+4.0
	if want := 4.65; math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost=%v want %v", cost, want)
	}
	// 相对全价（2.0+4.0=6.0）省下 1.35
	if got := cacheSaving("deepseek-chat", 900_000, false); math.Abs(got-1.35) > 1e-9 {
		t.Errorf("cacheSaving=%v want 1.35", got)
	}
	// 没有缓存价（未单独定价）的模型不报「已省」，也不按折扣算
	if got := cacheSaving("glm-4", 1_000_000, false); got != 0 {
		t.Errorf("glm-4 无缓存价，saved 应为 0，实得 %v", got)
	}
	// cached 超过 prompt（网关口径混乱）时夹住，不许算出负数输入
	if cost, _ := estimateCost("deepseek-chat", 1000, 0, 9999, false); cost < 0 {
		t.Errorf("cached>prompt 时不应出现负数花费: %v", cost)
	}
}

// 两家缓存字段不同，且不能相加（同一份数据两种写法）。
func TestOpenAIUsageCachedParsing(t *testing.T) {
	// OpenAI 风格
	o := &openAIUsage{PromptTokens: 1000, CompletionTokens: 10}
	o.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 800}
	if u := o.toUsage(); u.Cached != 800 || u.Prompt != 1000 || u.Completion != 10 {
		t.Errorf("OpenAI 风格解析错误: %+v", u)
	}

	// DeepSeek 风格（没有 details）
	ds := &openAIUsage{PromptTokens: 1000, CompletionTokens: 10, PromptCacheHitTokens: 990}
	if u := ds.toUsage(); u.Cached != 990 {
		t.Errorf("DeepSeek 风格解析错误: %+v", u)
	}

	// 两种都给了：取 details 的值，不做累加
	both := &openAIUsage{PromptTokens: 1000, CompletionTokens: 10, PromptCacheHitTokens: 990}
	both.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 800}
	if u := both.toUsage(); u.Cached != 800 {
		t.Errorf("两种字段并存时不应相加，实得 %d", u.Cached)
	}

	// 上游没给 usage
	var nilUsage *openAIUsage
	if u := nilUsage.toUsage(); !u.Empty() {
		t.Errorf("nil usage 应为空: %+v", u)
	}
}
