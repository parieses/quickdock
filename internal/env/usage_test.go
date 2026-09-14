package env

import (
	"testing"

	"quickdock/internal/sysutil"
)

func TestSubtreeOf(t *testing.T) {
	snap := []sysutil.ProcStat{
		{PID: 1, ParentPID: 0, MemBytes: 10},
		{PID: 2, ParentPID: 1, MemBytes: 20},
		{PID: 3, ParentPID: 2, MemBytes: 30},
		{PID: 4, ParentPID: 1, MemBytes: 40},
		{PID: 5, ParentPID: 99, MemBytes: 50},
	}
	got := subtreeOf(snap, 1)
	sum := int64(0)
	ids := map[int]bool{}
	for _, p := range got {
		ids[p.PID] = true
		sum += p.MemBytes
	}
	if len(got) != 4 || sum != 100 {
		t.Fatalf("subtreeOf(1) 结果错误: n=%d sum=%d ids=%v", len(got), sum, ids)
	}
	if ids[5] {
		t.Fatalf("不属于树的 PID 5 被错误纳入")
	}
	// 叶子节点
	if g := subtreeOf(snap, 3); len(g) != 1 || g[0].PID != 3 {
		t.Fatalf("subtreeOf(3) 应为单节点, got=%v", g)
	}
	// 不存在的 PID
	if g := subtreeOf(snap, 777); len(g) != 0 {
		t.Fatalf("subtreeOf(777) 应为空, got=%v", g)
	}
	// 自环不应死循环
	loop := []sysutil.ProcStat{{PID: 8, ParentPID: 8}}
	if g := subtreeOf(loop, 8); len(g) != 1 {
		t.Fatalf("自环应只返回自身, got=%v", g)
	}
}
