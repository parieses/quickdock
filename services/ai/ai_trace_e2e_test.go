package ai

// 轨迹端到端：run 建得起来、事件写得进去、读得回来。
//
// 用户反馈「新开的对话轨迹也没数据」。这条链路有三段可能断：
//   1. ensureRun 没建 run（开关、DB、字段约束）；
//   2. dbSink 没写事件（runID 为空、序列化失败）；
//   3. 读的时候按错 id 查。
// 三段分开验，任何一段断了都能立刻定位，而不是靠「抽屉是空的」猜。

import (
	"testing"

	"quickdock/internal/db"
)

func TestTraceEndToEnd(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}

	// 1. run
	sc := &agentScope{svc: svc, convID: conv.ID, goal: "看看项目结构", cwd: t.TempDir()}
	runID, nodeID, err := sc.ensureRun()
	if err != nil {
		t.Fatalf("ensureRun 失败: %v", err)
	}
	if runID == "" || nodeID == "" {
		t.Fatalf("run/root node 没建出来: run=%q node=%q", runID, nodeID)
	}

	// 2. 事件：模拟循环里真实发的两类
	sink := &dbSink{db: d, runID: runID, nodeID: nodeID}
	sink.Event("tool", map[string]any{"id": "c1", "name": "file_read", "args": `{"path":"a.go"}`})
	sink.Event("tool_result", map[string]any{"id": "c1", "name": "file_read", "ok": true, "ms": 12})

	events, err := d.ListAgentEvents(runID, 0, 50)
	if err != nil {
		t.Fatalf("读事件失败: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("事件数不对: %d（说明 dbSink 没写进去）", len(events))
	}
	if events[0].Kind != "tool" || events[1].Kind != "tool_result" {
		t.Errorf("事件顺序/类型不对: %s %s", events[0].Kind, events[1].Kind)
	}

	// 3. 绑定能不能读回同一棵树
	res := svc.AIAgentRunTree(runID, 0)
	if res.Code != 0 {
		t.Fatalf("读 run 树失败: %+v", res)
	}
	tree, ok := res.Data.(*AgentRunTree)
	if !ok || tree == nil {
		t.Fatalf("返回类型不对: %T", res.Data)
	}
	if tree.Run == nil || tree.Run.ID != runID {
		t.Errorf("run 头不对: %+v", tree.Run)
	}
	if len(tree.Nodes) != 1 {
		t.Errorf("根节点数不对: %d", len(tree.Nodes))
	}
	if len(tree.Events) != 2 {
		t.Errorf("树里事件数不对: %d", len(tree.Events))
	}

	// AILatestRun 必须能按会话找到它（前端刷新后靠这个补入口）
	latest := svc.AILatestRun(conv.ID)
	head, ok := latest.Data.(*db.AgentRun)
	if !ok || head == nil || head.ID != runID {
		t.Errorf("AILatestRun 没找到刚建的 run: %#v", latest.Data)
	}
}

// 没有任何工具调用的对话（纯聊天）也必须留下一条可回放的 run——
// 否则「新开的对话轨迹没数据」就是必然的：轨迹只记工具，不记这一轮本身。
func TestTraceHasEventsWithoutAnyTool(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat"})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	sc := &agentScope{svc: svc, convID: conv.ID, goal: "你好", cwd: t.TempDir()}
	runID, nodeID, err := sc.ensureRun()
	if err != nil || runID == "" {
		t.Fatalf("ensureRun 失败: %v", err)
	}
	sink := &dbSink{db: d, runID: runID, nodeID: nodeID}
	// done 是每轮必有的事件：没有它，纯聊天的 run 在抽屉里就是「0 步」。
	sink.Event("done", withRunID(map[string]any{"convId": conv.ID, "toolRounds": 0, "toolCalls": 0}, runID))

	events, _ := d.ListAgentEvents(runID, 0, 50)
	if len(events) == 0 {
		t.Fatal("纯聊天对话也必须至少有一条可展示的事件")
	}
	if events[0].Kind != "done" {
		t.Errorf("期望 done 事件，实际 %s", events[0].Kind)
	}
}
