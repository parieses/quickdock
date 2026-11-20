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

// 只认最后一条带 wire 的助手消息：更早的还原了也用不上。
func TestLastWireMessageIndex(t *testing.T) {
	hist := []db.AIMessage{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2", Wire: "[]"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	if got := lastWireMessageIndex(hist); got != 3 {
		t.Errorf("应命中下标 3，实际 %d", got)
	}
	// user 消息上挂着 wire 也不算：只有 assistant 的 wire 代表一次工具对话。
	hist[3].Role = "user"
	hist[3].Wire = ""
	if got := lastWireMessageIndex(hist); got != -1 {
		t.Errorf("没有 assistant wire 时应返回 -1，实际 %d", got)
	}
}
