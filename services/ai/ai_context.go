package ai

import (
	"encoding/json"
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

// aiContextOverhead 系统提示、工具定义与估算误差的固定预留。
//
// 原来只当「几百 token」处理，实际不是：65 个 MCP 工具的 JSON schema
// 本身就有一两万 token，再加上系统提示、记忆、规则文件，光固定部分就
// 能吃掉小窗口的一大半。这一项只做兜底——真正发出去之前会用
// aiPayloadTokens 把工具定义实测一遍（见 agent_run.go 的请求前守卫）。
const aiContextOverhead = 2000

// aiContextLimits 一次请求的上下限快照。
//
// 「上下限」是四个数而不是一个，因为它们回答的是不同的问题：
//
//	Window  硬上限：模型的物理窗口，超了上游直接 400；
//	Ceiling 出站上限：窗口扣掉输出预留后的可用输入空间，这是请求真正的天花板；
//	History 软上限：历史预算，到这里就该摘要压缩，留出估算误差的余量；
//	Floor   下限：压缩之后至少要留住的历史量，低于它就没有压的意义了。
//
// 分成四个数是因为「拿窗口当一个阈值」两头都错：按 Window 判太晚（一定 400），
// 按 History 判太早（还没超窗就把能留住的历史压掉了）。
type aiContextLimits struct {
	Window  int
	Output  int
	Ceiling int
	History int
	Floor   int
	// Measured 上一发请求的实测输入 token（上游 usage 报的，0 = 还没有）。
	// 它比任何估算都准，有它时判断超限就以它为准。
	Measured int
}

// contextLimitsOf 算出某个档案当前的上下限。
func contextLimitsOf(cfg AIProfile) aiContextLimits {
	// 用「有效值」：多模型档案里每个模型可以各自声明窗口与输出上限
	// （见 ai_model_entry.go），没声明才回落到按模型名推断。
	w := modelContextWindowOf(cfg)
	out := modelMaxOutputOf(cfg)
	if out <= 0 {
		out = aiDefaultMax
	}
	// 输出预留不该吃掉超过四分之一：模型再能写，一次回复也用不到 25 万 token，
	// 而 MaxTokens 配大了（有人直接填 100 万）会把输入空间挤没。
	if out > w/4 {
		out = w / 4
	}
	ceiling := w - out - aiContextOverhead
	if ceiling < 1024 {
		ceiling = 1024
	}
	// 只取七成：我们自己的 token 估算与真实分词器之间有 1.5 倍以上的差距，
	// 压到边上等于每轮都撞 400。
	history := ceiling * 7 / 10
	// WP4c：软上限可按档案覆盖（0 = 默认 120000）。
	if soft := softCapOf(cfg); history > soft {
		history = soft
	}
	if history < aiContextMinBudget {
		history = aiContextMinBudget
	}
	if history > ceiling {
		history = ceiling
	}
	// 下限同时受两头夹：不超过软上限（小窗口上软上限本来就很小），
	// 也不低于 1K（再低等于把历史清空，下一句就接不上了）。
	floor := aiContextMinBudget
	if floor > history {
		floor = history
	}
	if floor < 1024 {
		floor = 1024
	}
	return aiContextLimits{Window: w, Output: out, Ceiling: ceiling, History: history, Floor: floor}
}

// aiContextBudget 返回「历史 + 工具结果」可用的 token 预算（= 软上限）。
func aiContextBudget(cfg AIProfile) int {
	return contextLimitsOf(cfg).History
}

// requestSizes 每条会话最近一次请求的实测输入 token（上游 usage 报的）。
//
// 为什么值得缓存：我们自己的估算与真实分词器差得很远（中文、代码、JSON
// 的偏差方向都不一样），而 usage.prompt_tokens 是厂商自己数的。一次真实值
// 就能把后面每一轮的判断校准回来——尤其是「上一发已经占了 9 成、这一发
// 还要再塞 3 万 token 的工具结果」这种靠估算永远判不准的情况。
//
// 按会话缓存、进程内即可：重启后第一发按估算走，拿到 usage 就补回来。
var requestSizes sync.Map

// rememberRequestSize 记下这一发请求的实测大小。
func rememberRequestSize(convID string, n int) {
	if convID == "" || n <= 0 {
		return
	}
	requestSizes.Store(convID, n)
}

// lastRequestSize 取这条会话上一发的实测大小（0 = 还没测过）。
func lastRequestSize(convID string) int {
	if convID == "" {
		return 0
	}
	if v, ok := requestSizes.Load(convID); ok {
		if n, _ := v.(int); n > 0 {
			return n
		}
	}
	return 0
}

// aiLoopBudgetThisTurn 这一轮的工具结果预算：以上一发实测用量为准收紧。
//
// 默认预算（aiLoopBudget）是按窗口算的静态值，它假设「上下文是空的、从零开始长」。
// 多轮工具循环里不成立：上一发可能已经占掉窗口八成，这一轮再按三分之一的额度
// 往里塞工具结果，就等于往一条快满的管道里继续加水——表现是循环跑到第三四轮
// 突然 400，而前几轮都好好的。
//
// curTokens 是当前消息列表的估算大小，remain 是「离出站上限还剩多少」，
// 两者相加才是这一轮真正能容忍的体积；再按默认预算封顶（别因为余量大就放宽，
// 历史越长每轮重发越贵）。
func aiLoopBudgetThisTurn(cfg AIProfile, convID string, base, curTokens int) int {
	if base <= 0 {
		base = 24000
	}
	used := lastRequestSize(convID)
	if used <= 0 {
		return base
	}
	lim := contextLimitsOf(cfg)
	if lim.Ceiling <= 0 {
		return base
	}
	// 与请求前守卫同一个 85%：留一成半给分词器差异。
	remain := lim.Ceiling*85/100 - used
	if remain < 8000 {
		// 剩得不多时也不能把预算压成 0：一条全新的工具结果（读一个文件）
		// 至少要几 K，压得太狠的结果是模型反复重试同一个调用。
		remain = 8000
	}
	b := curTokens + remain
	if b > base {
		b = base
	}
	if b < 8000 {
		b = 8000
	}
	return b
}

// aiPayloadTokens 估算「真正要发出去的那一坨」有多大：消息 + 工具定义。
//
// 与 estimateLoopTokens 的分工：那个只看消息（循环内判断要不要折叠工具结果），
// 这个把工具定义也算进去——请求前守卫要回答的是「这一发会不会 400」，
// 而工具 schema 是每一发都要重发的固定成本，漏掉它就会长期系统性低估。
func aiPayloadTokens(messages []map[string]interface{}, tools []map[string]any) int {
	n := estimateLoopTokens(messages)
	if len(tools) > 0 {
		if b, err := json.Marshal(tools); err == nil {
			n += len([]rune(string(b))) * 10 / 16
		}
	}
	return n
}

// aiLoopBudget 本轮工具循环内的工具结果预算。
//
// 与 aiContextBudget 是两件事：那个管「库里历史有多长」，这个管「这一轮循环
// 里工具结果堆了多少」。**不能按历史软上限的 1/3 来算**：软上限默认 120K
// 是为了压住库里历史、控制每发重发的成本，可循环内的工具结果是另一套账。
// 1M 窗口上旧公式恒等于 40K，模型读三四个文件就撞线、工具结果被折叠，
// 它发现「刚读的内容被摘要掉了」就重读——实测能连转 30 轮不落一个文件。
//
// 也**不能按 ceiling/5 来算**（2026-11-20 放宽）：折叠会改写历史，是前缀缓存
// 的头号杀手——128K 窗口上 24K token 就开始折，长任务每几轮折一次，每次折叠
// 都把已攒的缓存整段作废（看板命中率 76% 的第二主因）。真正的 400 风险由
// aiLoopBudgetThisTurn 的 85% 余量守卫兜着，这里只是「本次循环内允许多少
// 工具结果」的静态上限，取 ceiling 的六成：128K 窗口 ~72K，1M 窗口封顶 400K。
// 折叠时机越晚越好——越晚折，折叠前攒下的缓存轮次越多。
func aiLoopBudget(cfg AIProfile) int {
	b := contextLimitsOf(cfg).Ceiling * 6 / 10
	if b < 24000 {
		b = 24000
	}
	if b > 400000 {
		b = 400000
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
