package mcp

import (
	"testing"

	mcpsrv "quickdock/internal/mcp"
	"quickdock/services"
	pluginsvc "quickdock/services/plugin"
)

// TestPluginExecuteReachableByDefault 锁定 plugin_execute 在默认等级（LevelWrite）下可见。
// LevelRisk 的前端入口是刻意不开放的（环境管理页只有「只读 / 只读+低危写」两档），
// 一旦把它标成 Risk，AI 就永远调不到插件，等同于没有这个工具。
func TestPluginExecuteReachableByDefault(t *testing.T) {
	s := &MCPService{}
	s.registerTools()

	for _, tl := range mcpsrv.List() {
		if tl.Name == "plugin_execute" {
			return
		}
	}
	t.Fatal("plugin_execute 未在默认等级下暴露：可能被设成了 LevelRisk（前端无法开启，工具等同不存在）")
}

// TestMCPStatusSeparatesExposedAndBlocked 锁定面板能同时看到「开放了多少」与「总共多少」。
//
// 只报一个数时，用户把等级从「低危写」调到「只读」会发现工具变少，
// 第一反应是服务坏了；有了 total / blocked 才能一眼看出是等级门正常生效。
func TestMCPStatusSeparatesExposedAndBlocked(t *testing.T) {
	prev := mcpsrv.MaxLevel()
	defer mcpsrv.SetMaxLevel(prev)
	mcpsrv.SetMaxLevel(mcpsrv.LevelWrite)

	s := &MCPService{}
	s.registerTools()

	res := s.MCPStatus()
	if res.Code != 0 {
		t.Fatalf("MCPStatus 应成功，实际: %s", res.Msg)
	}
	d, _ := res.Data.(map[string]any)
	total, exposed, blocked := toInt(d["total"]), toInt(d["tools"]), toInt(d["blocked"])
	if total <= exposed {
		t.Fatalf("高危工具应被计入 total 但不计入 tools，实际 total=%d tools=%d", total, exposed)
	}
	if blocked != total-exposed {
		t.Errorf("blocked 应等于 total-tools，实际 %d != %d-%d", blocked, total, exposed)
	}
}

// TestMCPToolsMarksHighRiskUnexposed 高危工具必须出现在清单里且 exposed=false。
//
// 这是给「工具为什么调不通」留解释的：界面上能看到它存在、只是没开放，
// 模型就不会反复去调一个永远返回「未知工具」的名字。
func TestMCPToolsMarksHighRiskUnexposed(t *testing.T) {
	prev := mcpsrv.MaxLevel()
	defer mcpsrv.SetMaxLevel(prev)
	mcpsrv.SetMaxLevel(mcpsrv.LevelWrite)

	s := &MCPService{}
	s.registerTools()

	list, _ := s.MCPTools().Data.([]map[string]any)
	found, bad := 0, 0
	for _, t := range list {
		level := toInt(t["level"])
		exposed, _ := t["exposed"].(bool)
		if level > mcpsrv.LevelWrite {
			found++
			if exposed {
				bad++
			}
		}
	}
	if found == 0 {
		t.Fatal("工具清单里应有高危工具（否则本断言失去意义）")
	}
	if bad > 0 {
		t.Errorf("有 %d 个高危工具被标记为开放，等级门失效", bad)
	}
}

// TestMCPCheckRejectsWhenStopped 未启动时的自检必须给出失败结论，
// 而不是返回 Code=0 让用户以为「服务正常、是客户端的问题」。
func TestMCPCheckRejectsWhenStopped(t *testing.T) {
	if mcpsrv.Default.Running() {
		t.Skip("MCP 服务正在运行，跳过未启动分支")
	}
	s := &MCPService{}
	if res := s.MCPCheck(); res.Code == 0 {
		t.Fatal("服务未启动时自检应失败")
	}
}

// toInt 把 ApiResult 里的数值取成 int：直接调用拿到 int，
// 经 JSON 传输后拿到 float64，两种都要认。
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// TestPluginRuntimeUninitialized 插件服务未就绪时 pluginRuntime 必须安全返回空串，
// 不能 panic，也不能把「查不到」误判成 none 而拒绝执行。
func TestPluginRuntimeUninitialized(t *testing.T) {
	// PluginMgr 未注入：ListPlugins 返回失败，pluginRuntime 应降级为空串而非 panic
	s := &MCPService{Plugin: &pluginsvc.PluginService{App: &services.AppService{}}}
	if got := s.pluginRuntime("any-plugin"); got != "" {
		t.Fatalf("插件服务未就绪时应返回空串，实际: %q", got)
	}
	s2 := &MCPService{}
	if got := s2.pluginRuntime("any-plugin"); got != "" {
		t.Fatalf("Plugin 为 nil 时应返回空串，实际: %q", got)
	}
}
