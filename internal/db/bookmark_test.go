package db

import "testing"

// 导入是「一次性写入几百条」的路径，正确性集中在三件事上：
// 文件夹映射到集合（含组名）、归一化 URL 判重、重复导入幂等。
// 这三条错了用户不会收到任何报错，只会发现「导完少了一半」或「导了两次变两份」。
func TestImportBookmarks(t *testing.T) {
	d := openTestDB(t)

	items := []BookmarkItem{
		{Title: "MDN", URL: "https://developer.mozilla.org/docs/", Folder: []string{"前端", "文档"}},
		// 与上一条归一化后同键：只是尾斜杠与跟踪参数不同
		{Title: "MDN 副本", URL: "https://developer.mozilla.org/docs?utm_source=x", Folder: []string{"杂项"}},
		{Title: "GitHub", URL: "https://github.com/", Folder: nil},
		// 伪协议：应当计入 invalid 而不是建条目
		{Title: "扩展按钮", URL: "javascript:void(0)", Folder: []string{"杂项"}},
	}

	res, err := d.ImportBookmarks("Chrome", items)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if res.Created != 2 {
		t.Errorf("created = %d，期望 2", res.Created)
	}
	if res.Duplicated != 1 {
		t.Errorf("duplicated = %d，期望 1（文件内归一化后重复）", res.Duplicated)
	}
	if res.Invalid != 1 {
		t.Errorf("invalid = %d，期望 1（javascript: 伪协议）", res.Invalid)
	}

	// 文件夹 → 集合：两级路径进组名，根上的书签落「未分类」
	var group, name string
	row, err := d.QueryOne(`SELECT c.group_name, c.name FROM items i
		JOIN collections c ON c.id = i.collection_id WHERE i.value = ?`,
		"https://developer.mozilla.org/docs/")
	if err != nil || row == nil {
		t.Fatalf("条目未落库: %v", err)
	}
	group, name = str(row["group_name"]), str(row["name"])
	if name != "文档" || group != "前端" {
		t.Errorf("集合映射 = (%q, %q)，期望 (前端, 文档)", group, name)
	}

	row, err = d.QueryOne(`SELECT c.group_name, c.name FROM items i
		JOIN collections c ON c.id = i.collection_id WHERE i.value = ?`, "https://github.com/")
	if err != nil || row == nil {
		t.Fatalf("根书签未落库: %v", err)
	}
	if str(row["name"]) != "未分类" || str(row["group_name"]) != "" {
		t.Errorf("根书签集合 = (%q, %q)，期望 (空, 未分类)", str(row["group_name"]), str(row["name"]))
	}

	// 二次导入：三条有效链接全部命中库里已有 URL，一条都不新增（幂等）。
	// 注意这里 skipped 是 3 不是 2：文件里那对归一化重复的条目在库里都已经存在，
	// 此时「文件内重复」的判定没有意义，两条都算「已存在」——用户看到的
	// 「跳过 N 条」应当对应文件里的有效条数。
	res2, err := d.ImportBookmarks("Chrome", items)
	if err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}
	if res2.Created != 0 || res2.Skipped != 3 {
		t.Errorf("二次导入 created=%d skipped=%d，期望 0 / 3", res2.Created, res2.Skipped)
	}

	// 场景同名复用，不会因为再次导入多出一个场景
	scenes, err := d.Query("SELECT id FROM scenes WHERE name = ?", "Chrome")
	if err != nil {
		t.Fatalf("查场景失败: %v", err)
	}
	if len(scenes) != 1 {
		t.Errorf("场景数 = %d，期望 1（同名复用）", len(scenes))
	}
}

// 同集合内重名要自动加后缀，否则列表里两条同名条目无法区分。
func TestImportBookmarksDedupesNames(t *testing.T) {
	d := openTestDB(t)
	items := []BookmarkItem{
		{Title: "同名", URL: "https://a.test/1", Folder: []string{"F"}},
		{Title: "同名", URL: "https://a.test/2", Folder: []string{"F"}},
	}
	if _, err := d.ImportBookmarks("Chrome", items); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	rows, err := d.Query("SELECT name FROM items WHERE type = ? ORDER BY name", "网页")
	if err != nil {
		t.Fatalf("查条目失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("条目数 = %d", len(rows))
	}
	if str(rows[0]["name"]) != "同名" || str(rows[1]["name"]) != "同名 · 2" {
		t.Errorf("重名后缀不对: %q / %q", str(rows[0]["name"]), str(rows[1]["name"]))
	}
}
