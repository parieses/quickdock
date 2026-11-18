package ai

// WP16：读类工具并行白名单的守卫测试。
//
// 这类问题的失效方式特别安静：多加一个写类工具进白名单，代码照样编译、测试照样
// 通过，上线之后表现为「偶发的文件内容错乱」——没人会想到是并行调度的问题。
//
// 这里刻意**不**去查注册表里的 Level：services/ai 的测试不能 import
// services/mcp（它会经 services/clipboard 反向依赖本包，形成 import cycle），
// 而且 LevelRead 本来就不等于只读（project_memory / plan_update 都是 LevelRead）。
// 所以守的是更实在的东西：
//  1. 已知的写类工具绝不在白名单里；
//  2. 「看起来只读、实际会写」的工具有显式排除表，且排除表在调度处生效；
//  3. 调度判据在混轮 / 重样 / 坏参数 / 排除项时退回串行。

import (
	"fmt"
	"testing"
)

// 白名单里绝不能出现的名字：会写盘、会跑命令、会改会话状态。
//
// 这份清单与 parallelUnsafeReadTools 分工不同：后者是「像只读、其实会写」，
// 这里是「一眼就知道会写」，加进去基本只能是手滑。
func TestParallelSafeToolsExcludeWriters(t *testing.T) {
	writers := []string{
		"run_command",    // 任意副作用
		"file_edit",      // 写盘
		"file_write",     // 写盘
		"plan_update",    // 整份覆盖会话清单
		"project_memory", // action=write 会写 MEMORY.md
	}
	for _, name := range writers {
		if parallelSafeTools[name] {
			t.Errorf("%q 会改状态，绝不能进并发白名单", name)
		}
	}
}

// 排除表与白名单不能有交集，且每条排除都必须写明原因。
func TestParallelUnsafeReadToolsNotWhitelisted(t *testing.T) {
	for name, why := range parallelUnsafeReadTools {
		if parallelSafeTools[name] {
			t.Errorf("%q 同时在安全白名单和排除表里", name)
		}
		if why == "" {
			t.Errorf("%q 没有写排除原因：下一个人只会看到它「没被加」，然后顺手加上", name)
		}
	}
}

// 排除表必须在调度处生效——否则它只是一段注释。
//
// 这条是 project_memory 的活证据：它是 LevelRead，action=write 却会
// MkdirAll + WriteFile 写 MEMORY.md。即使有人把它加进白名单，
// canRunParallel 也必须把整轮退回串行。
func TestUnsafeReadToolForcesSerialEvenIfWhitelisted(t *testing.T) {
	for name := range parallelUnsafeReadTools {
		// 临时把它加进白名单，验证排除表的优先级高于白名单。
		parallelSafeTools[name] = true
		ok := canRunParallel([]aiToolCall{
			{ID: "c0", Name: name, Args: `{"action":"write"}`},
			{ID: "c1", Name: "file_read", Args: `{"path":"a.go"}`},
		})
		delete(parallelSafeTools, name)
		if ok {
			t.Errorf("%q 在排除表里，即使被加进白名单也必须串行", name)
		}
	}
}

// 调度判据。
func TestCanRunParallel(t *testing.T) {
	mk := func(pairs ...string) []aiToolCall {
		out := make([]aiToolCall, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			out = append(out, aiToolCall{
				ID:   fmt.Sprintf("c%d", i/2),
				Name: pairs[i],
				Args: pairs[i+1],
			})
		}
		return out
	}

	cases := []struct {
		name  string
		calls []aiToolCall
		want  bool
	}{
		{
			name:  "两个不同的读类工具并发",
			calls: mk("file_read", `{"path":"a.go"}`, "git_status", `{"path":"."}`),
			want:  true,
		},
		{
			name:  "读类 + 网络并发",
			calls: mk("file_grep", `{"path":".","pattern":"x"}`, "web_fetch", `{"url":"https://e.com"}`),
			want:  true,
		},
		{
			name:  "混入写类工具则串行",
			calls: mk("file_read", `{"path":"a.go"}`, "file_edit", `{"path":"a.go"}`),
			want:  false,
		},
		{
			name:  "混入 run_command 则串行",
			calls: mk("file_read", `{"path":"a.go"}`, "run_command", `{"command":"ls"}`),
			want:  false,
		},
		{
			name:  "混入 plan_update 则串行（它会整份覆盖清单）",
			calls: mk("file_read", `{"path":"a.go"}`, "plan_update", `{"items":[]}`),
			want:  false,
		},
		{
			name:  "混入 project_memory 则串行（action=write 会写盘）",
			calls: mk("file_read", `{"path":"a.go"}`, "project_memory", `{"action":"write"}`),
			want:  false,
		},
		{
			name:  "单个调用不并发（开 goroutine 不划算）",
			calls: mk("file_read", `{"path":"a.go"}`),
			want:  false,
		},
		{
			name:  "完全相同的两次调用必须串行，否则去重会双跑",
			calls: mk("file_read", `{"path":"a.go"}`, "file_read", `{"path":"a.go"}`),
			want:  false,
		},
		{
			name:  "参数写坏则串行（坏参数终会在执行时报错，不差这一次）",
			calls: mk("file_read", `{"path":"a.go"}`, "git_status", `{not json`),
			want:  false,
		},
	}

	for _, c := range cases {
		if got := canRunParallel(c.calls); got != c.want {
			t.Errorf("%s: canRunParallel = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// 混轮不再整轮串行：三条派活仍是一段并发，后面的命令各成一段、顺序不变。
// 这条是实机回归：混进 run_command 曾让三个子 agent 一个个跑，比单干还慢。
func TestParallelGroupsSplitMixedRound(t *testing.T) {
	calls := []aiToolCall{
		{ID: "s0", Name: spawnAgentToolName, Args: `{"role":"explore","task":"a"}`},
		{ID: "s1", Name: spawnAgentToolName, Args: `{"role":"explore","task":"b"}`},
		{ID: "s2", Name: spawnAgentToolName, Args: `{"role":"explore","task":"c"}`},
		{ID: "r0", Name: "run_command", Args: `{"command":"gofmt -l ."}`},
		{ID: "r1", Name: "run_command", Args: `{"command":"go test ./..."}`},
	}
	got := parallelGroups(calls)
	want := [][]int{{0, 1, 2}, {3}, {4}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("分组应为 %v，实际 %v", want, got)
	}
}

// 有依赖的混轮必须保序：写之后的派活不能在写之前跑。
func TestParallelGroupsKeepDependencyOrder(t *testing.T) {
	calls := []aiToolCall{
		{ID: "e", Name: "file_edit", Args: `{"path":"a.go"}`},
		{ID: "s0", Name: spawnAgentToolName, Args: `{"role":"explore","task":"a"}`},
		{ID: "s1", Name: spawnAgentToolName, Args: `{"role":"explore","task":"b"}`},
	}
	got := parallelGroups(calls)
	want := [][]int{{0}, {1, 2}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("分组应为 %v，实际 %v", want, got)
	}
}

// 同段重复调用要拆开：去重是在 execOneToolCall 里按全轮 seen 做的，
// 两段并发同时跑同一参数会双双通过检查，重复派活。
func TestParallelGroupsSplitDuplicates(t *testing.T) {
	calls := []aiToolCall{
		{ID: "s0", Name: spawnAgentToolName, Args: `{"role":"explore","task":"a"}`},
		{ID: "s1", Name: spawnAgentToolName, Args: `{"task":"a","role":"explore"}`},
	}
	got := parallelGroups(calls)
	want := [][]int{{0}, {1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("分组应为 %v，实际 %v", want, got)
	}
}
