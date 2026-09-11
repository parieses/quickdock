package env

import "testing"

// TestRegistryGroupsAreKnown 保证 registry 里每个运行时的 group 都落在前端侧栏的分组清单内。
//
// 背景：EnvironmentPage.vue 的 sidebarGroups 用 `runtimes.filter(r => r.group === g)` 逐组取项，
// 且只有 `if (items.length)` 才 push 到侧栏——所以 group 一旦写错、或写出 GROUP_ORDER 里没有的新值，
// 该运行时就会从侧栏彻底消失，既不报错也没有日志。这个测试把这层「后端 group ↔ 前端 GROUP_ORDER」
// 的对应关系钉住，避免以后新增运行时或改动分组时静默漏项。
func TestRegistryGroupsAreKnown(t *testing.T) {
	// 必须与 frontend/src/components/EnvironmentPage.vue 的 GROUP_ORDER 逐字一致。
	known := map[string]bool{
		GroupLanguage:   true,
		GroupNetwork:    true,
		GroupDatabase:   true,
		GroupMiddleware: true,
		GroupAI:         true,
		GroupTool:       true,
	}

	regMu.RLock()
	defer regMu.RUnlock()

	for rt, def := range registry {
		if def.group == "" {
			t.Errorf("运行时 %s 未设置 group", rt)
			continue
		}
		if !known[def.group] {
			t.Errorf("运行时 %s 的 group=%q 不在前端 GROUP_ORDER 内，会导致它在侧栏不可见", rt, def.group)
		}
	}
}
