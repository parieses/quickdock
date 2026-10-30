package ai

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"quickdock/internal/logger"
)

// 上下文窗口（context window）的判定与预算。
//
// 为什么不能用一个固定值：窗口从 8K（本地小模型）到 1M（2026 年的主流旗舰）差了
// 一百多倍。固定 3000 的选择等于让两百万 token 的对话在第 5 轮就被摘要掉，
// 而固定 1M 又会让本地 8K 模型每轮都撞 400。所以这里做三层：
//
//  1. 档案里显式填的 ContextWindow（用户最清楚自己在跑什么）；
//  2. 内置模型表（按模型名匹配，2026-10 快照）；
//  3. 上游报错里学到的真实窗口（最权威：厂商自己说的）。
//
// 第 3 层是关键补充：用户接自建网关、私有模型名、厂商新发的模型都匹配不到表，
// 但超窗那次 400 的报文里几乎都写着 "maximum context length is N tokens"，
// 读出来后写进进程缓存，之后不必再撞一次。

// aiContextDefaultWindow 未知模型的兜底窗口。
//
// 取 128K 而不是更保守的 32K：2026 年主流 API 模型已经全部 ≥128K，
// 猜小了要白压一堆本来能留住的历史；猜大了最多撞一次 400，而撞完之后
// 我们会从报文里学到真实值，代价只有一次请求。
const aiContextDefaultWindow = 128000

// aiContextSoftCap 单次请求愿意携带的历史上限。
//
// 窗口 1M 不等于要把它装满：历史是每轮重发的，装满 70 万 token 的历史
// 意味着每问一句都付一次 70 万输入的钱，延迟也随之上来。12 万 token
// 约合 8 万汉字，已经远超「一个真实任务里需要记住的上下文」；
// 超过这个量级就摘要压缩，是省钱又不失忆的平衡点。
const aiContextSoftCap = 120000

// aiContextMinBudget 预算下限：再小的窗口也得给历史留一点，
// 否则每轮发送前都要压缩，等于每轮多花一次模型调用。
const aiContextMinBudget = 8000

// aiContextWindows 内置窗口表（2026-10 快照）。
//
// 匹配规则是「模型名包含 match」且取最长命中（见 contextWindowOf），
// 所以细粒度的条目要写全（gpt-5.4-mini 必须先于 gpt-5 命中），
// 顺序本身无所谓。数字来源是各厂商当期文档；厂商改窗口不会通知我们，
// 所以这里只当初始值用，一旦上游报过真实值就以那个为准。
var aiContextWindows = []struct {
	match  string
	window int
}{
	// ---- 1M 档 ----
	{"claude-opus-4.6", 1000000},
	{"claude-opus-4.7", 1000000},
	{"claude-opus-4.8", 1000000},
	{"claude-opus-5", 1000000},
	{"claude-sonnet-4.6", 1000000},
	{"claude-fable", 1000000},
	{"gpt-5.5", 1050000},
	{"gpt-5.6", 1050000},
	{"gemini-2.5", 1048576},
	{"gemini-3", 1048576},
	{"deepseek-v4", 1000000},
	{"qwen3.5-plus", 1131072},
	{"qwen3.5-flash", 1000000},
	{"qwen-plus", 1000000},
	{"qwen-flash", 1000000},
	{"glm-5.2", 1048576},
	{"kimi-k3", 1048576},
	{"minimax-m3", 1000000},
	{"grok-4.3", 1000000},
	{"grok-4.20", 1000000},
	{"llama-4", 1000000},
	{"gpt-4.1", 1047576},

	// ---- 400K 档 ----
	{"gpt-5.4-mini", 400000},
	{"gpt-5.4-nano", 400000},
	{"gpt-5.4", 1050000},
	{"gpt-5", 400000},

	// ---- 256/262K 档 ----
	{"kimi-k2.5", 262144},
	{"kimi-k2.6", 262144},
	{"kimi-k2.7", 262144},
	{"qwen3-max", 262144},
	{"mistral-large-3", 262144},
	{"grok", 256000},

	// ---- 200K 档 ----
	{"claude", 200000},
	{"glm-5", 200000},
	{"glm-4.7", 200000},
	{"glm-4.6", 200000},
	{"minimax", 200000},
	{"o3", 200000},
	{"o4", 200000},

	// ---- 128/131K 档 ----
	{"deepseek", 128000},
	{"gpt-4o", 128000},
	{"qwen", 131072},
	{"glm", 131072},
	{"kimi", 131072},
	{"moonshot", 131072},
	{"llama", 131072},
	{"mistral", 131072},
	{"qwen2.5", 131072},

	// ---- 32K 及以下：本地/老模型，认得就别猜 128K ----
	{"qwen2", 32768},
	{"phi-3", 4096},
}

// learnedWindows 从上游 400 报文里学到的真实窗口（key 为小写模型名）。
//
// 进程内缓存即可：重启后重新学一次的成本是「撞一次 400」，
// 而那一次本来就会走兜底重试，用户几乎无感。落库反而要处理
// 「换了 baseURL 但模型同名」的失效问题——同名模型在 vLLM 上是 8K、
// 在官方 API 上是 128K，按模型名落库会把两种部署混成一条。
var learnedWindows sync.Map

// contextWindowOf 判定某个配置的上下文窗口（token）。
func contextWindowOf(cfg AIProfile) int {
	if cfg.ContextWindow > 0 {
		return cfg.ContextWindow
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Model))
	if name == "" {
		return aiContextDefaultWindow
	}
	if v, ok := learnedWindows.Load(name); ok {
		if n, _ := v.(int); n > 0 {
			return n
		}
	}
	// 最长命中：gpt-5.4-mini 必须赢过 gpt-5。
	best, bestLen := 0, -1
	for _, e := range aiContextWindows {
		if !strings.Contains(name, e.match) {
			continue
		}
		if len(e.match) > bestLen {
			best, bestLen = e.window, len(e.match)
		}
	}
	if best > 0 {
		return best
	}
	return aiContextDefaultWindow
}

// aiContextBudget 返回「历史 + 工具结果」可用的 token 预算。
//
// 窗口不能全给输入：max_tokens 是预扣而不是上限，上游会把
// 「输入 + 预留输出」一起跟窗口比（DeepSeek 的报错就直接写明
// "10001 in the prompt, 0 for the completion"）。所以先扣掉输出预留
// 与系统提示/工具定义的位置，再只取七成——我们自己的 token 估算
// 与真实分词器之间有 1.5 倍以上的差距，压到边上等于每轮都撞 400。
func aiContextBudget(cfg AIProfile) int {
	w := contextWindowOf(cfg)
	out := cfg.MaxTokens
	if out <= 0 {
		out = aiDefaultMax
	}
	// 输出预留不该吃掉超过四分之一：模型再能写，一次回复也用不到 25 万 token，
	// 而 MaxTokens 配大了（有人直接填 100 万）会把输入空间挤没。
	if out > w/4 {
		out = w / 4
	}
	// 2000 留给系统提示、工具定义，以及估算误差的固定部分。
	b := (w - out - 2000) * 7 / 10
	if b > aiContextSoftCap {
		b = aiContextSoftCap
	}
	if b < aiContextMinBudget {
		b = aiContextMinBudget
	}
	return b
}

// aiLoopBudget 本轮工具循环内的工具结果预算。
//
// 与 aiContextBudget 是两件事：那个管「库里历史有多长」，这个管「这一轮循环
// 里工具结果堆了多少」。工具结果就地折叠（不调模型、几毫秒）比摘要便宜得多，
// 所以阈值可以给得比历史更松——按历史预算的三分之一，且不低于原来的 24K。
func aiLoopBudget(cfg AIProfile) int {
	b := aiContextBudget(cfg) / 3
	if b < 24000 {
		b = 24000
	}
	if b > 200000 {
		b = 200000
	}
	return b
}

// 超窗报文的特征词。各家的说法不一样：
//
//	OpenAI   : "Your input exceeds the context window of this model" / code=context_length_exceeded
//	DeepSeek : "This model's maximum context length is N tokens. However, you requested ..."
//	Anthropic: "prompt is too long: N tokens > M maximum"
//	vLLM/SGLang: "The input size is larger than the maximum model length"
//
// 只认「明显是超窗」的说法，宁可漏判（漏了就是普通报错，用户看到原始报文）
// 也不要误判——把一个 401 当成超窗去压缩历史，等于白白丢掉上下文。
var aiOverflowMarkers = []string{
	"context_length_exceeded",
	"maximum context length",
	"context window",
	"prompt is too long",
	"exceeds the maximum",
	"maximum model length",
	"too many tokens",
	"input is too long",
	"reduce your prompt",
	"reduce the length",
}

// aiOverflowWindowRe 从超窗报文里抠出真实窗口，如
// "maximum context length is 1048576 tokens"。
var aiOverflowWindowRe = regexp.MustCompile(`(?i)(?:maximum context length is|context window of|context length of|maximum context size of)\s*([0-9][0-9,]{2,})\s*tokens`)

// contextOverflow 判断错误是否为「上下文超窗」，返回从报文里读到的窗口（读不到为 0）。
func contextOverflow(err error, model string) (int, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	hit := false
	for _, m := range aiOverflowMarkers {
		if strings.Contains(lower, m) {
			hit = true
			break
		}
	}
	if !hit {
		return 0, false
	}
	w := 0
	if mm := aiOverflowWindowRe.FindStringSubmatch(msg); len(mm) == 2 {
		if n, e := strconv.Atoi(strings.ReplaceAll(mm[1], ",", "")); e == nil && n >= 1024 {
			w = n
		}
	}
	if w > 0 && model != "" {
		key := strings.ToLower(strings.TrimSpace(model))
		if prev, ok := learnedWindows.Load(key); !ok || prev != w {
			learnedWindows.Store(key, w)
			logger.I("[ai] 从上游报文学到上下文窗口 model=%s window=%d", model, w)
		}
	}
	return w, true
}
