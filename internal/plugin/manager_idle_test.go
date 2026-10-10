package plugin

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDegradeIdlePlugins 验证闲置降级的选择逻辑：仅「运行中 + 窗口不可见 + 闲置超阈值」
// 的插件被降级为就绪（registered）；可见窗口 / 已禁用 / 非运行中 / 近期活跃 均保持原状。
func TestDegradeIdlePlugins(t *testing.T) {
	m := newTestManager()
	// 副作用路径（PID 文件写盘、目录锁扫描）重定向到临时目录，避免污染仓库。
	tmp := t.TempDir()
	m.pluginsDir = tmp
	m.pidFilePath = filepath.Join(tmp, "plugin_pids.json")
	m.idleDegradeAfter = time.Millisecond

	old := time.Now().Add(-time.Hour).UnixNano()
	fresh := time.Now().Add(time.Second).UnixNano()

	// 1) 应被降级：running + 不可见 + 闲置超阈值
	idle := NewPluginInstance(PluginManifest{ID: "idle"}, tmp)
	idle.SetStatus("running")
	idle.lastActiveAt.Store(old)
	m.plugins["idle"] = idle

	// 2) 跳过：窗口正打开（可见）
	visible := NewPluginInstance(PluginManifest{ID: "visible"}, tmp)
	visible.SetStatus("running")
	visible.lastActiveAt.Store(old)
	m.plugins["visible"] = visible

	// 3) 跳过：已禁用
	disabled := NewPluginInstance(PluginManifest{ID: "disabled"}, tmp)
	disabled.SetStatus("running")
	disabled.SetDisabled(true)
	disabled.lastActiveAt.Store(old)
	m.plugins["disabled"] = disabled

	// 4) 跳过：非运行中（registered 懒加载占位）
	registered := NewPluginInstance(PluginManifest{ID: "registered"}, tmp)
	registered.SetStatus(statusRegistered)
	registered.lastActiveAt.Store(old)
	m.plugins["registered"] = registered

	// 5) 跳过：近期活跃（未超阈值）
	freshInst := NewPluginInstance(PluginManifest{ID: "fresh"}, tmp)
	freshInst.SetStatus("running")
	freshInst.lastActiveAt.Store(fresh)
	m.plugins["fresh"] = freshInst

	// 6) 跳过：none 运行时（纯前端，无后端进程可释放）
	noneRt := NewPluginInstance(PluginManifest{ID: "noneRt", Backend: BackendConfig{Runtime: "none"}}, tmp)
	noneRt.SetStatus("running")
	noneRt.lastActiveAt.Store(old)
	m.plugins["noneRt"] = noneRt

	// 仅 "visible" 视为窗口可见
	m.isWindowVisible = func(id string) bool { return id == "visible" }

	m.degradeIdlePlugins()

	// idle 被降级：状态回到 registered 且 stopped 置位（曾被实际终止）
	if got := idle.GetStatus(); got != statusRegistered {
		t.Errorf("idle: 期望降级为 %q，实际 %q", statusRegistered, got)
	}
	if !idle.stopped.Load() {
		t.Errorf("idle: 降级应置 stopped=true（曾被终止），实际 false")
	}

	// 其余插件均不应被降级：stopped 保持 false、status 保持原状
	for _, id := range []string{"visible", "disabled", "registered", "fresh", "noneRt"} {
		if m.plugins[id].stopped.Load() {
			t.Errorf("%s: 不应被降级（stopped 被误置）", id)
		}
	}
	if m.plugins["visible"].GetStatus() != "running" {
		t.Errorf("visible: 期望仍 running，实际 %q", m.plugins["visible"].GetStatus())
	}
	if m.plugins["registered"].GetStatus() != statusRegistered {
		t.Errorf("registered: 期望仍 %q，实际 %q", statusRegistered, m.plugins["registered"].GetStatus())
	}
	if m.plugins["fresh"].GetStatus() != "running" {
		t.Errorf("fresh: 期望仍 running，实际 %q", m.plugins["fresh"].GetStatus())
	}
	// disabled 禁用态 GetStatus 兜底 stopped（即便曾被标记为 running）
	if got := m.plugins["disabled"].GetStatus(); got != "stopped" {
		t.Errorf("disabled: 期望仍 stopped，实际 %q", got)
	}
}
