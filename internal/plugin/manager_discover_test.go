package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDiscoverAndLoadKeepsDisabledPlugin 回归：禁用插件必须在启动期登记进 m.plugins，
// 否则 ListPlugins 不返回、管理页无法重新启用（禁用即消失的 bug）。
func TestDiscoverAndLoadKeepsDisabledPlugin(t *testing.T) {
	dir := t.TempDir()

	writePlugin := func(id string) {
		p := filepath.Join(dir, id)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := PluginManifest{
			ID:      id,
			Name:    id,
			Version: "1.0.0",
			Backend: BackendConfig{Runtime: "none"},
		}
		b, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(p, "plugin.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePlugin("enabled-plug")
	writePlugin("disabled-plug")

	m := &Manager{
		plugins:             make(map[string]*PluginInstance),
		pluginsDir:          dir,
		hostMethods:         make(map[string]HostMethod),
		loadLocks:           make(map[string]*sync.Mutex),
		EnableLazyPluginLoad: true,
	}

	// 仅 enabled-plug 在启用集合内
	if err := m.DiscoverAndLoad(func(id string) bool { return id == "enabled-plug" }); err != nil {
		t.Fatalf("DiscoverAndLoad error: %v", err)
	}

	all := m.ListPlugins()
	if len(all) != 2 {
		t.Fatalf("期望 2 个插件（含已禁用），实际 %d: %+v", len(all), all)
	}

	byID := map[string]string{}
	for _, p := range all {
		byID[p.ID] = p.Status
	}
	if byID["enabled-plug"] != "running" {
		t.Errorf("enabled-plug 状态应为 running，实际 %q", byID["enabled-plug"])
	}
	if byID["disabled-plug"] != "stopped" {
		t.Errorf("disabled-plug 状态应为 stopped（留在列表可重新启用），实际 %q", byID["disabled-plug"])
	}
}

// TestEnsureLoadedRefusesDisabled 回归：已禁用插件不得经 EnsureLoaded 惰性复活，
// 否则 DisablePlugin 形同虚设、内存 running 与 DB disabled 状态不一致。
func TestEnsureLoadedRefusesDisabled(t *testing.T) {
	m := &Manager{
		plugins:     make(map[string]*PluginInstance),
		pluginsDir:  t.TempDir(),
		hostMethods: make(map[string]HostMethod),
		loadLocks:   make(map[string]*sync.Mutex),
	}
	inst := NewPluginInstance(PluginManifest{ID: "x", Name: "x", Version: "1.0.0"}, m.pluginsDir)
	inst.SetStatus("stopped")
	inst.disabled.Store(true)
	m.plugins["x"] = inst

	if err := m.EnsureLoaded("x"); err == nil {
		t.Fatal("Expect EnsureLoaded 拒绝禁用插件，但返回了 nil")
	}
}

// TestEnsureLoadedAllowsEnabled 健全性：已启用（registered）插件仍可被惰性复活。
func TestEnsureLoadedAllowsEnabled(t *testing.T) {
	m := &Manager{
		plugins:     make(map[string]*PluginInstance),
		pluginsDir:  t.TempDir(),
		hostMethods: make(map[string]HostMethod),
		loadLocks:   make(map[string]*sync.Mutex),
	}
	inst := NewPluginInstance(PluginManifest{ID: "y", Name: "y", Version: "1.0.0"}, m.pluginsDir)
	inst.SetStatus(statusRegistered)
	m.plugins["y"] = inst

	// 无磁盘 manifest，EnsureLoaded 会因找不到文件报错（而非「已禁用」），
	// 这里只验证它「不会因为 disabled 标记而拒绝」——错误应为文件不存在类，而非禁用拒绝。
	err := m.EnsureLoaded("y")
	if err != nil && strings.Contains(err.Error(), "已禁用") {
		t.Fatalf("已启用插件不应被拒绝，但得到: %v", err)
	}
}
