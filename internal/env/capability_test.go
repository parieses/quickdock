package env

import (
	"strings"
	"testing"
)

// capabilityWant 是「各运行时实际实现的可选能力接口」的唯一权威清单。
//
// 每个文件末尾都有一组 `var _ XxxCapability = (*XxxRuntime)(nil)` 编译期断言把它固定住；
// 本测试则反向校验：这份清单与真实 method set 必须逐项一致。
//
// 单靠断言只能保证「声明了的确实有」，无法发现「有但没声明」——那正是能力静默丢失的
// 起点（比如某人给 redis 加了 LogProvider，却忘了在文末补断言，下次签名改动就无人报错）。
// 这里用反射补齐反向覆盖。
//
// 维护方式：改了某个 runtime 的能力实现，就把这里对应的一行改掉；
// 测试会告诉你哪些断言该增、哪些该删。
var capabilityWant = map[Runtime][]string{
	RuntimeApache:     {"ServiceController", "LogProvider", "ConfigValidator", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeBun:        nil,
	RuntimeCaddy:      {"ServiceController", "WebConsoleProvider", "ConfigValidator", "SitesConfigHost", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeComposer:   nil,
	RuntimeErlang:     nil,
	RuntimeFFmpeg:     nil,
	RuntimeFrpc:       {"ServiceController", "LogProvider", "ConfigProvider"},
	RuntimeFTP:        {"ServiceController", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeGh:         nil,
	RuntimeGit:        nil,
	RuntimeGo:         nil,
	RuntimeMailpit:    {"ServiceController", "LogProvider", "WebConsoleProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimeMariaDB:    {"ServiceController", "LogProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimeMCP:        {"ServiceController", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeMemcached:  {"ServiceController", "LogProvider", "ConfigPortsProvider"},
	RuntimeMinIO:      {"ServiceController", "LogProvider", "WebConsoleProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimeMkcert:     nil,
	RuntimeMongoDB:    {"ServiceController", "LogProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimeMySQL:      {"ServiceController", "LogProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimeNginx:      {"ServiceController", "LogProvider", "WebConsoleProvider", "ConfigValidator", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeNode:       nil,
	RuntimeOllama:     {"ServiceController", "LogProvider", "ConfigProvider", "ConfigPortsProvider"},
	RuntimePHP:        {"ServiceController", "ConfigPortsProvider"},
	RuntimePostgreSQL: {"ServiceController", "LogProvider", "DataDirProvider", "ConfigPortsProvider"},
	RuntimePython:     nil,
	RuntimeRabbitMQ:   {"ServiceController", "LogProvider", "WebConsoleProvider", "ConfigPortsProvider"},
	RuntimeRedis:      {"ServiceController", "LogProvider", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeTraefik:    {"ServiceController", "LogProvider", "WebConsoleProvider", "ConfigProvider", "ConfigPortsProvider"},
	RuntimeWebDAV:     {"ServiceController", "ConfigProvider", "ConfigPortsProvider"},
}

// capOf 用类型断言实测某适配器实现了哪些可选能力接口。
func capOf(a RuntimeAdapter) []string {
	var has []string
	if _, ok := a.(ServiceController); ok {
		has = append(has, "ServiceController")
	}
	if _, ok := a.(LogProvider); ok {
		has = append(has, "LogProvider")
	}
	if _, ok := a.(WebConsoleProvider); ok {
		has = append(has, "WebConsoleProvider")
	}
	if _, ok := a.(ConfigValidator); ok {
		has = append(has, "ConfigValidator")
	}
	if _, ok := a.(DataDirProvider); ok {
		has = append(has, "DataDirProvider")
	}
	if _, ok := a.(SitesConfigHost); ok {
		has = append(has, "SitesConfigHost")
	}
	if _, ok := a.(ConfigProvider); ok {
		has = append(has, "ConfigProvider")
	}
	if _, ok := a.(ConfigPortsProvider); ok {
		has = append(has, "ConfigPortsProvider")
	}
	return has
}

// TestCapabilitiesMatchDeclared 校验能力清单与真实实现一致。
// 失败信息直接给出「多声明了哪些 / 少声明了哪些」，照提示改 capabilityWant 与文末断言即可。
func TestCapabilitiesMatchDeclared(t *testing.T) {
	m := NewManager()

	for rt, want := range capabilityWant {
		a, ok := m.adapters[rt]
		if !ok {
			t.Errorf("%s: 在 capabilityWant 中列出，但 NewManager 未注册该适配器", rt)
			continue
		}
		got := capOf(a)

		inGot := make(map[string]bool, len(got))
		for _, g := range got {
			inGot[g] = true
		}
		inWant := make(map[string]bool, len(want))
		for _, w := range want {
			inWant[w] = true
		}

		var missing, extra []string
		for _, w := range want {
			if !inGot[w] {
				missing = append(missing, w)
			}
		}
		for _, g := range got {
			if !inWant[g] {
				extra = append(extra, g)
			}
		}
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s 能力清单不符：\n  少声明(实际有,清单没有): %s\n  多声明(清单有,实际没有): %s",
				rt, strings.Join(missing, ", "), strings.Join(extra, ", "))
		}
	}
}

// TestCapabilityWantCoversAllRuntimes 保证 capabilityWant 覆盖了全部注册运行时，
// 避免新增运行时后忘了登记（那样它的能力就无人校验）。
func TestCapabilityWantCoversAllRuntimes(t *testing.T) {
	m := NewManager()
	for rt := range m.adapters {
		if _, ok := capabilityWant[rt]; !ok {
			t.Errorf("运行时 %s 未登记到 capabilityWant，请补上它的能力清单", rt)
		}
	}
	for rt := range capabilityWant {
		if _, ok := m.adapters[rt]; !ok {
			t.Errorf("capabilityWant 中的 %s 未在 NewManager 注册（已废弃？）", rt)
		}
	}
}
