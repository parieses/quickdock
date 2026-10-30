package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// chatTurn 一次模型调用的结果。
//
// 用结构体而不是继续加返回值：加上工具调用后已经有四个返回值，而
// (string, db.TokenUsage, []aiToolCall, error) 这种形状在调用点上极易读错顺序
// —— 三个都能用短变量声明接住，传反了编译器不会报错。
type chatTurn struct {
	Content   string
	Usage     db.TokenUsage
	ToolCalls []aiToolCall
}

// streamAIChat 流式调用模型。
//
// tools 为 nil 时不带工具（一次性问答、摘要、标题生成都走这条路）；
// 非 nil 时模型可能返回 tool_calls，此时 Content 可能为空——这不代表失败，
// 调用方必须看 ToolCalls 是否为空，否则会把「模型决定先查一下」误判成空回复。
func (a *AIService) streamAIChat(ctx context.Context, cfg AIProfile, messages []map[string]interface{}, convID string, tools []map[string]any, onToken func(text string), onReasoning func(text string)) (chatTurn, error) {
	// 链路日志只记「元数据」（模型/条数/耗时/用量），绝不记消息正文——
	// AI 对话可能含用户隐私，日志要能排障但不能泄密。
	startedAt := time.Now()
	var firstTokenAt time.Duration // 首字延迟：判断「慢」是上游排队还是本地处理的关键
	var usage db.TokenUsage
	logDone := func(outLen, toolCount int) {
		logger.I("[ai] 完成 model=%s conv=%s 耗时=%v 首字=%v 输出=%d字 工具=%d tokens=%d/%d 缓存=%d",
			cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond),
			firstTokenAt.Round(time.Millisecond), outLen, toolCount, usage.Prompt, usage.Completion, usage.Cached)
	}

	body := map[string]interface{}{
		"model":          cfg.Model,
		"messages":       messages,
		"temperature":    cfg.Temperature,
		"stream":         true,
		"stream_options": map[string]interface{}{"include_usage": true},
	}
	// 空 tools 不写字段：部分网关对 "tools": [] 直接 400，
	// 而空切片 marshal 出来就是 []，不会变成 null。
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	if cfg.MaxTokens > 0 {
		body["max_tokens"] = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	if cfg.ThinkingEnabled {
		body["enable_thinking"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return chatTurn{}, err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return chatTurn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authVal != "" {
		req.Header.Set(authKey, authVal)
	}
	// 导致逐字流式退化成"一次性整段"。强制 identity 让上游返回原始分块 SSE。
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")

	client := a.aiHTTPClient
	logger.I("[ai] 请求 model=%s provider=%s conv=%s messages=%d", cfg.Model, cfg.Provider, convID, len(messages))
	resp, err := client.Do(req)
	if err != nil {
		logger.E("[ai] 请求失败 model=%s conv=%s 耗时=%v: %v",
			cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), err)
		return chatTurn{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.E("[ai] 上游返回异常 model=%s conv=%s status=%d body=%s",
			cfg.Model, convID, resp.StatusCode, logger.Trunc(string(eb), 300))
		return chatTurn{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}

	var sb strings.Builder
	// tool_calls 是分片下发的：第一片带 index/id/name，后续分片只带
	// arguments 的字符串片段、不再带 id。必须按 index 累积，最后才是一条完整调用。
	// 直接按片 append 会得到一堆半截 JSON，且各网关分片粒度不同，本地很难复现。
	pendingTools := map[int]*aiToolCall{}
	// 有模型会把工具调用写成正文（DSML 标记，见 ai_dsml.go）：过滤掉、另存一份，
	// 既不让用户看到原始标记，也不丢掉这次调用。
	dsml := newDSMLFilter()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			// 取消时也要把过滤器里的可见残片放出来，否则用户点了停止会连带丢掉最后半句。
			sb.WriteString(dsml.Finish())
			logger.I("[ai] 已取消 model=%s conv=%s 耗时=%v 已输出=%d字",
				cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), len([]rune(sb.String())))
			return chatTurn{Content: sb.String(), Usage: usage, ToolCalls: flushToolCalls(pendingTools)}, ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"` // Agnes/DeepSeek thinking 模式
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *openAIUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			// 先过 DSML 过滤器：工具调用段落不推给前端，也不进 sb（那是要被展示与落库的正文）。
			if vis := dsml.Feed(delta.Content); vis != "" {
				if firstTokenAt == 0 {
					firstTokenAt = time.Since(startedAt)
				}
				sb.WriteString(vis)
				if onToken != nil {
					onToken(vis)
				}
			}
		}
		if delta.ReasoningContent != "" && onReasoning != nil {
			onReasoning(delta.ReasoningContent)
		}
		for _, tc := range delta.ToolCalls {
			cur := pendingTools[tc.Index]
			if cur == nil {
				cur = &aiToolCall{}
				pendingTools[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Name = tc.Function.Name
			}
			// 追加而不是覆盖：写成 = 只会留下最后一段参数。
			cur.Args += tc.Function.Arguments
		}
	}
	// 流结束：把过滤器扣着的尾巴放出来。可见残片照常推给前端，
	// 被吞掉的那段（工具调用）尝试解析成真正的调用。
	if tail := dsml.Finish(); tail != "" {
		sb.WriteString(tail)
		if onToken != nil {
			onToken(tail)
		}
	}
	calls := flushToolCalls(pendingTools)
	if hidden := dsml.Hidden(); hidden != "" {
		if extra := parseDSMLToolCalls(hidden); len(extra) > 0 {
			// 模型把调用写成了正文：接过来继续走工具轮次，而不是让用户看半句话。
			logger.W("[ai] 模型把 %d 个工具调用输出成了 DSML 文本（%d 字），已按工具调用处理 conv=%s",
				len(extra), len([]rune(hidden)), convID)
			calls = append(calls, extra...)
		} else {
			// 解析不出来（排版残缺）：这段本来就不该给用户看，丢掉但留痕。
			logger.W("[ai] 丢弃无法解析的 DSML 文本 %d 字 conv=%s", len([]rune(hidden)), convID)
		}
	}
	if err := scanner.Err(); err != nil {
		logger.E("[ai] 流读取中断 model=%s conv=%s 耗时=%v 已输出=%d字: %v",
			cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), len([]rune(sb.String())), err)
		return chatTurn{Content: sb.String(), Usage: usage, ToolCalls: calls}, err
	}
	out := sb.String()
	logDone(len([]rune(out)), len(calls))
	return chatTurn{Content: out, Usage: usage, ToolCalls: calls}, nil
}

// flushToolCalls 把按 index 累积的调用整理成有序切片。
//
// 必须排序：map 迭代无序，而回传时 tool 结果消息的顺序要与 tool_calls 一一对应，
// 顺序错了上游会 400。同时丢掉 name 为空的残片——那通常是流被中途掐断留下的
// 半条记录，回传给模型只会换来一次「tool 不存在」。
func flushToolCalls(pending map[int]*aiToolCall) []aiToolCall {
	if len(pending) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(pending))
	for i, c := range pending {
		if c != nil && c.Name != "" {
			idxs = append(idxs, i)
		}
	}
	sort.Ints(idxs)
	out := make([]aiToolCall, 0, len(idxs))
	for _, i := range idxs {
		c := pending[i]
		if c.ID == "" {
			// 少数网关不回 id：没有 id 就无法把结果对回去，自己造一个稳定的。
			c.ID = fmt.Sprintf("call_%d", i)
		}
		out = append(out, *c)
	}
	return out
}

// buildAIMessages 组装发送给模型的消息列表，并自动执行摘要压缩。
// 当前用户消息已在调用前由 saveUserTurn 落库（见 ai_stream.go），
// 故这里仅从 DB 加载历史（已含本轮用户消息），不再单独追加。
func (a *AIService) buildAIMessages(ctx context.Context, cfg AIProfile, convID, mode string) ([]map[string]interface{}, error) {
	return a.buildAIMessagesWithin(ctx, cfg, convID, mode, aiContextBudget(cfg), false)
}

// estimateMessageTokens 单条历史消息的 token 估算。
//
// 按 1 token ≈ 1.6 字符折算（中文场景偏保守，宁可高估——低估的后果是
// 攒到超窗才被上游拒绝，而高估只是提前压一次）。
func estimateMessageTokens(m db.AIMessage) int {
	n := len([]rune(m.Content)) * 10 / 16
	// 图片不按字符估：一张图的 token 与文字量级完全不同，
	// 漏掉会让摘要压缩在“其实已经超窗”的会话上迟迟不触发。
	return n + countStoredImages(m.Images)*aiImageTokenEstimate
}

// keepRecentCount 在给定预算下「保留最近多少条不压缩」。
//
// 固定 12 条在两边都是错的：1M 窗口下留 12 条等于把大半个可用的历史
// 白压成摘要；8K 窗口下留 12 条可能压缩完还是超。所以按预算反推：
// 从尾部往前累加到用掉 45%（剩下的 55% 要给本轮回复、工具结果和
// 待压缩部分留位置），并夹在 [8, 40] 之间。
func keepRecentCount(hist []db.AIMessage, budget int) int {
	limit := budget * 45 / 100
	used, keep := 0, 0
	for i := len(hist) - 1; i >= 0 && keep < aiKeepRecentMax; i-- {
		used += estimateMessageTokens(hist[i])
		keep++
		if used >= limit && keep >= aiKeepRecentMin {
			break
		}
	}
	if keep < aiKeepRecentMin {
		keep = aiKeepRecentMin
	}
	if keep > len(hist) {
		keep = len(hist)
	}
	return keep
}

// buildAIMessagesWithin 按指定预算组装上下文。
//
// budget <= 0 时用自动预算。force 用于超窗兜底：上游已经报过 400 之后
// 不再等「待压缩段攒够 keep 条」——那时候必须真的压下去，否则重试
// 只会原样再撞一次。
func (a *AIService) buildAIMessagesWithin(ctx context.Context, cfg AIProfile, convID, mode string, budget int, force bool) ([]map[string]interface{}, error) {
	hist, err := a.App.DB.ListAIMessages(convID)
	if err != nil {
		return nil, err
	}
	conv, err := a.App.DB.GetAIConversation(convID)
	if err != nil {
		return nil, err
	}
	if budget <= 0 {
		budget = aiContextBudget(cfg)
	}

	estTokens := func(msgs []db.AIMessage) int {
		n := 0
		for _, m := range msgs {
			n += estimateMessageTokens(m)
		}
		return n
	}

	// 摘要压缩：历史超预算且足够长时，总结最旧部分并清理。
	// 只压缩「保留部分之前」的那一段，且要求它本身达到 keep 条——避免
	// 「保留 recent 条 + 每轮新增 1 条」导致每轮都重算摘要、无谓消耗 token。
	keep := keepRecentCount(hist, budget)
	if force && keep > len(hist)/2 {
		// 强制模式（上游已报超窗）：保留段本身就要砍一半。
		// 否则「历史 14 条、keep 8 条」时待压缩段只有 6 条，压完仍然超窗。
		keep = len(hist) / 2
		if keep < 2 {
			keep = 2
		}
	}
	if estTokens(hist) > budget && len(hist) > keep {
		toSum := hist[:len(hist)-keep]
		if len(toSum) >= keep || force {
			recent := hist[len(hist)-keep:]
			if summary, serr := a.summarizeAndStore(ctx, cfg, conv, toSum); serr == nil {
				_ = a.App.DB.DeleteOldAIMessages(convID, keep)
				hist = recent
				conv.Summary = summary
			} else {
				// 摘要失败（多为摘要请求自己也超窗或上游抖动）：不能就这么算了，
				// 否则每轮都会拿同一份超长历史重试、每次都失败、每轮都撞 400。
				// 降级为硬截断——丢掉最旧的那部分，只留最近 keep 条。
				// 这点信息损失换来的是对话还能继续，比彻底卡死好。
				logger.W("[ai] 摘要压缩失败 conv=%s，降级为硬截断: %v", convID, serr)
				_ = a.App.DB.DeleteOldAIMessages(convID, keep)
				hist = recent
			}
		}
	}

	system := aiModePrompts[mode]
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
	if conv.Summary != "" {
		system += "\n\n[历史对话摘要]\n" + conv.Summary
	}

	messages := []map[string]interface{}{{"role": "system", "content": system}}
	for _, m := range hist {
		// 带图片的 user 消息组装为 [text, image_url...] 块数组，其余保持纯字符串。
		// 图片按当前档案能力转换：本地图重读 base64，URL 图直发；能力不匹配的跳过。
		messages = append(messages, map[string]interface{}{
			"role":    m.Role,
			"content": a.messageContent(cfg, m.Role, m.Content, m.Images),
		})
	}
	return messages, nil
}

// aiSummaryInputRune 单次摘要调用喂进去的字符上限。
//
// 必须有上限：原来的写法把整段待压缩历史拼成一条 user 消息发给模型，
// 而这段历史正是刚刚判定为「太长」的那段——历史攒到 40 万 token 时，
// 摘要请求自己就先超窗 400，压缩永远失败，于是每一轮都拿同一份超长历史
// 再撞一次 400。40K 字符（≈25K token）任何模型的窗口都装得下。
const aiSummaryInputRune = 40000

// summarizeAndStore 调用模型把旧对话压缩为摘要并写回会话
func (a *AIService) summarizeAndStore(ctx context.Context, cfg AIProfile, conv *db.AIConversation, toSum []db.AIMessage) (string, error) {
	var b strings.Builder
	b.WriteString("请将以下对话压缩为简洁的中文摘要，保留关键事实、结论与待办，便于后续延续上下文。只输出摘要，不要解释：\n\n")
	for _, m := range toSum {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	body := clipSummaryInput(b.String())
	if body != b.String() {
		body += "\n\n（以上为较早对话，中间部分因过长已省略）"
	}
	msgs := []map[string]string{
		{"role": "system", "content": "你是一个压缩助手，负责把对话历史提炼为要点摘要。"},
		{"role": "user", "content": body},
	}
	summary, usage, err := a.callAIOnce(ctx, cfg, msgs)
	if err != nil {
		return "", err
	}
	a.recordAIUsage(conv.ID, usageKindSummary, cfg, usage)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("摘要为空")
	}
	combined := summary
	if conv.Summary != "" {
		combined = conv.Summary + "\n" + summary
	}
	// 防止摘要只增不减、system prompt 单调膨胀最终超出上下文窗口（HTTP 400）：
	// 合并后超过上限时仅保留最新一轮摘要。
	if len([]rune(combined)) > 2000 {
		combined = summary
	}
	if err := a.App.DB.UpdateAIConversationMeta(conv.ID, "", combined); err != nil {
		return "", err
	}
	return combined, nil
}

// clipSummaryInput 把摘要输入裁到上限内。
//
// 保留头 30% + 尾 70%：头部是会话的起点（用户最初要做什么），
// 尾部离现在最近（当前进展与结论），中间是最可以牺牲的过程细节。
// 只留尾部会丢掉原始需求，只留头部会丢掉最近的决定，两头都留最实用。
func clipSummaryInput(s string) string {
	r := []rune(s)
	if len(r) <= aiSummaryInputRune {
		return s
	}
	head := aiSummaryInputRune * 3 / 10
	tail := aiSummaryInputRune - head
	return string(r[:head]) + "\n\n[…中间省略…]\n\n" + string(r[len(r)-tail:])
}

// callAIOnce 一次性（非流式）调用，用于摘要压缩 / 标题生成
func (a *AIService) callAIOnce(ctx context.Context, cfg AIProfile, messages []map[string]string) (string, db.TokenUsage, error) {
	// 兜底：调用方漏传/传 nil ctx 时避免 http.NewRequestWithContext panic（如旧版标题生成）
	if ctx == nil {
		ctx = context.Background()
	}
	body := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
		"stream":      false,
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	if cfg.ThinkingEnabled {
		body["enable_thinking"] = true
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", db.TokenUsage{}, err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return "", db.TokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authVal != "" {
		req.Header.Set(authKey, authVal)
	}

	client := a.aiHTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return "", db.TokenUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", db.TokenUsage{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *openAIUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", db.TokenUsage{}, err
	}
	if len(out.Choices) == 0 {
		return "", db.TokenUsage{}, fmt.Errorf("无返回内容")
	}
	return out.Choices[0].Message.Content, out.Usage.toUsage(), nil
}

// callAIStream 已由 streamAIChat 取代（回调式，供本地 HTTP 流式服务逐段写出 NDJSON）。

// AITestConnection 发送一条简单的用户消息验证 API Key 和模型是否可用。
// 使用非流式单次调用，超时 15 秒。
