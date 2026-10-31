package db

import (
	"testing"
)

// TestTombstoneItemDeleteRestore 项目删除→墓碑→恢复，id 与字段原样回巢。
func TestTombstoneItemDeleteRestore(t *testing.T) {
	d := openTestDB(t)

	// 造数据：workspace → scene → collection → item（走正常创建路径，
	// 避开裸 INSERT 的 NOT NULL 列遗漏）
	ws, err := d.CreateWorkspace("默认空间")
	if err != nil {
		t.Fatalf("建 workspace 失败: %v", err)
	}
	sc, err := d.CreateScene(ws.ID, "场景一", "通用", "")
	if err != nil {
		t.Fatalf("建 scene 失败: %v", err)
	}
	col, err := d.CreateCollection(ws.ID, sc.ID, "集合一", "目录集合", "single", "")
	if err != nil {
		t.Fatalf("建 collection 失败: %v", err)
	}
	it, err := d.CreateItem(ws.ID, col.ID, "项目甲", "网页", "https://example.com")
	if err != nil {
		t.Fatalf("建 item 失败: %v", err)
	}

	// 删除 → 墓碑应存在且带 label
	if err := d.DeleteItem(it.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	got, _ := d.Query("SELECT name FROM items WHERE id = ?", it.ID)
	if len(got) != 0 {
		t.Fatalf("删除后 items 仍有行")
	}
	list, err := d.ListTombstones()
	if err != nil || len(list) != 1 {
		t.Fatalf("墓碑应有 1 条，实际 %d (%v)", len(list), err)
	}
	if list[0].Collection != "items" || list[0].ID != it.ID || list[0].Label != "项目甲" {
		t.Fatalf("墓碑字段不对: %+v", list[0])
	}

	// 恢复 → 行原样回来（含 value）
	if err := d.RestoreTombstone("items", it.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	rows, _ := d.Query("SELECT name, type, value FROM items WHERE id = ?", it.ID)
	if len(rows) != 1 {
		t.Fatalf("恢复后 items 应有 1 行，实际 %d", len(rows))
	}
	if rows[0]["value"] != "https://example.com" {
		t.Fatalf("恢复后 value 不对: %v", rows[0]["value"])
	}
	// 恢复成功后墓碑移除
	list, _ = d.ListTombstones()
	if len(list) != 0 {
		t.Fatalf("恢复后墓碑应清空，实际 %d", len(list))
	}
}

// TestTombstoneSceneCascade 删场景连带集合/条目一起进墓碑，恢复整棵回来。
func TestTombstoneSceneCascade(t *testing.T) {
	d := openTestDB(t)

	ws, err := d.CreateWorkspace("默认空间")
	if err != nil {
		t.Fatalf("建 workspace 失败: %v", err)
	}
	sc, err := d.CreateScene(ws.ID, "场景一", "通用", "")
	if err != nil {
		t.Fatalf("建 scene 失败: %v", err)
	}
	col, err := d.CreateCollection(ws.ID, sc.ID, "集合一", "目录集合", "single", "")
	if err != nil {
		t.Fatalf("建 collection 失败: %v", err)
	}
	if _, err := d.CreateItem(ws.ID, col.ID, "项目甲", "命令", "echo hi"); err != nil {
		t.Fatalf("建 item 失败: %v", err)
	}

	// 无子集合的场景也不能炸（IN () 空串陷阱）
	empty, err := d.CreateScene(ws.ID, "空场景", "通用", "")
	if err != nil {
		t.Fatalf("建空 scene 失败: %v", err)
	}
	if err := d.DeleteScene(empty.ID); err != nil {
		t.Fatalf("删空场景失败: %v", err)
	}

	// 删带子树的场景
	if err := d.DeleteScene(sc.ID); err != nil {
		t.Fatalf("删场景失败: %v", err)
	}
	for _, q := range []string{
		"SELECT id FROM scenes WHERE id = '" + sc.ID + "'",
		"SELECT id FROM collections WHERE id = '" + col.ID + "'",
		"SELECT id FROM items WHERE collection_id = '" + col.ID + "'",
	} {
		rows, _ := d.Query(q)
		if len(rows) != 0 {
			t.Fatalf("%s 删除后仍有行", q)
		}
	}

	// 恢复场景：子树一起回来
	if err := d.RestoreTombstone("scenes", sc.ID); err != nil {
		t.Fatalf("恢复场景失败: %v", err)
	}
	for _, q := range []string{
		"SELECT id FROM scenes WHERE id = '" + sc.ID + "'",
		"SELECT id FROM collections WHERE id = '" + col.ID + "'",
		"SELECT id FROM items WHERE collection_id = '" + col.ID + "'",
	} {
		rows, _ := d.Query(q)
		if len(rows) != 1 {
			t.Fatalf("%s 恢复后应有 1 行，实际 %d", q, len(rows))
		}
	}
}

// TestTombstonePurgeAndClear 彻底删除与清空。
func TestTombstonePurgeAndClear(t *testing.T) {
	d := openTestDB(t)
	if err := d.AddTombstone("items", "x1", "项目X", map[string][]map[string]interface{}{
		"items": {{"id": "x1", "name": "项目X", "type": "网页", "value": ""}},
	}); err != nil {
		t.Fatalf("写墓碑失败: %v", err)
	}
	if err := d.DeleteTombstone("items", "x1"); err != nil {
		t.Fatalf("彻底删除失败: %v", err)
	}
	list, _ := d.ListTombstones()
	if len(list) != 0 {
		t.Fatalf("彻底删除后应无墓碑")
	}

	// 清空
	_ = d.AddTombstone("notes", "n1", "笔记N", map[string][]map[string]interface{}{
		"notes": {{"id": "n1", "name": "笔记N"}},
	})
	if err := d.ClearTombstones(); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	list, _ = d.ListTombstones()
	if len(list) != 0 {
		t.Fatalf("清空后应无墓碑，实际 %d", len(list))
	}
}

// TestTombstoneNoteFolderRestore 笔记文件夹删除后整棵子树进墓碑，
// 恢复时父节点必须先于子节点写回（notes.parent_id 自引用）。
func TestTombstoneNoteFolderRestore(t *testing.T) {
	d := openTestDB(t)

	// 三层：文件夹 f1 → 文件夹 f2 → 笔记 n1（顺序刻意打乱插入）
	seed := []struct {
		id, keyword, name, parent string
		isFolder                  int
	}{
		{"n1", "leaf", "叶子笔记", "f2", 0},
		{"f2", "sub", "子文件夹", "f1", 1},
		{"f1", "root", "根文件夹", "", 1},
	}
	for _, s := range seed {
		if err := d.ExecuteParams(
			`INSERT INTO notes (id, keyword, content, name, parent_id, is_folder, is_note, created_at)
			 VALUES (?, ?, '', ?, ?, ?, ?, '2026-01-01T00:00:00Z')`,
			[]interface{}{s.id, s.keyword, s.name, s.parent, s.isFolder, 1 - s.isFolder}); err != nil {
			t.Fatalf("建笔记 %s 失败: %v", s.id, err)
		}
	}

	if err := d.DeleteNoteNode("f1"); err != nil {
		t.Fatalf("删除文件夹失败: %v", err)
	}
	for _, id := range []string{"f1", "f2", "n1"} {
		rows, _ := d.Query("SELECT id FROM notes WHERE id = ?", id)
		if len(rows) != 0 {
			t.Fatalf("删除后 notes 仍有 %s", id)
		}
	}
	list, err := d.ListTombstones()
	if err != nil || len(list) != 1 {
		t.Fatalf("墓碑应有 1 条，实际 %d (%v)", len(list), err)
	}
	if list[0].Collection != "notes" || list[0].ID != "f1" || list[0].Label != "根文件夹" {
		t.Fatalf("墓碑字段不对: %+v", list[0])
	}

	if err := d.RestoreTombstone("notes", "f1"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for _, id := range []string{"f1", "f2", "n1"} {
		rows, _ := d.Query("SELECT id, parent_id FROM notes WHERE id = ?", id)
		if len(rows) != 1 {
			t.Fatalf("恢复后 notes 应有 %s，实际 %d", id, len(rows))
		}
	}
	// 层级关系不能被打乱
	rows, _ := d.Query("SELECT parent_id FROM notes WHERE id = ?", "n1")
	if rows[0]["parent_id"] != "f2" {
		t.Fatalf("恢复后 n1.parent_id 应为 f2，实际 %v", rows[0]["parent_id"])
	}
}

// TestTombstoneWorkspaceRestore 删整个工作空间是最重的一次删除，
// 恢复时四张表按父→子顺序写回，层级关系完整。
func TestTombstoneWorkspaceRestore(t *testing.T) {
	d := openTestDB(t)

	if err := d.ExecuteParams(`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, ?, '', '')`,
		[]interface{}{"w1", "工作空间甲"}); err != nil {
		t.Fatalf("建 workspace 失败: %v", err)
	}
	if err := d.ExecuteParams(`INSERT INTO scenes (id, workspace_id, name, created_at, updated_at) VALUES (?, ?, ?, '', '')`,
		[]interface{}{"s1", "w1", "场景一"}); err != nil {
		t.Fatalf("建 scene 失败: %v", err)
	}
	if err := d.ExecuteParams(`INSERT INTO collections (id, workspace_id, scene_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, '', '')`,
		[]interface{}{"c1", "w1", "s1", "集合一"}); err != nil {
		t.Fatalf("建 collection 失败: %v", err)
	}
	if err := d.ExecuteParams(`INSERT INTO items (id, workspace_id, collection_id, name, type, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, '', '')`,
		[]interface{}{"i1", "w1", "c1", "项目甲", "网页", "https://example.com"}); err != nil {
		t.Fatalf("建 item 失败: %v", err)
	}

	if err := d.DeleteWorkspace("w1"); err != nil {
		t.Fatalf("删除工作空间失败: %v", err)
	}
	list, err := d.ListTombstones()
	if err != nil || len(list) != 1 {
		t.Fatalf("墓碑应有 1 条，实际 %d (%v)", len(list), err)
	}
	if list[0].Collection != "workspaces" || list[0].ID != "w1" || list[0].Label != "工作空间甲" {
		t.Fatalf("墓碑字段不对: %+v", list[0])
	}

	if err := d.RestoreTombstone("workspaces", "w1"); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	for _, q := range []string{
		"SELECT id FROM workspaces WHERE id = 'w1'",
		"SELECT id FROM scenes WHERE id = 's1'",
		"SELECT id FROM collections WHERE id = 'c1'",
		"SELECT id FROM items WHERE id = 'i1'",
	} {
		rows, _ := d.Query(q)
		if len(rows) != 1 {
			t.Fatalf("%s 恢复后应有 1 行，实际 %d", q, len(rows))
		}
	}
}
