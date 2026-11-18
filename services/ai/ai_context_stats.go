package ai

// 「上下文用了多少」的数据源（WP2）。
//
// 为什么要单独一个只读接口：这件事前端自己算不出来——窗口上限来自三层检测
// （档案配置 / 上游 400 学到的 / 内置表 + 默认），工具 schema 的开销只有装配完
// 才知道，而「上一发实测用了多少」是进程内状态。
//
// 刻意**不**调 buildAIMessages：那条路在超预算时会触发历史摘要，
// 也就是「点一下看看占用」可能真的花钱调一次模型。这里全部走聚合查询与估算。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"quickdock/services"
)

// AIContextStatsView 上下文用量快照。
//
// 字段分成三组，对应弹框里的三块：
//   - 上限：Window / Output / Ceiling / HistoryBudget / LoopBudget
//   - 已用：MeasuredTokens（实测，最准）/ EstimatedTokens（估算）/ 明细
//   - 会话累计：SessionPrompt / SessionCompletion / RequestCount
type AIContextStatsView struct {
	ConversationID string `json:"conversation_id"`
	Model          string `json:"model"`
	// ProfileOK 有没有解析到可用档案（没有时上限走默认值，弹框要说明）。
	ProfileOK bool `json:"profile_ok"`

	// —— 上限 ——
	Window        int `json:"window"`         // 模型物理窗口
	Output        int `json:"output"`         // 输出预留
	Ceiling       int `json:"ceiling"`        // 出站上限（真正会 400 的线）
	HistoryBudget int `json:"history_budget"` // 历史软上限（到这里就该压缩）
	LoopBudget    int `json:"loop_budget"`    // 本轮工具结果预算
	// WindowSource 窗口值从哪来：profile / learned / table / default。
	// 用户看到「128k（内置表）」和「128k（上游报的）」时的反应完全不同。
	WindowSource string `json:"window_source"`

	// —— 已用 ——
	MeasuredTokens  int `json:"measured_tokens"`  // 上一发请求的实测输入，0 = 还没有
	EstimatedTokens int `json:"estimated_tokens"` // 按库里的历史 + 工具 schema 估算
	ToolCount       int `json:"tool_count"`
	ToolTokens      int `json:"tool_tokens"`
	MessageCount    int `json:"message_count"`
	HistoryChars    int `json:"history_chars"`

	// —— 会话累计 ——
	SessionPrompt     int `json:"session_prompt"`
	SessionCompletion int `json:"session_completion"`
	RequestCount      int `json:"request_count"`

	// —— 上次压缩（WP2 compacted 状态）——
	// 全零 / LastCompactAt 为空表示这次进程里还没压过。
	LastCompactAt         string `json:"last_compact_at"`
	LastCompactBefore     int    `json:"last_compact_before"`
	LastCompactAfter      int    `json:"last_compact_after"`
	LastCompactFolded     int    `json:"last_compact_folded"`
	LastCompactSummarized int    `json:"last_compact_summarized"`
	LastCompactByModel    bool   `json:"last_compact_by_model"`
}

// AIContextStats 返回一条会话的上下文用量快照。
func (a *AIService) AIContextStats(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}
	conv, err := a.App.DB.GetAIConversation(convID)
	if err != nil {
		return services.Fail(err)
	}
	if conv == nil {
		return services.FailMsg("会话不存在或已被删除")
	}

	cfg, ok := a.aiProfileForRun("", convID)
	lim := contextLimitsOf(cfg)

	mode := strings.TrimSpace(conv.Mode)
	if mode == "" {
		mode = "chat"
	}
	// 用会话自己的权限档装配工具面：弹框里的「工具定义开销」必须与这一轮
	// 真正会发出去的 tools[] 一致，否则用户会看到「只读档也说有 81 个工具」。
	sf := a.aiSurface(mode, aiConversationPermission(conv), aiConversationLegacyScene(conv), nil)
	tools, _ := aiToolsPayload(sf)
	toolTokens := 0
	if len(tools) > 0 {
		// 与 aiPayloadTokens 用同一个换算比（10/16 字符/token），
		// 否则弹框的数字与内部判定会对不上，用户按它调预算就会调反。
		if b, merr := json.Marshal(tools); merr == nil {
			toolTokens = len([]rune(string(b))) * 10 / 16
		}
	}

	count, chars, terr := a.App.DB.AIMessageTextStats(convID)
	if terr != nil {
		// 明细读不出来不该让整个弹框失败：上限与实测值仍然有价值。
		count, chars = 0, 0
	}

	last, _ := lastCompactionOf(convID)
	return services.Ok(AIContextStatsView{
		ConversationID:        convID,
		Model:                 cfg.Model,
		ProfileOK:             ok,
		Window:                lim.Window,
		Output:                lim.Output,
		Ceiling:               lim.Ceiling,
		HistoryBudget:         lim.History,
		LoopBudget:            aiLoopBudget(cfg),
		WindowSource:          contextWindowSource(cfg),
		MeasuredTokens:        lastRequestSize(convID),
		EstimatedTokens:       chars*10/16 + toolTokens,
		ToolCount:             len(tools),
		ToolTokens:            toolTokens,
		MessageCount:          count,
		HistoryChars:          chars,
		SessionPrompt:         conv.PromptTokens,
		SessionCompletion:     conv.CompletionTokens,
		RequestCount:          conv.RequestCount,
		LastCompactAt:         last.At,
		LastCompactBefore:     last.Before,
		LastCompactAfter:      last.After,
		LastCompactFolded:     last.Folded,
		LastCompactSummarized: last.Summarized,
		LastCompactByModel:    last.Summary,
	})
}

// compactWarningText 上下文用到 70% 时的那句提示（WP4c）。
//
// 抽成函数而不是就地拼字符串：文案里有两个必须说准的数字（当前占用、
// 触发压缩的阈值），而它们来自两处不同的计算。写死在 Sprintf 里时，
// 改阈值忘了改文案没人会发现——用户会按错误的预期等。
func compactWarningText(pct, margin int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 99 {
		pct = 99
	}
	return fmt.Sprintf(
		"上下文已用到约 %d%%，超过 %d%% 会自动压缩历史（更早的工具结果会先折叠）。", pct, margin)
}

// aiLastCompact 一次压缩留下的痕迹，供弹框显示「上次压了多少」。
//
// 为什么放内存：它只是「这次进程里最近一次压缩长什么样」，重启后重新压一次
// 就有新值了。落库要处理「同一会话多次压缩的保留策略」，收益远小于成本。
type aiLastCompact struct {
	Before     int
	After      int
	Folded     int
	Summarized int
	// Summary 是不是历史摘要那种压缩（要真调一次模型），而不是循环内折叠。
	Summary bool
	At      string
}

var aiLastCompacts sync.Map

// rememberCompaction 记下最近一次压缩。
func rememberCompaction(convID string, before, after, folded, summarized int, summary bool) {
	if strings.TrimSpace(convID) == "" {
		return
	}
	aiLastCompacts.Store(strings.TrimSpace(convID), aiLastCompact{
		Before: before, After: after, Folded: folded, Summarized: summarized,
		Summary: summary, At: time.Now().Format(time.RFC3339),
	})
}

// lastCompactionOf 取最近一次压缩（没有则 false）。
func lastCompactionOf(convID string) (aiLastCompact, bool) {
	v, ok := aiLastCompacts.Load(strings.TrimSpace(convID))
	if !ok {
		return aiLastCompact{}, false
	}
	c, ok := v.(aiLastCompact)
	return c, ok
}

// AICompactConversation 立即压缩一次历史（WP4c 手动入口）。
//
// 为什么复用 buildAIMessagesWithin 而不是另写一套「保留几条、压到多少」：
// 那套口径已经有三个使用者（预判守卫、超窗兜底、这里），任何一处单独实现
// 都会慢慢漂移，而漂移的表现是「点手动压缩之后，下一轮又被自动压一次」。
//
// 代价：这是一次真实的模型调用（摘要）。所以只由用户点按钮触发，不做成自动。
func (a *AIService) AICompactConversation(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}
	conv, err := a.App.DB.GetAIConversation(convID)
	if err != nil {
		return services.Fail(err)
	}
	if conv == nil {
		return services.FailMsg("会话不存在或已被删除")
	}
	cfg, _ := a.aiProfileForRun("", convID)
	lim := contextLimitsOf(cfg)
	// 目标取上限的一半：与预判守卫同一口径。只压到刚过线的话，
	// 下一轮工具结果一长就又超，用户会以为「压缩按钮没生效」。
	target := lim.Ceiling / 2
	if target < lim.Floor {
		target = lim.Floor
	}
	mode := strings.TrimSpace(conv.Mode)
	if mode == "" {
		mode = "chat"
	}
	// progressSink 的 onProgress 为 nil 时什么都不写：这是 RPC，没有 SSE 可写。
	msgs, berr := a.buildAIMessagesWithin(context.Background(), cfg, convID, mode, target, true, progressSink{})
	if berr != nil {
		return services.Fail(berr)
	}
	return services.Ok(map[string]any{
		"messages": len(msgs),
		"target":   target,
		"ceiling":  lim.Ceiling,
	})
}

// contextWindowSource 说明窗口值是怎么来的（与 contextWindowOf 的判定顺序一致）。
// 顺序必须与 contextWindowOf 完全对齐：两处一旦不一致，弹框就会标错来源，
// 而「我以为它认得这个模型，其实只是默认猜了 128k」会直接误导用户去调配置。
func contextWindowSource(cfg AIProfile) string {
	if cfg.ContextWindow > 0 {
		return "profile"
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Model))
	if name == "" {
		return "default"
	}
	if v, ok := learnedWindows.Load(name); ok {
		if n, _ := v.(int); n > 0 {
			return "learned"
		}
	}
	bestLen := -1
	hit := false
	for _, e := range aiContextWindows {
		if !strings.Contains(name, e.match) {
			continue
		}
		if len(e.match) > bestLen {
			bestLen, hit = len(e.match), true
		}
	}
	if hit {
		return "table"
	}
	return "default"
}
