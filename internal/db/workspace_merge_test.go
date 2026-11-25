package db

import (
	"testing"
)

// 升级迁移的正确性直接关系到「老库数据会不会丢」：应用去掉工作空间切换后，
// 非默认空间再也点不到，里面几十上百条数据只能靠这次合并救回来。
// 这里验证三件最容易出错的事：全部空间都并过来、同名场景不重复、书签场景补类型。
func TestMergeWorkspacesIntoDefault(t *testing.T) {
	d := openTestDB(t)

	// openTestDB 的 Open() 已跑过 migrate；此时库里没有工作空间（services 才补建），
	// 手动建一个默认空间 + 两个非默认空间，模拟老库的真实布局。
	defID, err := d.CreateWorkspace(defaultWorkspaceName)
	if err != nil {
		t.Fatalf("建默认空间失败: %v", err)
	}
	bmID, err := d.CreateWorkspace("书签")
	if err != nil {
		t.Fatalf("建书签空间失败: %v", err)
	}
	otherID, err := d.CreateWorkspace("旧项目空间")
	if err != nil {
		t.Fatalf("建旧空间失败: %v", err)
	}

	// 书签空间：一个 Chrome 场景（默认空间没有同名）→ 场景类型应被补成「书签」
	bmScene, err := d.CreateScene(bmID.ID, "Chrome", "通用", "")
	if err != nil {
		t.Fatalf("建书签场景失败: %v", err)
	}
	bmCol, err := d.CreateCollection(bmID.ID, bmScene.ID, "技术", "目录集合", "single", "")
	if err != nil {
		t.Fatalf("建书签集合失败: %v", err)
	}
	if _, err := d.CreateItem(bmID.ID, bmCol.ID, "MDN", "网页", "https://developer.mozilla.org/"); err != nil {
		t.Fatalf("建书签条目失败: %v", err)
	}

	// 旧项目空间：一个与默认空间同名的场景 → 合并后不重复，集合改挂过去
	defScene, err := d.CreateScene(defID.ID, "工作", "项目", "")
	if err != nil {
		t.Fatalf("建默认场景失败: %v", err)
	}
	if _, err := d.CreateCollection(defID.ID, defScene.ID, "默认集合", "目录集合", "single", ""); err != nil {
		t.Fatalf("建默认集合失败: %v", err)
	}
	dupScene, err := d.CreateScene(otherID.ID, "工作", "项目", "")
	if err != nil {
		t.Fatalf("建旧场景失败: %v", err)
	}
	if _, err := d.CreateCollection(otherID.ID, dupScene.ID, "旧集合", "目录集合", "single", ""); err != nil {
		t.Fatalf("建旧集合失败: %v", err)
	}

	// 执行迁移（migrate 内部已跑过一次但当时没有这些空间，这里重跑模拟升级）
	if err := d.mergeWorkspacesIntoDefaultLocked(); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 空间只剩一个，且名字是默认名
	rows, _ := d.ListWorkspaces()
	if len(rows) != 1 || rows[0].Name != defaultWorkspaceName {
		t.Fatalf("工作空间 = %d 个 / 首名 %q，期望 1 个默认空间", len(rows), firstWorkspaceName(rows))
	}

	// 书签场景已挂到默认空间，类型补成「书签」
	scenes, _ := d.ListScenes(defID.ID)
	foundChrome, workCount := false, 0
	for _, s := range scenes {
		if s.Name == "Chrome" {
			foundChrome = true
			if s.Type != bookmarkSceneType {
				t.Errorf("书签场景类型 = %q，期望 %q", s.Type, bookmarkSceneType)
			}
		}
		if s.Name == "工作" {
			workCount++
		}
	}
	if !foundChrome {
		t.Error("书签空间的 Chrome 场景未迁入默认空间")
	}
	if workCount != 1 {
		t.Errorf("同名场景「工作」合并后应只有 1 个，实际 %d 个", workCount)
	}

	// 两个来自不同空间的集合都在，且都挂默认空间
	names := map[string]bool{}
	for _, sc := range scenes {
		cols, _ := d.ListCollections(sc.ID)
		for _, c := range cols {
			names[c.Name] = true
			if c.WorkspaceID != defID.ID {
				t.Errorf("集合 %s 的 workspaceId = %s，期望 %s", c.Name, c.WorkspaceID, defID.ID)
			}
		}
	}
	for _, want := range []string{"技术", "默认集合", "旧集合"} {
		if !names[want] {
			t.Errorf("集合 %q 迁移后丢失", want)
		}
	}

	// 书签条目仍可查到（workspaceId 已改）
	items, _ := d.ListAllItems()
	found := false
	for _, it := range items {
		if it.Value == "https://developer.mozilla.org/" {
			found = true
			if it.WorkspaceID != defID.ID {
				t.Errorf("书签条目 workspaceId = %s，期望 %s", it.WorkspaceID, defID.ID)
			}
		}
	}
	if !found {
		t.Error("书签条目迁移后丢失")
	}
}

func firstWorkspaceName(rows []Workspace) string {
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Name
}
