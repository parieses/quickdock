package ai

// 两条「别为了省上下文把缓存整段扔掉」的策略：
//
//  1. 循环内折叠：越线后一次折到 60% 预算，而不是压到刚好不超——后者下一轮
//     又超、又折，每折一次都改写历史，上游前缀缓存每轮作废（实测折叠轮命中
//     66.7%，未折叠轮 80.9%）。
//  2. 收尾闸门：收尾轮不再把 tools 撤下，只拒绝执行。撤下 tools 会同时改变
//     请求的 tools 数组与 system 文案，缓存整段作废，而收尾轮往往 prompt 最大。

import (
	"fmt"
	"strings"
	"testing"
)

// 越线后一次折到目标水位，并且同一批消息不会再折第二次。
func TestCompactFoldsToTargetOnce(t *testing.T) {
	big := strings.Repeat("x", aiToolFoldRune*4)
	msgs := []map[string]interface{}{msg("system", nil), msg("user", nil)}
	tagMessagesRound(msgs, 0)
	// 轮数要够多：最近两轮永远不折，它们是「折到底也降不下去」的下限。
	for i := 1; i <= 12; i++ {
		msgs = append(msgs, assistantCall(),
			map[string]interface{}{"role": "tool", "content": big, "tool_call_id": fmt.Sprintf("c%d", i)})
		tagMessagesRound(msgs, i)
	}

	before := estimateLoopTokens(msgs)
	budget := before * 4 / 5 // 越线 25%
	cr := compactLoopMessages(msgs, budget)
	if cr.Folded+cr.Summarized == 0 {
		t.Fatal("超过预算却没折叠")
	}
	after := estimateLoopTokens(msgs)
	if after*100 > budget*compactFoldTargetPercent {
		t.Errorf("应一次折到预算的 %d%%（%d），实际 %d",
			compactFoldTargetPercent, budget*compactFoldTargetPercent/100, after)
	}
	// 最近两轮不动：模型正要用它们。
	if got := msgs[len(msgs)-1]["content"].(string); got != big {
		t.Error("最后一轮的工具结果被折叠了")
	}
	// 已经低于水位：再调用一次不应再改（否则又是一次缓存作废）。
	cr2 := compactLoopMessages(msgs, budget)
	if cr2.Folded+cr2.Summarized != 0 {
		t.Errorf("折到位后不该再折，实际又折了 %d 条", cr2.Folded+cr2.Summarized)
	}
}

// 收尾闸门产出的消息必须满足工具调用协议：assistant.tool_calls + 每个 call 一条 tool。
func TestClosedToolMessagesKeepProtocol(t *testing.T) {
	calls := []aiToolCall{
		{ID: "a1", Name: "file_read", Args: `{"path":"x.go"}`},
		{ID: "a2", Name: "run_command", Args: `{"command":"ls"}`},
	}
	out := closedToolMessages(calls, "我先看看")
	if len(out) != len(calls)+1 {
		t.Fatalf("应有 1 条 assistant + %d 条 tool，实际 %d", len(calls), len(out))
	}
	if out[0]["role"] != "assistant" {
		t.Errorf("第一条应是 assistant: %v", out[0])
	}
	wire, _ := out[0]["tool_calls"].([]map[string]any)
	if len(wire) != len(calls) {
		t.Fatalf("assistant 应带回 %d 个 tool_calls，实际 %d", len(calls), len(wire))
	}
	for i, c := range calls {
		m := out[i+1]
		if m["role"] != "tool" {
			t.Errorf("第 %d 条应是 tool: %v", i+1, m)
		}
		if m["tool_call_id"] != c.ID {
			t.Errorf("第 %d 条 tool_call_id=%v，期望 %s", i+1, m["tool_call_id"], c.ID)
		}
		s, _ := m["content"].(string)
		if !strings.Contains(s, "不会被执行") {
			t.Errorf("回执应说明调用未执行: %q", s)
		}
	}
	// 正文为空也要能组装（协议里 content 用空串，不能用 null）。
	if out := closedToolMessages(calls[:1], ""); len(out) != 2 {
		t.Errorf("空正文时应仍返回 assistant + tool: %v", out)
	}
}
