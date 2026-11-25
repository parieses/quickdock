package db

import "testing"

// 库里只要有一条 items.working_directory 为 NULL，旧 scanItems 直接把它扫进
// string 会让整批 ListAllItems / GetMostUsedItems / SearchAllItems 全部失败
// （Ctrl+K 全量池和最近使用一起空掉）。这条回归锁住 Null* 兜底行为。
func TestScanItemsWithNullableColumns(t *testing.T) {
	d := openTestDB(t)
	ws, err := d.CreateWorkspace("默认工作空间")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := d.CreateScene(ws.ID, "浏览器", "书签", "")
	if err != nil {
		t.Fatal(err)
	}
	co, err := d.CreateCollection(ws.ID, sc.ID, "收藏夹", "网页集合", "system", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateItem(ws.ID, co.ID, "null-columns", "网页", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	// 显式把可空列写成 NULL（模拟旧包合并 / 第三方写入）。
	// no_search 是 NOT NULL，不能置 NULL，故不在此列。
	if _, err := d.conn.Exec(`UPDATE items SET working_directory = NULL, tool_id = NULL, args = NULL,
		remark = NULL, plugin_data = NULL, icon = NULL, color = NULL, env = NULL,
		type = NULL, tool = NULL, usage_count = NULL, sort = NULL`); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListAllItems()
	if err != nil {
		t.Fatalf("ListAllItems 遇到 NULL 列失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d 条", len(got))
	}
	it := got[0]
	if it.WorkingDirectory != "" || it.ToolID != "" || it.Args != "" || it.Remark != "" ||
		it.PluginData != "" || it.Icon != "" || it.Color != "" || it.Env != "" ||
		it.Type != "" || it.Tool != "" {
		t.Fatalf("NULL 文本列应兜底为空串，得到 %+v", it)
	}
	if it.NoSearch != 0 || it.UsageCount != 0 || it.Sort != 0 {
		t.Fatalf("NULL 整数列应兜底为 0，得到 %+v", it)
	}
}
