package db

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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

// PurgeAgentRunsBefore：按天清理只删过老的 run，节点与事件靠级联一起走。
//
// 为什么值得一条测试：清理是唯一的「删数据」路径，写错一步的后果不是报错，
// 而是「上周那次失败的执行树悄悄不见了」——没人会立刻发现。
// 同时钉住三件事：只删过老的、级联真的生效、新 run 不受牵连。
func TestPurgeAgentRunsBefore(t *testing.T) {
	d := openTestDB(t)

	conv, err := d.CreateAIConversation(NewAIConversation{Title: "清理测试"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	oldRun, err := d.CreateAgentRun(NewAgentRun{ConvID: conv.ID, Goal: "很久以前的任务"})
	if err != nil {
		t.Fatalf("建老 run 失败: %v", err)
	}
	newRun, err := d.CreateAgentRun(NewAgentRun{ConvID: conv.ID, Goal: "刚刚的任务"})
	if err != nil {
		t.Fatalf("建新 run 失败: %v", err)
	}

	node, err := d.CreateAgentNode(NewAgentNode{RunID: oldRun.ID, Role: "lead", Task: "老会话的步骤"})
	if err != nil {
		t.Fatalf("建节点失败: %v", err)
	}
	if _, err := d.AppendAgentEvent(oldRun.ID, node.ID, "tool", `{"name":"file_read"}`); err != nil {
		t.Fatalf("追加事件失败: %v", err)
	}
	if _, err := d.AppendAgentEvent(newRun.ID, "", "tool", `{"name":"file_read"}`); err != nil {
		t.Fatalf("追加新 run 事件失败: %v", err)
	}

	// 把老 run 推到保留期之外：CreateAgentRun 用的是 now()，只能直接改。
	backdated := time.Now().AddDate(0, 0, -(AgentRunRetentionDays + 10)).Format(time.RFC3339)
	if err := d.ExecuteParams(`UPDATE agent_runs SET created_at = ? WHERE id = ?`,
		[]interface{}{backdated, oldRun.ID}); err != nil {
		t.Fatalf("回填创建时间失败: %v", err)
	}

	n, err := d.PurgeAgentRunsBefore(time.Now().AddDate(0, 0, -AgentRunRetentionDays))
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只删掉 1 条过期 run，实际 %d", n)
	}

	// 老 run 本体、节点、事件全部消失。
	if got, gerr := d.GetAgentRun(oldRun.ID); gerr != nil {
		t.Fatalf("查老 run 失败: %v", gerr)
	} else if got != nil {
		t.Errorf("过期的 run 没被删掉: %+v", got)
	}
	if nodes, _ := d.ListAgentNodes(oldRun.ID); len(nodes) != 0 {
		t.Errorf("节点应随 run 级联删除，仍有 %d 个", len(nodes))
	}
	if evs, _ := d.ListAgentEvents(oldRun.ID, 0, 100); len(evs) != 0 {
		t.Errorf("事件应随 run 级联删除，仍有 %d 条", len(evs))
	}

	// 新 run 必须完好：清理不能把「刚跑完的那次」也带走。
	if got, gerr := d.GetAgentRun(newRun.ID); gerr != nil || got == nil {
		t.Errorf("误删了未过期的 run: %v %+v", gerr, got)
	}
	if evs, _ := d.ListAgentEvents(newRun.ID, 0, 100); len(evs) != 1 {
		t.Errorf("新 run 的事件被误删，剩 %d 条", len(evs))
	}

	// 保留期本身也要守住：调小它等于悄悄缩短可回放的历史。
	if AgentRunRetentionDays < 7 {
		t.Errorf("AgentRunRetentionDays = %d，短于回滚点的 7 天，轨迹会比快照先消失", AgentRunRetentionDays)
	}
}

// TestAddAgentRunUsage 锁住 run 用量累加的行为。
//
// 顶层 run 的 token 从主循环写回（子 agent 走 handleSpawnAgent），
// 有进展的 run 若这一列恒为 0，「整段会话」回放头行就会显示假的 0 tok。
// 这里验证累加语义：多次相加、以及 0/负数被忽略（别把用量冲掉）。
func TestAddAgentRunUsage(t *testing.T) {
	d := openTestDB(t)
	conv, err := d.CreateAIConversation(NewAIConversation{Title: "用量测试"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	run, err := d.CreateAgentRun(NewAgentRun{ConvID: conv.ID, Goal: "g", Trigger: "chat"})
	if err != nil {
		t.Fatalf("建 run 失败: %v", err)
	}

	for _, n := range []int{1200, 0, -5, 380} {
		if err := d.AddAgentRunUsage(run.ID, n); err != nil {
			t.Fatalf("累加 %d 失败: %v", n, err)
		}
	}
	got, err := d.GetAgentRun(run.ID)
	if err != nil {
		t.Fatalf("读 run 失败: %v", err)
	}
	if got.UsedTokens != 1580 {
		t.Errorf("累计用量应为 1200+380=1580（0/负数忽略），实际 %d", got.UsedTokens)
	}
}
