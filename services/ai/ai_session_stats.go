package ai

// 会话统计（输入框下方三个胶囊的其中两个：会话统计 / Token 用量）。
//
// 为什么单独一个后端接口：DSH 参考图里的「模型用时 / 工具调用用时 / TTFT /
// TPS / 缓存读取明细」分散在 ai_messages 的 token 列和 agent_events 的 JSON
// payload 里，前端拿不到全量事件（回放接口每条 run 只带 200 条），也不该
// 自己解析一堆 JSON。这里用 SQL 聚合，一次往返给全。
//
// 口径（与 DSH 尽量一致，差异在注释里写明）：
//   - 轮 / 步：ai_messages.tool_rounds 之和 / tool_calls 数组长度之和。
//   - 工具用时：agent_events 里 tool_result 的 payload.ms 之和。
//   - 总用时：每条 run 的 updated_at - created_at 之和（含工具等待）。
//   - 模型用时 = 总用时 - 工具用时（下限 0）——我们不逐 chunk 记模型时间，
//     这是最接近 DSH「模型用时」的近似；工具间隙也算进模型侧，宁可略高。
//   - TTFT：done 事件的 firstTokenMs 平均值。
//   - TPS：completion_tokens 总和 / 模型用时秒数。
//   - Token：ai_messages 的 prompt/completion/cached 之和（**含**并进消息行的
//     记忆整理用量，见 AddAIMessageUsage）；未缓存输入 = prompt - cached，
//     缓存命中率 = cached / prompt。

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"quickdock/services"
)

// AISessionStatsView 会话统计快照。
type AISessionStatsView struct {
	ConversationID string `json:"conversation_id"`
	// —— 会话统计 ——
	Rounds       int `json:"rounds"`       // 工具轮数
	Steps        int `json:"steps"`        // 工具调用数
	ModelMs      int `json:"model_ms"`     // 模型用时（毫秒，近似）
	ToolMs       int `json:"tool_ms"`      // 工具调用用时（毫秒）
	AvgFirstMs   int `json:"avg_first_ms"` // 平均首字延迟
	TPS          int `json:"tps"`          // 输出速度 token/秒
	RequestCount int `json:"request_count"`
	// —— Token 用量 ——
	TotalTokens   int    `json:"total_tokens"`
	UncachedInput int    `json:"uncached_input"`
	CacheRead     int    `json:"cache_read"`
	Output        int    `json:"output"`
	CacheHitRate  int    `json:"cache_hit_rate"` // 0-100
	Model         string `json:"model"`          // 用量最多的提供方/模型名（弹框「本轮用量」首行）
	// ModelDetails 本次会话用到的全部模型（按 token 用量降序）。一次会话可能中途
	// 换模型，弹框「本轮用量」的首行只放 top1，这里让「比较多」的模型也能全部看到。
	ModelDetails []AIModelUsage `json:"model_details"`
}

// AIModelUsage 单个模型的用量：模型名 + 它烧的 token + 它被调了几次。
type AIModelUsage struct {
	Model    string `json:"model"`
	Tokens   int    `json:"tokens"`
	Requests int    `json:"requests"`
}

// AISessionStats 返回一条会话的统计快照。
func (a *AIService) AISessionStats(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("缺少会话 id")
	}

	v := AISessionStatsView{ConversationID: convID}

	// —— token 与轮次：ai_messages 聚合 ——
	rows, err := a.App.DB.Query(
		`SELECT prompt_tokens, completion_tokens, cached_tokens, tool_rounds, tool_calls, model
		 FROM ai_messages WHERE conv_id = ? AND role = 'assistant'`, convID)
	if err != nil {
		return services.Fail(err)
	}
	var prompt, cached int
	// 模型按用量归并，取烧 token 最多的那个当「本轮用量」的提供方/模型——
	// 一次会话可能中途换模型，显示用得最多的那个比「最后一条」更有代表性。
	type modelAgg struct{ tokens, requests int }
	modelAggs := map[string]modelAgg{}
	for _, row := range rows {
		p := intOf(row["prompt_tokens"])
		c := intOf(row["completion_tokens"])
		cc := intOf(row["cached_tokens"])
		prompt += p
		cached += cc
		v.Output += c
		v.TotalTokens += p + c
		if model, _ := row["model"].(string); model != "" {
			a := modelAggs[model]
			a.tokens += p + c
			a.requests++
			modelAggs[model] = a
		}
		v.Rounds += intOf(row["tool_rounds"])
		if s, _ := row["tool_calls"].(string); s != "" {
			var calls []json.RawMessage
			if json.Unmarshal([]byte(s), &calls) == nil {
				v.Steps += len(calls)
			}
		}
		v.RequestCount++
	}
	// —— 记忆整理调用的用量已经在消息行里 ——
	//
	// 记忆整理（kind=memory，见 ai_memory_auto.go）跑完会把用量并进它所属那条
	// 助手回复的 prompt/completion/cached 列（AddAIMessageUsage）。所以上面按
	// ai_messages 聚合出来的 token 已经包含它——这里不要再从 ai_usage 加一遍，
	// 那会重复计算。轮 / 步 / 请求次数仍然只描述问答本身。
	chatOutput := v.Output

	v.CacheRead = cached
	if prompt > cached {
		v.UncachedInput = prompt - cached
	}
	if prompt > 0 {
		rate := cached * 100 / prompt
		// cached 大于 prompt 只可能来自上游计数异常（个别网关把命中块重复累加，
		// 或缓存命中数与 prompt 不是同一口径）。夹到 100：界面上出现 137%
		// 比"缓存很好"更容易让人以为统计坏了。
		if rate > 100 {
			rate = 100
		}
		v.CacheHitRate = rate
	}
	for model, agg := range modelAggs {
		v.ModelDetails = append(v.ModelDetails, AIModelUsage{
			Model: model, Tokens: agg.tokens, Requests: agg.requests,
		})
	}
	// 用量降序：首行 top1 与明细第一行一致；token 相同时按模型名排，避免 map 遍历的随机顺序。
	sort.Slice(v.ModelDetails, func(i, j int) bool {
		if v.ModelDetails[i].Tokens != v.ModelDetails[j].Tokens {
			return v.ModelDetails[i].Tokens > v.ModelDetails[j].Tokens
		}
		return v.ModelDetails[i].Model < v.ModelDetails[j].Model
	})
	if len(v.ModelDetails) > 0 {
		v.Model = v.ModelDetails[0].Model
	}

	// —— 用时 / TTFT：agent_runs + agent_events 聚合 ——
	type span struct{ start, end time.Time }
	runs, err := a.App.DB.ListAgentRunsByConv(convID, 200)
	if err != nil {
		return services.Fail(err)
	}
	var totalMs, toolMs, firstSum, firstN int
	for _, run := range runs {
		st, e1 := time.Parse(time.RFC3339, run.CreatedAt)
		en, e2 := time.Parse(time.RFC3339, run.UpdatedAt)
		if e1 == nil && e2 == nil && en.After(st) {
			totalMs += int(en.Sub(st).Milliseconds())
		}
		events, eerr := a.App.DB.ListAgentEvents(run.ID, 0, 500)
		if eerr != nil {
			continue
		}
		for _, ev := range events {
			var p map[string]any
			if json.Unmarshal([]byte(ev.Payload), &p) != nil {
				continue
			}
			switch ev.Kind {
			case "tool_result":
				toolMs += intOf(p["ms"])
			case "done":
				if ms := intOf(p["firstTokenMs"]); ms > 0 {
					firstSum += ms
					firstN++
				}
			}
		}
	}
	v.ToolMs = toolMs
	v.ModelMs = totalMs - toolMs
	if v.ModelMs < 0 {
		v.ModelMs = 0
	}
	if firstN > 0 {
		v.AvgFirstMs = firstSum / firstN
	}
	if v.ModelMs >= 1000 {
		// 分子用聊天自身的输出量：记忆整理并进消息行的那部分输出不是这段 run
		// 时间里产生的，算进来会把 TPS 抬成一个不存在的速度。
		v.TPS = chatOutput * 1000 / v.ModelMs
	}

	return services.Ok(v)
}

func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
