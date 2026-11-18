package ai

// WP4a：轮次索引。
//
// 三个不变量，任何一个错了都会让折叠逻辑悄悄失效（而且是「少折」或「折错轮」，
// 都不会报错）：
//  1. 标记不能漏到上游请求里 —— 严格的网关会因为多一个未知字段直接 400；
//  2. 没有标记时（单测、外部调用方、老对话）必须能退回结构化推导；
//  3. 折叠永远不碰最后一轮 —— 模型正要用它。

import (
	"fmt"
	"strings"
	"testing"
)

func msg(role string, extra map[string]any) map[string]interface{} {
	m := map[string]interface{}{"role": role, "content": strings.Repeat("x", 10)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func assistantCall() map[string]interface{} {
	return msg("assistant", map[string]any{"tool_calls": []map[string]any{{"id": "c1"}}})
}

// 标记必须能算进轮次；未标记的按「沿用上一条」补齐。
//
// 注意这里的构造方式必须与生产一致：**逐轮增量 append 后打标**。
// tagMessagesRound 只给未标记的打，一次性建完再补标记会把早期消息也一起
// 打成当前轮——那是调用顺序错误，不是函数的问题。
func TestResolveRoundsFromMarkers(t *testing.T) {
	msgs := []map[string]interface{}{msg("system", nil), msg("user", nil)}
	tagMessagesRound(msgs, 0)

	// 第 1 轮
	msgs = append(msgs, assistantCall(), msg("tool", nil))
	tagMessagesRound(msgs, 1)

	// 轮次之间插进来一条用户消息：属于同一轮（没有边界信息，沿用上一轮）。
	msgs = append(msgs, msg("user", nil))
	tagMessagesRound(msgs, 1)

	rounds := resolveMessageRounds(msgs)
	want := []int{0, 0, 1, 1, 1}
	for i := range want {
		if rounds[i] != want[i] {
			t.Errorf("第 %d 条轮次 = %d，期望 %d", i, rounds[i], want[i])
		}
	}
	if lastRoundOf(rounds) != 1 {
		t.Errorf("最大轮次 = %d，期望 1", lastRoundOf(rounds))
	}
	// 幂等：再打一次同一个轮次不改变任何东西。
	tagMessagesRound(msgs, 9)
	if r := resolveMessageRounds(msgs); r[4] != 1 {
		t.Errorf("已有标记被覆盖: %d", r[4])
	}
}

// 完全没有标记时退回结构化推导：assistant(tool_calls) 开新轮。
func TestResolveRoundsFallback(t *testing.T) {
	msgs := []map[string]interface{}{
		msg("system", nil),
		msg("user", nil),
		assistantCall(),
		msg("tool", nil),
		assistantCall(),
		msg("tool", nil),
	}
	rounds := resolveMessageRounds(msgs)
	want := []int{0, 0, 1, 1, 2, 2}
	for i := range want {
		if rounds[i] != want[i] {
			t.Errorf("第 %d 条轮次 = %d，期望 %d", i, rounds[i], want[i])
		}
	}
}

// 标记必须被剥掉，其它字段一个不能少。
func TestStripInternalMessageKeys(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "user", "content": "hi", aiMsgRoundKey: 3, "name": "n"},
		{"role": "tool", "content": "r", "tool_call_id": "c1", aiMsgRoundKey: 2},
	}
	out := stripInternalMessageKeys(msgs)
	if len(out) != 2 {
		t.Fatalf("条数变了: %d", len(out))
	}
	for i, m := range out {
		for k := range m {
			if isInternalMessageKey(k) {
				t.Errorf("第 %d 条仍带内部键 %q", i, k)
			}
		}
	}
	if out[0]["name"] != "n" || out[1]["tool_call_id"] != "c1" {
		t.Errorf("普通字段被误删: %v / %v", out[0], out[1])
	}
	// 原 messages 不受影响（下一轮还要用标记）。
	if msgs[0][aiMsgRoundKey] != 3 {
		t.Error("剥键不该修改原切片")
	}
	// 没有标记时直接穿透（不额外分配）。
	plain := []map[string]interface{}{{"role": "user", "content": "x"}}
	if got := stripInternalMessageKeys(plain); &got[0] != &plain[0] {
		t.Error("无标记时应原样返回同一底层数组")
	}
}

// 折叠绝不碰最后一轮；更早的轮次才会被折。
func TestCompactKeepsLastRound(t *testing.T) {
	big := strings.Repeat("y", aiToolFoldRune*3)
	// 与生产同序：逐轮 append → 打标。
	msgs := []map[string]interface{}{msg("system", nil), msg("user", nil)}
	tagMessagesRound(msgs, 0)

	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": big, "tool_call_id": "c1"})
	tagMessagesRound(msgs, 1)

	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": big, "tool_call_id": "c2"})
	tagMessagesRound(msgs, 2)
	// 再跑一轮：WP4b 之后「最近两轮」都不动，所以需要 3 轮才能看到折叠。
	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": big, "tool_call_id": "c3"})
	tagMessagesRound(msgs, 3)

	// 预算压到极小，强制触发折叠。
	cr := compactLoopMessages(msgs, 1)
	if cr.Folded+cr.Summarized == 0 {
		t.Fatal("超预算时应折叠早期轮次")
	}
	if len(cr.FoldedIDs) == 0 {
		t.Error("压缩事件必须带 folded_ids，否则「压缩后重查率」不可测")
	}
	// 第 1 轮的 tool 被折（内容变短），第 2 轮的必须原样。
	first := msgs[3]["content"].(string)
	last := msgs[5]["content"].(string)
	if len([]rune(first)) >= len([]rune(big)) {
		t.Error("第 1 轮的工具结果应被折叠")
	}
	if last != big {
		t.Error("最后一轮的工具结果被折叠了——模型正要用它")
	}
}

// 全部消息都在同一轮时，什么都不折（没有「更早的轮次」可折）。
func TestCompactSingleRoundNoop(t *testing.T) {
	big := strings.Repeat("z", aiToolFoldRune*3)
	msgs := []map[string]interface{}{
		msg("user", nil),
		assistantCall(),
		{"role": "tool", "content": big, "tool_call_id": "c1"},
	}
	tagMessagesRound(msgs, 0)
	if cr := compactLoopMessages(msgs, 1); cr.Folded+cr.Summarized != 0 {
		t.Errorf("只有一轮时不该折叠，实际折了 %d 条", cr.Folded+cr.Summarized)
	}
	if msgs[2]["content"].(string) != big {
		t.Error("单轮内容被改了")
	}
}

// 折叠后的文案要能指导模型下一步（而不是一句「已截断」）。
func TestCompactNoteMentionsRerun(t *testing.T) {
	big := strings.Repeat("w", aiToolFoldRune*3)
	msgs := []map[string]interface{}{msg("user", nil)}
	tagMessagesRound(msgs, 0)
	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": big, "tool_call_id": "c1"})
	tagMessagesRound(msgs, 1)
	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": "small2", "tool_call_id": "c2"})
	tagMessagesRound(msgs, 2)
	msgs = append(msgs, assistantCall(), map[string]interface{}{"role": "tool", "content": "small3", "tool_call_id": "c3"})
	tagMessagesRound(msgs, 3)

	compactLoopMessages(msgs, 1)
	got, _ := msgs[2]["content"].(string)
	if !strings.Contains(got, "折叠") || !strings.Contains(got, "重新调用") {
		t.Errorf("折叠文案缺少指引: %q", got)
	}
}

// 轮次数字不该出现在估算里（它只是元数据）——顺带防「标记把预算撑爆」。
func TestRoundMarkerDoesNotAffectEstimate(t *testing.T) {
	m := msg("tool", nil)
	before := estimateLoopTokens([]map[string]interface{}{m})
	m[aiMsgRoundKey] = 7
	after := estimateLoopTokens([]map[string]interface{}{m})
	if before != after {
		t.Errorf("标记影响了 token 估算: %d -> %d", before, after)
	}
	_ = fmt.Sprint(before)
}
