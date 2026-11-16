package mcp

// 装配结果的回归护栏。
//
// 这一组不追求覆盖全部工具（那会变成一份每次加工具都要重排的快照，噪声大于价值），
// 只锁两类**静默**失效：
//   1. 某个核心能力从 AI 面里掉了（通常是注册行被删、等级被改高、域在某个场景被关）；
//   2. 两个面（外部 MCP / 内置 AI）的边界又被揉到一起。

import (
	"testing"

	mcpsrv "quickdock/internal/mcp"
)

// TestAISurfaceKeepsCoreTools 内置 AI 在助手档必须拿得到这些核心工具。
//
// 列表刻意短：只放「没有它，AI 就退回聊天机器人」的那些。新增工具不必加进来；
// 这份清单只回答一个问题——「用户报『AI 不会改文件了』时，是配置问题还是代码问题」。
func TestAISurfaceKeepsCoreTools(t *testing.T) {
	registerAllTools()

	sf := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI,
		Policy: mcpsrv.NewPolicy(mcpsrv.LevelWrite, nil), // 默认策略：一个都不关
		Scene:  mcpsrv.SceneAssistant,
	})

	core := []string{
		// 代码域：没有这四个，AI 只能空谈
		"dir_list", "file_read", "file_write", "file_edit", "run_command", "git_diff",
		// 网页域
		"web_fetch",
		// 宿主能力：问「我的待办 / 环境 / 笔记」时要能查
		"app_info", "env_list", "env_status", "note_search", "todo_list",
		"schedule_list", "monitor_list", "clipboard_recent", "log_read",
	}
	for _, name := range core {
		if !sf.Has(name) {
			t.Errorf("内置 AI 助手档缺少核心工具 %q：检查它的注册、等级（应 ≤ LevelWrite）与所属域的 Scenes", name)
		}
	}

	// 高危档默认不该在面上：LevelWrite 是宿主默认上限，LevelRisk 要用户明确放开。
	for _, bad := range []string{"process_kill", "system_command"} {
		if sf.Has(bad) {
			t.Errorf("高危工具 %q 出现在了默认 AI 面上：LevelRisk 应被策略上限挡住", bad)
		}
	}
}

// TestReadOnlySceneHasNoWriteTools 只读档一条写类工具都不能有。
//
// 反过来（「只读档多挂一个读工具」）不算错，所以不在这里断言总量。
func TestReadOnlySceneHasNoWriteTools(t *testing.T) {
	registerAllTools()

	sf := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerAI,
		Policy: mcpsrv.NewPolicy(mcpsrv.LevelWrite, nil),
		Scene:  mcpsrv.SceneReadOnly,
	})
	if len(sf.Tools) == 0 {
		t.Fatal("只读档一个工具都没有：AI 会退化成纯聊天")
	}
	for _, tl := range sf.Tools {
		if tl.Level > mcpsrv.LevelRead {
			t.Errorf("只读档挂上了写类工具 %q（level=%d）", tl.Name, tl.Level)
		}
	}
}

// TestMCPFaceExcludesAgentTools 外部 MCP 面里不能出现 agent 专有工具。
//
// 这条边界一旦破了，外部客户端会看到 file_write / run_command，等于把「AI 能在
// 你的机器上干什么」整套交出去——那批工具的设计前提是「有人盯着并逐次审批」。
func TestMCPFaceExcludesAgentTools(t *testing.T) {
	registerAllTools()

	sf := mcpsrv.Resolve(mcpsrv.ToolScope{
		Caller: mcpsrv.CallerMCP,
		Policy: mcpsrv.NewPolicy(mcpsrv.LevelRisk, nil), // 放到最宽，仍然不该出现
	})
	for _, name := range []string{"file_write", "file_edit", "run_command", "web_fetch", "spawn_agent"} {
		if sf.Has(name) {
			t.Errorf("外部 MCP 面出现了 agent 专有工具 %q", name)
		}
	}
	// 但宿主自己的能力必须在：外部客户端连上来就是为了这些。
	for _, name := range []string{"app_info", "env_list", "note_search", "todo_list"} {
		if !sf.Has(name) {
			t.Errorf("外部 MCP 面缺少宿主能力 %q", name)
		}
	}
}
