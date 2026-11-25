package bookmark

import "testing"

func row(id, name, url, remark string) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "name": name, "value": url, "remark": remark,
		"collection_name": "集合A", "scene_name": "Chrome", "usage_count": int64(0),
	}
}

func TestGroupDuplicatesByURL(t *testing.T) {
	rows := []map[string]interface{}{
		row("1", "MDN", "https://developer.mozilla.org/docs/", ""),
		// 尾斜杠 + utm 参数，归一化后与上一行同键
		row("2", "MDN 文档", "https://developer.mozilla.org/docs?utm_source=x", ""),
		row("3", "GitHub", "https://github.com/", ""),
	}
	groups := groupDuplicates(rows)
	if len(groups) != 1 {
		t.Fatalf("组数 = %d，期望 1（只有 MDN 重复）", len(groups))
	}
	if groups[0].Kind != "url" || len(groups[0].Items) != 2 {
		t.Errorf("分组错: kind=%s n=%d", groups[0].Kind, len(groups[0].Items))
	}
	if extra := duplicateExtra(groups); extra != 1 {
		t.Errorf("多余条数 = %d，期望 1", extra)
	}
}

func TestGroupDuplicatesByTitle(t *testing.T) {
	// 标题相同、URL 不同，且两个 URL 各自唯一 → 归一为标题组
	rows := []map[string]interface{}{
		row("1", "Go 官方文档", "https://go.dev/doc/", ""),
		row("2", "Go 官方文档", "https://golang.google.cn/doc/", ""),
		row("3", "另一个站点", "https://example.com/", ""),
	}
	groups := groupDuplicates(rows)
	if len(groups) != 1 || groups[0].Kind != "title" {
		t.Fatalf("期望 1 个标题组，实际 %+v", groups)
	}
	if duplicateExtra(groups) != 1 {
		t.Errorf("多余条数应为 1")
	}
}

// URL 已被判为重复的条目不该再进标题组，否则同一批数据会被报两次、
// 前端「多余 N 条」也会翻倍。
func TestGroupDuplicatesNoDoubleCount(t *testing.T) {
	rows := []map[string]interface{}{
		row("1", "同名", "https://a.test/x", ""),
		row("2", "同名", "https://a.test/x/", ""),
	}
	groups := groupDuplicates(rows)
	if len(groups) != 1 {
		t.Fatalf("同一组数据被重复报成 %d 组", len(groups))
	}
	if groups[0].Kind != "url" {
		t.Errorf("应优先归为 URL 组，实际 %s", groups[0].Kind)
	}
}

func TestGroupDuplicatesEmpty(t *testing.T) {
	if g := groupDuplicates(nil); len(g) != 0 {
		t.Errorf("空输入应为 0 组")
	}
}
