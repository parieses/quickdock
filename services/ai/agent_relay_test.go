package ai

// 派活链路的两块「一改就静默坏掉」的东西：
//   - childRelaySink：子 agent 的事件要压成一条 child 帧，漏过滤会把子 agent 的
//     正文画进主对话；
//   - 只读角色的工具面：放宽了域之后，写类工具必须仍被等级挡住。

import (
	"context"
	"sync"
	"testing"

	mcpsrv "quickdock/internal/mcp"
)

type capturedFrame struct {
	kind string
	data map[string]any
}

type captureSink struct {
	mu     sync.Mutex
	frames []capturedFrame
	pushes int
}

func (c *captureSink) Push(string, string) { c.pushes++ }

func (c *captureSink) Event(kind string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, capturedFrame{kind: kind, data: data})
}

func (c *captureSink) Failed() bool { return false }

// 子 agent 的 token / reasoning 一律不许进主对话；工具与轮次要压成 child 帧，
// 且每帧都带父级那条 spawn_agent 的 callId（前端靠它把进度贴到卡片上）。
func TestChildRelaySinkCompresses(t *testing.T) {
	parent := &captureSink{}
	relay := &childRelaySink{parent: parent, callID: "call-1", nodeID: "node-1", role: "explore"}

	relay.Push("token", "子 agent 正文不该出现在主对话")
	relay.Push("reasoning", "思考也不该")
	relay.Event("reasoning", map[string]any{"text": "x"}) // 白名单外：丢弃
	relay.Event("conv", map[string]any{"id": "c"})        // 白名单外：丢弃
	relay.Event("tool", map[string]any{"id": "c2", "name": "file_grep", "round": 3})
	relay.Event("tool_result", map[string]any{"id": "c2", "ok": true, "ms": 1200})
	relay.Event("done", nil)

	if parent.pushes != 0 {
		t.Fatalf("子 agent 的文本碎片不该转发，实际转了 %d 次", parent.pushes)
	}
	if len(parent.frames) != 3 {
		t.Fatalf("只该转 tool / tool_result / done 三条，实际 %d 条: %+v", len(parent.frames), parent.frames)
	}
	for _, f := range parent.frames {
		if f.kind != "child" {
			t.Fatalf("事件类型必须是 child，实际 %q", f.kind)
		}
		if f.data["callId"] != "call-1" || f.data["nodeId"] != "node-1" || f.data["role"] != "explore" {
			t.Fatalf("每帧都要带归属信息，实际 %+v", f.data)
		}
	}
	if got := parent.frames[0].data; got["phase"] != "tool" || got["tool"] != "file_grep" || got["round"] != 3 {
		t.Errorf("tool 帧内容不对: %+v", got)
	}
	if got := parent.frames[1].data; got["phase"] != "tool_done" || got["ms"] != 1200 {
		t.Errorf("tool_done 帧内容不对: %+v", got)
	}
	if got := parent.frames[2].data; got["phase"] != "done" {
		t.Errorf("done 帧内容不对: %+v", got)
	}
}

// eventSinkFrom 必须是「有就取、没有就 nil」——单测与一次性问答都没有流，
// 取不到时任何一处 panic 都会把整条工具链带崩。
func TestEventSinkFromCtx(t *testing.T) {
	if eventSinkFrom(context.Background()) != nil {
		t.Error("没有挂 sink 时应返回 nil")
	}
	s := &captureSink{}
	if eventSinkFrom(withEventSink(context.Background(), s)) != s {
		t.Error("挂上的 sink 应能取回")
	}
	// withEventSink(nil) 不该把 ctx 变成一个「有值但为 nil」的陷阱。
	if eventSinkFrom(withEventSink(context.Background(), nil)) != nil {
		t.Error("nil sink 不该被挂上")
	}
}

// 只读角色放开 code 域之后，必须仍然拿不到写类工具。
//
// 这是本轮改动最危险的一处：git 工具在 code 域，而 file_write / run_command
// 也在 code 域。判据只能是等级，不能是域。
func TestReadOnlyRolesKeepWritesOut(t *testing.T) {
	tool := func(name, domain string, level int) mcpsrv.Tool {
		return mcpsrv.Tool{Name: name, Domain: domain, Level: level}
	}
	cases := []struct {
		role string
		tool mcpsrv.Tool
		want bool
	}{
		// 本次要放开的：只读的代码域工具。
		{"explore", tool("git_status", mcpsrv.CategoryCode, mcpsrv.LevelRead), true},
		{"explore", tool("git_diff", mcpsrv.CategoryCode, mcpsrv.LevelRead), true},
		{"explore", tool("dir_list", mcpsrv.CategoryCode, mcpsrv.LevelRead), true},
		{"review", tool("git_diff", mcpsrv.CategoryCode, mcpsrv.LevelRead), true},
		// 仍须挡住的：同域但会写盘 / 跑命令。
		{"explore", tool("file_write", mcpsrv.CategoryCode, mcpsrv.LevelWrite), false},
		{"explore", tool("file_edit", mcpsrv.CategoryCode, mcpsrv.LevelWrite), false},
		{"explore", tool("run_command", mcpsrv.CategoryCode, mcpsrv.LevelWrite), false},
		{"review", tool("run_command", mcpsrv.CategoryCode, mcpsrv.LevelWrite), false},
		// 看着只读、实际有副作用的两条：靠 Exclude 摘掉。
		{"explore", tool("plan_update", mcpsrv.CategoryCode, mcpsrv.LevelRead), false},
		{"explore", tool("project_memory", mcpsrv.CategoryCode, mcpsrv.LevelRead), false},
		{"review", tool("project_memory", mcpsrv.CategoryCode, mcpsrv.LevelRead), false},
		// code 角色不受影响。
		{"code", tool("file_write", mcpsrv.CategoryCode, mcpsrv.LevelWrite), true},
	}
	for _, c := range cases {
		f := agentRoleSpecs[c.role].filter()
		if got := f.keep(c.tool); got != c.want {
			t.Errorf("role=%s tool=%s 期望 %v，实际 %v", c.role, c.tool.Name, c.want, got)
		}
	}
}

// 子 agent 不能再派活：这条不靠等级（spawn 是 LevelWrite，只读角色本来就够不着），
// 而是靠 NoSpawn 显式撤下——code 角色是 LevelWrite，少了这条就能无限套娃。
func TestChildRoleHasNoSpawn(t *testing.T) {
	for _, role := range agentRoleOrder {
		f := agentRoleSpecs[role].filter()
		if f.keep(mcpsrv.Tool{Name: spawnAgentToolName, Domain: mcpsrv.CategoryCode, Level: mcpsrv.LevelWrite}) {
			t.Errorf("角色 %s 不该拿到 spawn_agent", role)
		}
	}
}
