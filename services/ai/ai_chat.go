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
	"strconv"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
)

// 上游失败重试（WP15）。
//
// 为什么必须有：长任务跑到第三十轮被限流一次，整轮就作废——前面二十九轮的
// 上下文与费用全部沉没，用户只看到「HTTP 429」。而 429/5xx 本来就是**可重试**的
// 语义（跟参数错误、鉴权失败完全不同类）。
//
// 三条边界，缺一条都会帮倒忙：
//   - 只重试「还没开始吐字」的阶段：这里只包住建连 + 等响应头，正文一旦开始
//     流式读取就不再重试（重试会让用户看到同一段话两遍）；
//   - 用户停止 / 看门狗超时一律不重试：那是我们自己取消的，重试等于无视用户；
//   - 4xx（除 429）不重试：Key 不对、参数不对，重试一万次也一样。
const (
	aiRetryMax      = 3
	aiRetryBaseWait = time.Second
	aiRetryMaxWait  = 8 * time.Second
)

// aiRetryableStatus 判断状态码是否值得重试。
func aiRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429 限流
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// aiRetryDelay 下一次重试等多久：优先照上游的 Retry-After，否则指数退避。
func aiRetryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		// Retry-After 有两种格式（秒数 / HTTP-date），只处理秒数：
		// date 形式在网关里几乎见不到，为它引一套时间解析不值。
		if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
			if secs, e := strconv.Atoi(v); e == nil && secs >= 0 {
				d := time.Duration(secs) * time.Second
				if d > aiRetryMaxWait {
					d = aiRetryMaxWait
				}
				return d
			}
		}
	}
	d := aiRetryBaseWait << max(attempt-1, 0)
	if d > aiRetryMaxWait {
		d = aiRetryMaxWait
	}
	return d
}

// 上游空闲看门狗的两个窗口。
//
// http.Client 上只配了 ResponseHeaderTimeout（等响应头），一旦上游把 200 和
// 响应头发过来、之后再也不吐字，scanner.Scan() 会**一直**阻塞：对话停在转圈，
// 没有超时、没有报错，用户只能自己点停止——而停止还会被当成「用户主动中断」。
// 网络中间层（代理、网关）掐掉长连接时经常是这个形态。
const (
	// aiStreamIdleFirst 首个数据块的最长等待。开了 thinking 的模型在出字之前要
	// 先想一阵，这个窗口必须比 ResponseHeaderTimeout(60s) 宽，否则会把正常的
	// 长思考误判成挂死。
	aiStreamIdleFirst = 180 * time.Second
	// aiStreamIdleGap 两个数据块之间的最长静默。正常流式每几百毫秒一块，
	// 90 秒没有任何一行（连 keep-alive 注释都没有）就基本可以断定连接死了。
	aiStreamIdleGap = 90 * time.Second
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
	// FinishReason 是上游 SSE 给的收尾原因（stop / length / tool_calls / content_filter）。
	// 必须解析：thinking 模型把 max_tokens 花在 reasoning 上会被 length 截断，
	// 此时 Content 和 ToolCalls 都是空的——不带上它，调用方只能看到「空回复」，
	// 分不清是模型不想答、还是被截断了，自然也谈不上对症重试。
	FinishReason string
}

// streamAIChat 流式调用模型。
//
// tools 为 nil 时不带工具（一次性问答、摘要、标题生成都走这条路）；
// 非 nil 时模型可能返回 tool_calls，此时 Content 可能为空——这不代表失败，
// 调用方必须看 ToolCalls 是否为空，否则会把「模型决定先查一下」误判成空回复。
func (a *AIService) streamAIChat(ctx context.Context, cfg AIProfile, messages []map[string]interface{}, convID string, tools []map[string]any, onToken func(text string), onReasoning func(text string)) (chatTurn, error) {
	// WP4a：剥掉循环内部的轮次标记再发。
	//
	// 标记是给我们自己看的（决定折叠哪几轮），但 messages 里的 map 就是请求体——
	// 多一个未知字段，严格的网关会直接 400，而这种错只在某些 provider 上出现。
	// 放在这一个总入口而不是各个调用方：只有一处需要记得。
	messages = stripInternalMessageKeys(messages)
	// 链路日志只记「元数据」（模型/条数/耗时/用量），绝不记消息正文——
	// AI 对话可能含用户隐私，日志要能排障但不能泄密。
	startedAt := time.Now()
	var firstTokenAt time.Duration // 首字延迟：判断「慢」是上游排队还是本地处理的关键
	var usage db.TokenUsage
	// 上游分块统计。只有「首字 / 总耗时」两个数字时，分不清「上游整段回包」
	// 和「上游逐块、只是吐得快」——这两种在页面上都是「一下全出来」。
	// 记块数与相邻块最大间隔，复现一次就能定论：块=1 是上游不分块，
	// 块=200 且间隔很小就是输出本身太快，前端没有责任。
	var chunkN int
	var lastChunkAt, maxChunkGap time.Duration
	logDone := func(outLen, toolCount int) {
		note := ""
		// 长文本只收到一两块 = 上游没做增量（中转网关 buffering / 忽略 stream），
		// 这条标记让下次排查不用再猜。短文本本来就可能只有一块，不打标。
		if chunkN <= 2 && outLen > 200 {
			note = " 上游疑似未分块下发"
		}
		logger.I("[ai] 完成 model=%s conv=%s 耗时=%v 首字=%v 输出=%d字 工具=%d 块=%d 块间隔max=%v tokens=%d/%d 缓存=%d%s",
			cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond),
			firstTokenAt.Round(time.Millisecond), outLen, toolCount, chunkN,
			maxChunkGap.Round(time.Millisecond), usage.Prompt, usage.Completion, usage.Cached, note)
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
	applyThinkingSwitch(body, cfg)
	applyReasoningLevel(body, cfg)
	raw, err := json.Marshal(body)
	if err != nil {
		return chatTurn{}, err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	// 空闲看门狗：派生一个 ctx，超时后取消它，transport 会因此关掉连接，
	// 阻塞在 scanner.Scan() 上的读立刻返回。父 ctx 取消（用户停止）照样生效。
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	idle := time.AfterFunc(aiStreamIdleFirst, cancelStream)
	defer idle.Stop()
	// idleFired 区分「看门狗掐的」和「用户/上层掐的」：后者要让调用方按中断
	// 处理（保留半截正文、标 stopped），前者是一次可归因的失败。
	idleFired := func() bool { return streamCtx.Err() != nil && ctx.Err() == nil }

	client := a.aiHTTPClient
	// 重试循环只覆盖「建连 → 拿到响应头」这一段：正文开始流式读取之后就不能再
	// 重试了（会让用户看到同一段话两遍），那之后的失败交给调用方按中断处理。
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		// 每次都要新建请求：body 已被上一次读完，复用会发出空 body。
		req, rerr := http.NewRequestWithContext(streamCtx, http.MethodPost, ep, bytes.NewReader(raw))
		if rerr != nil {
			return chatTurn{}, rerr
		}
		req.Header.Set("Content-Type", "application/json")
		if authVal != "" {
			req.Header.Set(authKey, authVal)
		}
		// 导致逐字流式退化成"一次性整段"。强制 identity 让上游返回原始分块 SSE。
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Cache-Control", "no-store")

		if attempt == 0 {
			logger.I("[ai] 请求 model=%s provider=%s conv=%s messages=%d", cfg.Model, cfg.Provider, convID, len(messages))
		} else {
			logger.I("[ai] 第 %d/%d 次重试 model=%s provider=%s conv=%s", attempt, aiRetryMax, cfg.Model, cfg.Provider, convID)
		}

		var derr error
		resp, derr = client.Do(req)
		if derr == nil && resp.StatusCode == http.StatusOK {
			break
		}

		// 先判断「该不该重试」，再决定要不要把错误报出去。
		retryable := derr != nil || (resp != nil && aiRetryableStatus(resp.StatusCode))
		// 用户点停止 / 看门狗掐断：这两种都是我们自己取消的，重试等于无视用户。
		// 看门狗的错误由下面统一给出可读文案，不在这里返回。
		canceled := streamCtx.Err() != nil
		if !retryable || canceled || attempt >= aiRetryMax {
			break
		}

		wait := aiRetryDelay(resp, attempt+1)
		if resp != nil {
			status := resp.StatusCode
			// 上一发的 body 必须关掉再重试，否则连接一直占着（Transport 的
			// idle 连接池会膨胀，长任务里能明显看出句柄数上升）。
			_ = resp.Body.Close()
			resp = nil
			logger.W("[ai] 上游 HTTP %d，%v 后重试（第 %d/%d 次）model=%s conv=%s",
				status, wait, attempt+1, aiRetryMax, cfg.Model, convID)
			// WP15 尾巴：让界面知道「在重试，不是卡死」。
			// 用户看不到这条时最常见的反应是点停止，而那正好把
			// 一次马上要成功的请求掐掉。
			notifyRetry(streamCtx, attempt+1, wait, status)
		} else {
			logger.W("[ai] 请求失败（%v），%v 后重试（第 %d/%d 次）model=%s conv=%s",
				derr, wait, attempt+1, aiRetryMax, cfg.Model, convID)
			notifyRetry(streamCtx, attempt+1, wait, 0)
		}

		select {
		case <-streamCtx.Done():
			// 等退避期间被取消：可能是用户点停止，也可能是看门狗掐断。
			// 两种都立刻收敛，不再重试。
			if idleFired() {
				logger.E("[ai] 上游 %v 未返回响应头，判定空闲超时 model=%s conv=%s", aiStreamIdleFirst, cfg.Model, convID)
				return chatTurn{}, fmt.Errorf("上游超过 %v 没有返回响应（模型 %s），连接可能已断", aiStreamIdleFirst, cfg.Model)
			}
			if cErr := ctx.Err(); cErr != nil {
				return chatTurn{}, cErr
			}
			return chatTurn{}, streamCtx.Err()
		case <-time.After(wait):
		}
	}

	if resp == nil || resp.StatusCode != http.StatusOK {
		// 走到这里只有三种：不可重试的错误、重试用尽、被取消。
		if idleFired() {
			logger.E("[ai] 上游 %v 未返回响应头，判定空闲超时 model=%s conv=%s", aiStreamIdleFirst, cfg.Model, convID)
			return chatTurn{}, fmt.Errorf("上游超过 %v 没有返回响应（模型 %s），连接可能已断", aiStreamIdleFirst, cfg.Model)
		}
		if resp != nil {
			eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			logger.E("[ai] 上游返回异常 model=%s conv=%s status=%d body=%s",
				cfg.Model, convID, resp.StatusCode, logger.Trunc(string(eb), 300))
			return chatTurn{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
		}
		if cErr := ctx.Err(); cErr != nil {
			return chatTurn{}, cErr
		}
		if cErr := streamCtx.Err(); cErr != nil {
			return chatTurn{}, cErr
		}
		logger.E("[ai] 请求失败 model=%s conv=%s 耗时=%v", cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond))
		return chatTurn{}, fmt.Errorf("请求上游失败（模型 %s），已重试 %d 次", cfg.Model, aiRetryMax)
	}
	defer resp.Body.Close()

	var sb strings.Builder
	// 收尾原因（stop / length / tool_calls / …）：thinking 模型被 max_tokens
	// 掐断时它是最直接的证据，必须一路带回给 agent 主循环。
	var finishReason string
	// tool_calls 是分片下发的：第一片带 index/id/name，后续分片只带
	// arguments 的字符串片段、不再带 id。必须按 index 累积，最后才是一条完整调用。
	// 直接按片 append 会得到一堆半截 JSON，且各网关分片粒度不同，本地很难复现。
	pendingTools := map[int]*aiToolCall{}
	// 有模型会把工具调用写成正文（DSML 标记，见 ai_dsml.go）：过滤掉、另存一份，
	// 既不让用户看到原始标记，也不丢掉这次调用。
	dsml := newDSMLFilter()
	scanner := bufio.NewScanner(resp.Body)
	// 上限 16MB：模型整段回大 tool_calls（一次性写上百 KB 的文件、或网关不做
	// 增量而整段回包）时，1MB 会让整条流以 "token too long" 直接中断且难归因。
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		// 收到任何一行（含 keep-alive 注释）都算「连接还活着」，重置静默窗口。
		idle.Reset(aiStreamIdleGap)
		select {
		case <-ctx.Done():
			// 取消时也要把过滤器里的可见残片放出来，否则用户点了停止会连带丢掉最后半句。
			sb.WriteString(dsml.Finish())
			logger.I("[ai] 已取消 model=%s conv=%s 耗时=%v 已输出=%d字",
				cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), len([]rune(sb.String())))
			return chatTurn{Content: sb.String(), Usage: usage, ToolCalls: flushToolCalls(pendingTools), FinishReason: finishReason}, ctx.Err()
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
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
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
		// 分块统计放在 choices 判定之后：include_usage 那一块（choices 为空）
		// 和 keep-alive 注释都不算内容块，混进来会把「上游只回一块」洗白。
		nowChunkAt := time.Since(startedAt)
		if chunkN > 0 {
			if gap := nowChunkAt - lastChunkAt; gap > maxChunkGap {
				maxChunkGap = gap
			}
		}
		chunkN++
		lastChunkAt = nowChunkAt
		delta := chunk.Choices[0].Delta
		if fr := chunk.Choices[0].FinishReason; fr != "" {
			finishReason = fr
		}
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
	// 先看是不是被看门狗掐的：那时 scanner.Err() 也是一个"context canceled"，
	// 直接归到下面那条分支会写成「流读取中断」，看不出真正原因是上游不吐字。
	if idleFired() {
		logger.W("[ai] 上游超过 %v 无任何数据，判定空闲超时 model=%s conv=%s 耗时=%v 已输出=%d字",
			aiStreamIdleGap, cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), len([]rune(sb.String())))
		// 半截正文照常返回：调用方会落库，与「用户停止」同一口径处理。
		return chatTurn{Content: sb.String(), Usage: usage, ToolCalls: calls, FinishReason: finishReason},
			fmt.Errorf("上游超过 %v 没有返回任何数据，连接可能已被中间层断开（模型 %s）", aiStreamIdleGap, cfg.Model)
	}
	if err := scanner.Err(); err != nil {
		logger.E("[ai] 流读取中断 model=%s conv=%s 耗时=%v 已输出=%d字: %v",
			cfg.Model, convID, time.Since(startedAt).Round(time.Millisecond), len([]rune(sb.String())), err)
		return chatTurn{Content: sb.String(), Usage: usage, ToolCalls: calls, FinishReason: finishReason}, err
	}
	out := sb.String()
	logDone(len([]rune(out)), len(calls))
	return chatTurn{Content: out, Usage: usage, ToolCalls: calls, FinishReason: finishReason}, nil
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
//
// sink 只用来播报压缩过程（compact 事件）；传 nil 时压缩照做，只是不说。
func (a *AIService) buildAIMessages(ctx context.Context, cfg AIProfile, convID, mode string, sink EventSink) ([]map[string]interface{}, error) {
	return a.buildAIMessagesWithin(ctx, cfg, convID, mode, aiContextBudget(cfg), false, sink)
}

// compactEvent compact 事件的载荷。
//
// 字段偏多是有意的：界面上那两行分隔字要同时回答三件事——有没有在压（phase）、
// 压掉了多少（before → after）、这条会话的天花板在哪（window / ceiling / limit）。
// 少了天花板，用户看到「48.2K → 12.1K」也无从判断这个 48.2K 算大还是算小，
// 而这恰恰是他会追问的下一句。
//
// measured 是上一发请求上游实报的输入 token：与 before（我们的估算）一起看
// 就知道自己的估法是高估还是低估，排查「总是提前压」时是关键线索。
func compactEvent(kind, phase string, before, after, dropped, kept int, lim aiContextLimits, budget int, summarized, attempted bool) map[string]any {
	return map[string]any{
		// kind 区分两种压缩：history = 真调模型做摘要（贵、可能失败），
		// loop = 循环内折叠较早的工具结果（几毫秒、不会失败）。
		// 前端必须能分开——不然一次折叠会被显示成「摘要失败，已直接截断」。
		"kind":     kind,
		"phase":    phase,
		"before":   before,
		"after":    after,
		"dropped":  dropped,
		"kept":     kept,
		"window":   lim.Window,
		"ceiling":  lim.Ceiling,
		"limit":    budget,
		"floor":    lim.Floor,
		"measured": lim.Measured,
		"summary":  summarized,
		// attempted 这次到底有没有真的去调模型摘要。失败时的文案只在
		// attempted && !summary 才该出现（见前端 aiCompactTruncated）。
		"attempted": attempted,
	}
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
func keepRecentCount(hist []db.AIMessage, budget int, cfg AIProfile) int {
	lo, hi := keepRecentOf(cfg)
	limit := budget * 45 / 100
	used, keep := 0, 0
	for i := len(hist) - 1; i >= 0 && keep < hi; i-- {
		used += estimateMessageTokens(hist[i])
		keep++
		if used >= limit && keep >= lo {
			break
		}
	}
	if keep < lo {
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
//
// sink 非空时在压缩前后各发一条 compact 事件：摘要是一次真正花钱、要等几秒的
// 模型调用，不告诉用户，表现就是「发出去之后卡住半天，然后 AI 忽然不记得前文了」。
func (a *AIService) buildAIMessagesWithin(ctx context.Context, cfg AIProfile, convID, mode string, budget int, force bool, sink EventSink) ([]map[string]interface{}, error) {
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
	keep := keepRecentCount(hist, budget, cfg)
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
			// 压缩前先播报：摘要本身是一次要等几秒的模型调用，
			// 这几秒里界面上必须有个「正在压缩」的说法。
			before := estTokens(hist)
			lim := contextLimitsOf(cfg)
			lim.Measured = lastRequestSize(convID)
			// attempted 表示这次是否真的发起了摘要调用。
			//
			// 冷却期内不发：摘要失败通常不是偶发（超窗、上游拒绝、模型不认这个
			// 端点），每轮都重试等于每轮白付一次调用 + 弹一条「摘要失败」。
			// 冷却期仍然硬截断，对话能继续，只是这段时间里不再尝试摘要。
			attempt := !summaryCoolingDown(convID)
			if sink != nil {
				sink.Event("compact", compactEvent(compactKindHistory, "start", before, 0, len(toSum), len(recent), lim, budget, false, attempt))
			}
			summarized := false
			if !attempt {
				logger.I("[ai] 摘要冷却中，直接硬截断 conv=%s", convID)
			}
			if attempt {
				if summary, serr := a.summarizeAndStore(ctx, cfg, conv, toSum); serr == nil {
					_ = a.App.DB.DeleteOldAIMessages(convID, keep)
					hist = recent
					conv.Summary = summary
					summarized = true
					clearSummaryFailure(convID)
				} else {
					// 摘要失败（多为摘要请求自己也超窗或上游抖动）：不能就这么算了，
					// 否则每轮都会拿同一份超长历史重试、每次都失败、每轮都撞 400。
					// 降级为硬截断——丢掉最旧的那部分，只留最近 keep 条。
					// 这点信息损失换来的是对话还能继续，比彻底卡死好。
					logger.W("[ai] 摘要压缩失败 conv=%s，降级为硬截断: %v", convID, serr)
					markSummaryFailure(convID)
					_ = a.App.DB.DeleteOldAIMessages(convID, keep)
					hist = recent
				}
			} else {
				_ = a.App.DB.DeleteOldAIMessages(convID, keep)
				hist = recent
			}
			if sink != nil {
				sink.Event("compact", compactEvent(compactKindHistory, "done", before, estTokens(recent), len(toSum), len(recent), lim, budget, summarized, attempt))
			}
			// 记一笔给弹框的「压缩状态」（WP2）：这是真调了一次模型的摘要，
			// 与循环内的折叠要分开标（前者贵、后者几毫秒）。
			rememberCompaction(convID, before, estTokens(recent), 0, len(toSum), summarized)
		}
	}

	// 旧 mode（explain/translate/summarize）已降级为一次性动作，
	// 这里统一拿 chat 的提示词，约束由 aiActionInstruction 拼在 user 消息尾部。
	system := aiPromptFor(mode)
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
	if conv.Summary != "" {
		system += "\n\n[历史对话摘要]\n" + conv.Summary
	}
	// 记忆：项目任务读到的是 <cwd>/.quickDock（跟着项目走），无空间任务读到的是
	// 数据目录下跨任务共享的那份（它们的 cwd 是系统新建的空目录，按 cwd 存等于每条
	// 对话都从零开始——用户看到的就是「记忆全是默认的」）。
	//
	// 先 ensureMemoryProfile：若整体记忆还不存在，确定性扫描项目写一份基线画像，
	// 保证 .quickDock 一定被建起来（而不是让模型去根目录生成 AGENTS.md 当记忆）。
	// 计划模式是只读的，不在此写任何文件（含 AI 自己的记忆目录）。
	if mode != "plan" {
		ensureMemoryProfile(conv.Cwd)
	}
	if mem := memoryBlock(conv.Cwd); mem != "" {
		system += "\n\n" + mem
	}
	// 技能：这条对话选中的 SKILL.md（没选过则回落 skills.json 的全局默认）。
	// 只给「名字 + 适用场景 + 路径」，正文由模型需要时自己 file_read ——
	// 技能写得多长都不占常态上下文。
	if sk := convSkillsPrompt(conv.Skills); sk != "" {
		system += sk
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
	// WP11：摘要属于「输入长、输出短、失败不致命」那一类，换廉价档。
	cfg = aiCheapProfile(cfg)
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
	applyThinkingSwitch(body, cfg)
	applyReasoningLevel(body, cfg)
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
