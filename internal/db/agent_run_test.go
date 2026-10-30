package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAgentRunNodeEventRoundTrip 三张编排表的往返：
//
//   - seq 由库分配且同一 run 内单调递增（前端增量拉取全靠它）；
//   - 节点结束后才有 ended_at；
//   - 删 run 时节点与事件靠外键级联清掉（不级联就会留一堆孤儿行，
//     而 SQLite 默认不启用外键，只能靠 DSN 里那条 pragma——这里顺带验证它生效）。
func TestAgentRunNodeEventRoundTrip(t *testing.T) {
	d := openTestDB(t)

	conv, err := d.CreateAIConversation(NewAIConversation{Title: "编排测试"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	run, err := d.CreateAgentRun(NewAgentRun{
		ConvID: conv.ID, Goal: "重构一下登录模块", Trigger: "chat", BudgetTokens: 50000,
	})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}
	if run.Status != AgentStatusRunning || run.BudgetTokens != 50000 || run.UsedTokens != 0 {
		t.Fatalf("run 初始状态不对: %+v", run)
	}

	root, err := d.CreateAgentNode(NewAgentNode{RunID: run.ID, Role: "lead", Task: "拆解任务"})
	if err != nil {
		t.Fatalf("建根节点失败: %v", err)
	}
	child, err := d.CreateAgentNode(NewAgentNode{RunID: run.ID, ParentID: root.ID, Role: "code", Task: "改 auth.go"})
	if err != nil {
		t.Fatalf("建子节点失败: %v", err)
	}
	if root.Seq != 1 || child.Seq != 2 {
		t.Fatalf("seq 应由库分配且递增，得到 root=%d child=%d", root.Seq, child.Seq)
	}

	// 事件 seq 单调递增，增量拉取只返回 afterSeq 之后的部分。
	var seqs []int
	for _, k := range []string{"tool", "tool_result", "notice"} {
		s, aerr := d.AppendAgentEvent(run.ID, child.ID, k, `{"ok":true}`)
		if aerr != nil {
			t.Fatalf("追加事件失败: %v", aerr)
		}
		seqs = append(seqs, s)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("事件 seq 未递增: %v", seqs)
		}
	}
	tail, err := d.ListAgentEvents(run.ID, seqs[0], 100)
	if err != nil {
		t.Fatalf("拉增量事件失败: %v", err)
	}
	if len(tail) != 2 || tail[0].Kind != "tool_result" {
		t.Fatalf("增量拉取结果不对: %+v", tail)
	}

	// 节点结束后才盖 ended_at。
	if err := d.UpdateAgentNodeResult(child.ID, AgentStatusRunning, "", TokenUsage{Prompt: 10, Completion: 5}); err != nil {
		t.Fatalf("更新节点失败: %v", err)
	}
	running, _ := d.GetAgentNode(child.ID)
	if running.EndedAt != "" {
		t.Fatalf("仍在运行的节点不该有结束时间: %+v", running)
	}
	if err := d.UpdateAgentNodeResult(child.ID, AgentStatusDone, "改完了", TokenUsage{Prompt: 1200, Completion: 300}); err != nil {
		t.Fatalf("收尾节点失败: %v", err)
	}
	done, _ := d.GetAgentNode(child.ID)
	if done.EndedAt == "" || done.PromptTokens != 1200 || done.Result != "改完了" {
		t.Fatalf("节点收尾字段不对: %+v", done)
	}

	// 成本归集按 run 维度（只算带 run_id 的调用）。
	if err := d.RecordAIUsageScoped(conv.ID, run.ID, child.ID, "deepseek-chat", "chat", TokenUsage{Prompt: 1000, Completion: 200, Cached: 800}); err != nil {
		t.Fatalf("记录编排用量失败: %v", err)
	}
	_ = d.RecordAIUsage(conv.ID, "deepseek-chat", "title", TokenUsage{Prompt: 9000, Completion: 20}) // 编排外，不该计入
	u, err := d.SumAIUsageByRun(run.ID)
	if err != nil {
		t.Fatalf("汇总 run 用量失败: %v", err)
	}
	if u.Prompt != 1000 || u.Completion != 200 || u.Cached != 800 {
		t.Fatalf("run 用量汇总串进了编排外的调用: %+v", u)
	}

	// 删会话 → run 级联 → 节点/事件级联（外键必须真的生效）。
	if err := d.DeleteAIConversation(conv.ID); err != nil {
		t.Fatalf("删会话失败: %v", err)
	}
	if got, _ := d.GetAgentRun(run.ID); got != nil {
		t.Fatalf("会话删除后 run 应随之消失: %+v", got)
	}
	if nodes, _ := d.ListAgentNodes(run.ID); len(nodes) != 0 {
		t.Fatalf("节点应随 run 级联删除，剩 %d 条", len(nodes))
	}
	if evs, _ := d.ListAgentEvents(run.ID, 0, 100); len(evs) != 0 {
		t.Fatalf("事件应随 run 级联删除，剩 %d 条", len(evs))
	}
}

// TestAgentEventPayloadTruncated 超大载荷必须被截成合法 JSON。
//
// 直接按字节切会切在多字节字符或转义序列中间，前端 JSON.parse 整帧失败，
// 表现出来是「事件全没了」而不是「内容被截断」——这种 bug 极难从现象反推。
func TestAgentEventPayloadTruncated(t *testing.T) {
	d := openTestDB(t)
	run, err := d.CreateAgentRun(NewAgentRun{Goal: "载荷截断"})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}

	big := `{"text":"` + strings.Repeat("日志行", 20000) + `"}`
	if _, err := d.AppendAgentEvent(run.ID, "", "tool_result", big); err != nil {
		t.Fatalf("追加超大事件失败: %v", err)
	}
	evs, err := d.ListAgentEvents(run.ID, 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("读事件失败: %v %d", err, len(evs))
	}
	if len(evs[0].Payload) >= len(big) {
		t.Fatalf("载荷没被截断：%d 字节", len(evs[0].Payload))
	}
	if !json.Valid([]byte(evs[0].Payload)) {
		t.Fatalf("截断后的载荷不是合法 JSON: %s", evs[0].Payload[:80])
	}
}
