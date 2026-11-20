package ai

// 方案 C 的回归：wire 落库 → 重建时接回去，让下次提问的前缀与上一发对齐。
//
// 背景（2026-11-20 看板）：1198 请求 / 108 会话，缓存命中 76%，而同类 harness
// 99%。根因不是压缩时机，而是 buildAIMessages 重建历史时只取 role+content——
// DB 里明明存了 tool_calls，工具轮却全部消失，于是每次提问的第一发从第一个
// 工具轮起就和上一次不同，整段前缀作废。

import (
	"encoding/json"
	"testing"

	"quickdock/internal/db"
)

// 往返保真：tool_calls 的数组/嵌套 map 不能在这一趟里变形。
func TestDecodeMessageWireRoundTrip(t *testing.T) {
	tail := []map[string]interface{}{
		{
			"role":    "assistant",
			"content": "我先看看",
			"tool_calls": []map[string]any{{
				"id":   "c1",
				"type": "function",
				"function": map[string]any{
					"name":      "file_read",
					"arguments": `{"path":"a.go"}`,
				},
			}},
		},
		{"role": "tool", "tool_call_id": "c1", "content": "package a"},
	}
	raw, err := json.Marshal(tail)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	back := decodeMessageWire(string(raw))
	if len(back) != len(tail) {
		t.Fatalf("往返后条数 = %d，期望 %d", len(back), len(tail))
	}
	// 判据是**发出去的字节**，不是 Go 的静态类型：JSON 反序列化后 tool_calls
	// 会变成 []interface{}，但只要再次 marshal 的字节与原样相同，上游看到的
	// 前缀就是一个东西（map 的键序由 encoding/json 统一按字典序输出）。
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("再次序列化失败: %v", err)
	}
	if string(again) != string(raw) {
		t.Errorf("往返后字节不一致（前缀缓存会失效）:\n原始 %s\n往返 %s", raw, again)
	}
}

// 坏数据只能退化成「没有 wire」，不能连累整条对话。
func TestDecodeMessageWireBroken(t *testing.T) {
	if got := decodeMessageWire(""); got != nil {
		t.Errorf("空串应返回 nil，实际 %v", got)
	}
	if got := decodeMessageWire("{半截"); got != nil {
		t.Errorf("非法 JSON 应返回 nil，实际 %v", got)
	}
}

// 每一条带 wire 的助手回复都要还原（不是只有最近一条）：
// 只还原最近一条时，第二次追问起前缀就从更早的助手消息开始分叉，整段历史
// 按全价重算——这是看板命中率 76% 的主因。顺序必须是 wire 在前、正文在后。
func TestAppendHistoryWithWiresReplaysAll(t *testing.T) {
	hist := []db.AIMessage{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1", Wire: `[{"role":"assistant","content":"t1"},{"role":"tool","tool_call_id":"c1","content":"r1"}]`},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2", Wire: `[{"role":"assistant","content":"t2"},{"role":"tool","tool_call_id":"c2","content":"r2"}]`},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	out := appendHistoryWithWires([]map[string]interface{}{}, hist, func(m db.AIMessage) interface{} { return m.Content })
	var got []string
	for _, m := range out {
		role, _ := m["role"].(string)
		c, _ := m["content"].(string)
		got = append(got, role+"/"+c)
	}
	want := []string{
		"user/q1",
		"assistant/t1", "tool/r1", "assistant/a1",
		"user/q2",
		"assistant/t2", "tool/r2", "assistant/a2",
		"user/q3",
		"assistant/a3",
	}
	if len(got) != len(want) {
		t.Fatalf("重放条数 = %d，期望 %d\n实际: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 = %q，期望 %q", i, got[i], want[i])
		}
	}
}
