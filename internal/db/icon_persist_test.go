package db

import "testing"

// TestPersistExtractedIconsOnlyWritesFresh 锁住「只回写本次新提取的图标」这一行为。
//
// 背景：persistExtractedIcons 曾按「Icon != ""」收集参数，而 DB 里本来就存着图标的
// 条目扫出来 Icon 同样非空，于是每次只读查询（ListAllItems / GetMostUsedItems /
// SearchAllItems）都要白拿一次全局写锁、白构造一条 CASE UPDATE，靠 WHERE icon=”
// 才没有写坏数据。实测 4/4 参数全部未命中，并发下把 p99 从 2.7ms 拖到 11.6ms。
//
// 这个用例防止有人把 iconFresh 优化掉又退回旧写法。
func TestPersistExtractedIconsOnlyWritesFresh(t *testing.T) {
	d := openTestDB(t)

	ws, err := d.CreateWorkspace("perf")
	if err != nil {
		t.Fatalf("创建工作空间失败: %v", err)
	}
	sc, err := d.CreateScene(ws.ID, "scene", "normal", "")
	if err != nil {
		t.Fatalf("创建场景失败: %v", err)
	}
	coll, err := d.CreateCollection(ws.ID, sc.ID, "coll", "", "", "")
	if err != nil {
		t.Fatalf("创建集合失败: %v", err)
	}
	// 类型/值都不触发图标提取，保证 icon 落库为空，模拟「还没回填过图标」的历史条目。
	it, err := d.CreateItem(ws.ID, coll.ID, "notepad", "命令", "notepad")
	if err != nil {
		t.Fatalf("创建条目失败: %v", err)
	}
	if it.Icon != "" {
		t.Fatalf("前置条件不成立：条目不应自带图标，实际 %q", it.Icon)
	}

	iconOf := func() string {
		row, err := d.QueryOne("SELECT icon FROM items WHERE id = ?", it.ID)
		if err != nil {
			t.Fatalf("读图标失败: %v", err)
		}
		return str(row["icon"])
	}

	// 1) 非新提取（iconFresh=false）即便 Icon 非空，也不该写库。
	d.persistExtractedIcons([]CollectionItem{
		{ID: it.ID, Icon: "data:image/png;base64,STALE", iconFresh: false},
	})
	if got := iconOf(); got != "" {
		t.Fatalf("iconFresh=false 的条目被写回了: %q", got)
	}

	// 2) 新提取的图标必须写回。
	d.persistExtractedIcons([]CollectionItem{
		{ID: it.ID, Icon: "data:image/png;base64,FRESH", iconFresh: true},
	})
	if got := iconOf(); got != "data:image/png;base64,FRESH" {
		t.Fatalf("新提取的图标没有写回，实际 %q", got)
	}

	// 3) 已落库的图标不会被后续扫描再次覆盖（WHERE icon='' 的保护仍在）。
	d.persistExtractedIcons([]CollectionItem{
		{ID: it.ID, Icon: "data:image/png;base64,OVERWRITE", iconFresh: true},
	})
	if got := iconOf(); got != "data:image/png;base64,FRESH" {
		t.Fatalf("已落库图标被覆盖了，实际 %q", got)
	}
}

// TestEnrichItemIconReportsFresh 确认 enrichItemIcon 只在真的提取到图标时返回 true，
// 否则 persistExtractedIcons 会把大量「提取失败、Icon 仍为空」的条目也算作待回写。
func TestEnrichItemIconReportsFresh(t *testing.T) {
	cases := []struct {
		name string
		item CollectionItem
		want bool
	}{
		{"已有图标不再提取", CollectionItem{Icon: "data:x", Type: "应用", Value: `C:\a.exe`}, false},
		{"非应用类型不提取", CollectionItem{Type: "网页", Value: "https://example.com"}, false},
		{"空值不提取", CollectionItem{Type: "应用", Value: ""}, false},
	}
	for _, c := range cases {
		item := c.item
		if got := enrichItemIcon(&item); got != c.want {
			t.Errorf("%s: 返回 %v，期望 %v", c.name, got, c.want)
		}
	}
}
