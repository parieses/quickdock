package mcp

// todo_update 的增量语义回归：只改 AI 显式传的字段，其余保持原值。
// 这是「AI 只想改优先级却把备注/标签清空」这类事故的防线——
// 一旦有人把 updateArg 换回 mcpsrv.Arg 的零值兜底，这里立刻红。

import "testing"

func TestUpdateArgIncremental(t *testing.T) {
	cur := map[string]string{
		"title":    "原标题",
		"note":     "原备注不能丢",
		"tags":     `["工作"]`,
		"priority": "low",
	}
	args := map[string]any{
		"id":       "todo-1",
		"priority": "high", // 只改优先级
	}

	got := map[string]string{}
	for k, v := range cur {
		got[k] = updateArg(args, k, v)
	}

	for k, want := range cur {
		if k == "priority" {
			want = "high"
		}
		if got[k] != want {
			t.Errorf("%s = %q，期望 %q（只改了 priority，其余字段必须原样保留）", k, got[k], want)
		}
	}
}

func TestUpdateArgExplicitEmptyClears(t *testing.T) {
	// 显式传空串表示清空（如去掉截止日期），不能和「没传」混为一谈。
	args := map[string]any{"note": ""}
	if got := updateArg(args, "note", "旧备注"); got != "" {
		t.Errorf("显式传空串应清空，实际 %q", got)
	}
	if got := updateArg(args, "tags", `["旧"]`); got != `["旧"]` {
		t.Errorf("没传的字段应保留原值，实际 %q", got)
	}
}
