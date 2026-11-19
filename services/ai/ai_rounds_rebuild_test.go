package ai

// P0 回归：列表被整体重建后，循环内折叠必须还活着。
//
// 场景：长会话撞一次 400 → buildAIMessagesWithin 从库里重建整个 messages
// （纯净 {role,content}，不带 _aiRound）→ 若按旧写法整表打标，所有消息轮次相同，
// compactLoopMessages 的保护条件对全表成立，本次运行剩余轮次里再也不折叠。

import (
	"strings"
	"testing"
)

// 重建后必须恢复「历史在第 0 轮、每轮工具调用各占一轮」的结构。
func TestRetagMessagesByStructure(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "a"}}},
		{"role": "tool", "tool_call_id": "a", "content": "r1"},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "b"}}},
		{"role": "tool", "tool_call_id": "b", "content": "r2"},
	}
	// 模拟「旧标记随重建丢失」，同时保证函数会清掉可能存在的不一致标记。
	msgs[0][aiMsgRoundKey] = 9
	retagMessagesByStructure(msgs)

	want := []int{0, 0, 1, 1, 2, 2}
	got := resolveMessageRounds(msgs)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条轮次 = %d，期望 %d", i, got[i], want[i])
		}
	}
	if lastRoundOf(got) != 2 {
		t.Errorf("最大轮次 = %d，期望 2", lastRoundOf(got))
	}
}

// 关键断言：重建后仍然能折叠较早轮次的工具结果（旧写法这里会是 0）。
func TestCompactionStillWorksAfterRebuild(t *testing.T) {
	big := strings.Repeat("x", aiToolFoldRune*3)
	msgs := []map[string]interface{}{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "a"}}},
		{"role": "tool", "tool_call_id": "a", "content": big},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "b"}}},
		{"role": "tool", "tool_call_id": "b", "content": big},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c"}}},
		{"role": "tool", "tool_call_id": "c", "content": big},
	}
	retagMessagesByStructure(msgs)

	cr := compactLoopMessages(msgs, 1) // 预算压到极小，强制折叠
	if cr.Folded+cr.Summarized == 0 {
		t.Fatal("重建后仍应能折叠较早轮次（否则 WP4b 在长会话里等于不存在）")
	}
	// 最近两轮（b/c）必须原样。
	if msgs[5]["content"].(string) != big || msgs[7]["content"].(string) != big {
		t.Error("最近两轮不该被折叠")
	}
}

// 只给尾巴打标：重建过的历史不能被后续 tagMessagesRound 改成本轮。
func TestTagTailDoesNotTouchHistory(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "q"},
	}
	tagMessagesRound(msgs, 0)
	tail := len(msgs)
	msgs = append(msgs, map[string]interface{}{"role": "assistant", "content": "", "tool_calls": []any{}})
	tagMessagesRound(msgs[tail:], 3)

	rounds := resolveMessageRounds(msgs)
	if rounds[0] != 0 || rounds[1] != 0 {
		t.Errorf("历史被改写了: %v", rounds)
	}
	if rounds[2] != 3 {
		t.Errorf("尾巴没打上本轮标记: %d", rounds[2])
	}
}
