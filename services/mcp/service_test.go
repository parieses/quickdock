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
