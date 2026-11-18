package ai

// 循环内消息的轮次索引（WP4a）。
//
// 问题：`compactLoopMessages` 判断「哪些消息属于最后一轮」靠**反向扫数组**——
// 从尾部找第一条 `role=tool` 之前那个带 `tool_calls` 的 assistant。在当前
// 「只折叠更早的工具结果」的策略下够用，但它没有「第几轮」这个概念，
// 支撑不了分层压缩（最近 2 轮不动 / 3–8 轮折叠 / 更早摘要）。
//
// 做法：给循环内的消息打一个内存标记（不落库、不发给上游）。
// 标记必须在上游请求前剥掉——多一个未知字段，严格的网关会直接 400，
// 而这种错只在某些 provider 上出现，本地测不出来。

import "strings"

// aiMsgRoundKey 轮次标记的内部键名。
//
// 用 `_ai` 前缀而不是 `round`：这个 map 就是请求体，键名要一眼能看出
// 「这是我们自己加的、必须剥掉」。
const aiMsgRoundKey = "_aiRound"

// tagMessagesRound 给还没标记的消息打上轮次。
//
// 只标「还没有标记」的：一轮里可能有多次 append（assistant + tool 批次、
// 用户插话、后台任务通知），逐个 append 处各算一次轮次容易漏；
// 统一在轮次边界调一次，没标的都归当前轮。
func tagMessagesRound(messages []map[string]interface{}, round int) {
	for _, m := range messages {
		if m == nil {
			continue
		}
		if _, ok := m[aiMsgRoundKey]; ok {
			continue
		}
		m[aiMsgRoundKey] = round
	}
}

// messageRoundOf 取一条消息的轮次；没有标记返回 -1。
func messageRoundOf(m map[string]interface{}) int {
	if m == nil {
		return -1
	}
	switch v := m[aiMsgRoundKey].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		// JSON 往返之后数字会变 float64（工具结果回放、快照恢复）。
		return int(v)
	}
	return -1
}

// stripInternalMessageKeys 返回剥掉内部标记的副本。
//
// 浅拷贝 map 而不是深拷贝：只为了去掉一个键，没必要把几十 KB 的
// tool 结果再复制一遍。原始 messages 仍然带标记（下一轮还要用）。
func stripInternalMessageKeys(messages []map[string]interface{}) []map[string]interface{} {
	needCopy := false
	for _, m := range messages {
		if m == nil {
			continue
		}
		if _, ok := m[aiMsgRoundKey]; ok {
			needCopy = true
			break
		}
	}
	if !needCopy {
		// 绝大多数调用（标题生成、摘要、外部复用）都没有标记，直接穿透。
		return messages
	}
	out := make([]map[string]interface{}, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			out = append(out, m)
			continue
		}
		clean := make(map[string]interface{}, len(m))
		for k, v := range m {
			if k == aiMsgRoundKey {
				continue
			}
			clean[k] = v
		}
		out = append(out, clean)
	}
	return out
}

// resolveMessageRounds 给每条消息算出轮次（下标对齐 messages）。
//
// 优先用标记；一条标记都没有时退回**结构化推导**（按 assistant.tool_calls
// 切轮）。为什么保留退路：单测、外部调用方、以及「标记机制上线前就在跑的长
// 对话」都不会有标记，折叠逻辑不能因此整个失效。
func resolveMessageRounds(messages []map[string]interface{}) []int {
	rounds := make([]int, len(messages))
	marked := false
	for i, m := range messages {
		if r := messageRoundOf(m); r >= 0 {
			marked = true
			rounds[i] = r
			continue
		}
		rounds[i] = -1
	}
	if marked {
		// 没标记的按「沿用上一条的轮次」补：标记只在轮次边界写，
		// 中间夹着的消息（插话、通知）本来就属于同一轮。
		prev := 0
		for i := range rounds {
			if rounds[i] < 0 {
				rounds[i] = prev
				continue
			}
			prev = rounds[i]
		}
		return rounds
	}
	// 结构化退路：assistant(tool_calls) 开启新的一轮，其后的 tool 消息归该轮。
	cur := 0
	for i, m := range messages {
		if m == nil {
			rounds[i] = cur
			continue
		}
		if m["role"] == "assistant" {
			if _, ok := m["tool_calls"]; ok {
				cur++
			}
		}
		rounds[i] = cur
	}
	return rounds
}

// lastRoundOf 返回最大轮次（没有消息时返回 0）。
func lastRoundOf(rounds []int) int {
	last := 0
	for _, r := range rounds {
		if r > last {
			last = r
		}
	}
	return last
}

// isInternalMessageKey 判断一个键是不是我们自己的内部键（调试与测试用）。
func isInternalMessageKey(k string) bool {
	return strings.HasPrefix(k, "_ai")
}
