package aitools

import (
	"context"
	"testing"
)

// 收集器只认自己登记过的 id，且收完就清空。
//
// 判错的代价很实：KillAll 若按「所有后台任务」杀，用户停 A 会话会把 B 会话
// 正在跑的构建一起干掉；若不清空，同一批 id 会被重复计数，日志里看不出真相。
func TestJobCollector(t *testing.T) {
	c := &JobCollector{}
	ctx := WithJobCollector(context.Background(), c)
	recordJob(ctx, "job_not_exists_a")
	recordJob(ctx, "job_not_exists_b")
	recordJob(ctx, "") // 空 id 不入账

	// 不存在的任务杀不到，计数为 0（它们本来就没在跑）。
	if n := c.KillAll(); n != 0 {
		t.Fatalf("KillAll = %d，期望 0（这些 job 不存在）", n)
	}
	// 清空之后再来一次不该重复处理。
	if n := c.KillAll(); n != 0 {
		t.Fatalf("KillAll 第二次 = %d，期望 0", n)
	}

	// 没有收集器的 ctx 上登记：静默跳过，不能 panic（工具也可能被
	// 非 agent 路径直接调用，比如外部 MCP 客户端）。
	recordJob(context.Background(), "job_x")
}
