package ai

// WP1：主对话也落 agent_events 的那层胶水（multiSink）与 run 幂等创建的单测。
//
// 这里刻意不跑 runAgent 全链路：那需要 API Key 与网络，属于集成测试。
// 这两块是「一旦写错就静默丢事件 / 重复建 run」的地方，单独测性价比最高。

import (
	"sync"
	"testing"

	"quickdock/internal/db"
)

// recordSink 记录收到的事件与文本碎片，用来断言转发。
type recordSink struct {
	mu     sync.Mutex
	events []string
	pushes []string
	failed bool
	convID string
}

func (r *recordSink) Push(kind, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, kind+":"+text)
}

func (r *recordSink) Event(kind string, _ map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, kind)
}

func (r *recordSink) Failed() bool { return r.failed }

func (r *recordSink) SetConvID(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.convID = id
}

func (r *recordSink) snapshot() (events []string, pushes []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...), append([]string{}, r.pushes...)
}

// 每个下游都必须收到同一批事件，且顺序一致（顺序影响 run 树的 seq 语义）。
func TestMultiSinkForwardsAll(t *testing.T) {
	a, b := &recordSink{}, &recordSink{}
	m := &multiSink{sinks: []EventSink{a, b}}

	for _, kind := range []string{"conv", "tool", "tool_result", "done"} {
		m.Event(kind, map[string]any{"k": kind})
	}
	m.Push("token", "你好")

	wantEvents := []string{"conv", "tool", "tool_result", "done"}
	wantPushes := []string{"token:你好"}
	for name, s := range map[string]*recordSink{"a": a, "b": b} {
		events, pushes := s.snapshot()
		if len(events) != len(wantEvents) {
			t.Fatalf("%s 收到 %d 个事件，期望 %d: %v", name, len(events), len(wantEvents), events)
		}
		for i := range wantEvents {
			if events[i] != wantEvents[i] {
				t.Errorf("%s 第 %d 个事件 = %q，期望 %q", name, i, events[i], wantEvents[i])
			}
		}
		if len(pushes) != 1 || pushes[0] != wantPushes[0] {
			t.Errorf("%s 的 Push 转发不对: %v", name, pushes)
		}
	}
}

// 任一下游断开即视为断开：SSE 断了就没必要继续跑。
func TestMultiSinkFailedIsAny(t *testing.T) {
	healthy, broken := &recordSink{}, &recordSink{failed: true}
	if m := (&multiSink{sinks: []EventSink{healthy, broken}}); !m.Failed() {
		t.Error("任一下游断开时应报告 Failed")
	}
	if m := (&multiSink{sinks: []EventSink{healthy, healthy}}); m.Failed() {
		t.Error("全部健康时不应报告 Failed")
	}
}

// convID 必须透传到每个实现了 convIDSetter 的下游。
//
// 这条防的是一个静默故障：漏透传 → SSE 帧没有 convId → 前端把增量归到别的会话，
// 症状是「消息跑到另一个对话里」，而代码本身不报错。
func TestMultiSinkPropagatesConvID(t *testing.T) {
	a, b := &recordSink{}, &recordSink{}
	m := &multiSink{sinks: []EventSink{a, b}}
	m.SetConvID("conv-42")
	if a.convID != "conv-42" || b.convID != "conv-42" {
		t.Fatalf("convID 未透传: a=%q b=%q", a.convID, b.convID)
	}
}

// ensureRun 必须幂等：同一轮里反复调用只能建一条 run + 一个根节点。
func TestEnsureRunIdempotent(t *testing.T) {
	svc, _ := newTestAIService(t)
	sc := newAgentScope(svc, "conv-1", "整理项目文档", "", AIProfile{})

	run1, node1, err := sc.ensureRun()
	if err != nil {
		t.Fatalf("首次 ensureRun 失败: %v", err)
	}
	if run1 == "" || node1 == "" {
		t.Fatalf("run/node 不应为空: run=%q node=%q", run1, node1)
	}

	run2, node2, err := sc.ensureRun()
	if err != nil {
		t.Fatalf("第二次 ensureRun 失败: %v", err)
	}
	if run1 != run2 || node1 != node2 {
		t.Fatalf("ensureRun 不幂等: (%q,%q) != (%q,%q)", run1, node1, run2, node2)
	}
}

// 子 agent 的 scope 已经带父级 run/node，ensureRun 必须原样返回而不是另建一棵树。
//
// 如果这里退化成「新建」，run 树会裂成两棵：用户在界面上看到两个无关的根，
// 而子任务挂在其中一个下面。
func TestChildScopeReusesParentRun(t *testing.T) {
	svc, d := newTestAIService(t)

	run, err := d.CreateAgentRun(db.NewAgentRun{ConvID: "conv-2", Goal: "父目标", Trigger: "chat"})
	if err != nil {
		t.Fatalf("建父 run 失败: %v", err)
	}
	node, err := d.CreateAgentNode(db.NewAgentNode{RunID: run.ID, Role: "lead", Task: "父目标"})
	if err != nil {
		t.Fatalf("建父节点失败: %v", err)
	}

	sc := newChildAgentScope(svc, "conv-2", run.ID, node.ID, "", "", 1, AIProfile{})
	gotRun, gotNode, err := sc.ensureRun()
	if err != nil {
		t.Fatalf("子 scope ensureRun 失败: %v", err)
	}
	if gotRun != run.ID || gotNode != node.ID {
		t.Fatalf("子 scope 应复用父 run/node: got=(%q,%q) want=(%q,%q)", gotRun, gotNode, run.ID, node.ID)
	}
}
