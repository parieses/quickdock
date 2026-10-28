package db

import (
	"path/filepath"
	"testing"
)

// TestInsertClipboardEntryStoresTextType 验证文本条目入库时按正则算出 text_type 并落库，
// 这样前端智能分类标签（url/email/json/code/phone）可以直接下推到 SQL 过滤，
// 而不是把整段历史拉回来再做 JS 正则。
func TestInsertClipboardEntryStoresTextType(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	cases := []struct {
		text string
		want string
	}{
		{"https://example.com", "url"},
		{"user@example.com", "email"},
		{"13800138000", "phone"},
		{`{"a":1}`, "json"},
		{"[1,2,3]", "json"},
		{"function foo(){}", "code"},
		{"hello world", ""},
		{"", ""},
	}
	for _, c := range cases {
		e, err := d.InsertClipboardEntry(c.text, "app")
		if err != nil {
			t.Fatalf("插入 %q 失败: %v", c.text, err)
		}
		if e.TextType != c.want {
			t.Errorf("文本 %q 的 TextType = %q，期望 %q", c.text, e.TextType, c.want)
		}
		// 落库后回读也要一致。
		got, err := d.GetClipboardEntry(e.ID)
		if err != nil {
			t.Fatalf("回读 %q 失败: %v", c.text, err)
		}
		if got.TextType != c.want {
			t.Errorf("回读文本 %q 的 TextType = %q，期望 %q", c.text, got.TextType, c.want)
		}
	}
}

// TestListClipboardEntriesBySmartTypeFiltersCorrect 验证按 text_type 过滤只返回对应智能分类，
// 且 content_type 固定为 text。
func TestListClipboardEntriesBySmartTypeFiltersCorrect(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	if _, err := d.InsertClipboardEntry("https://a.com", "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertClipboardEntry("https://b.com", "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertClipboardEntry("user@example.com", "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.InsertClipboardEntry("plain text", "app"); err != nil {
		t.Fatal(err)
	}
	// 图片条目绝不能出现在任何智能分类里。
	if _, err := d.InsertClipboardImageEntry("img-1", filepath.Join(dir, "a.png"), "hash-1", "", "app"); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListClipboardEntriesBySmartType(0, "url")
	if err != nil {
		t.Fatalf("按 url 过滤失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("url 应返回 2 条，实际 %d 条", len(got))
	}
	for _, e := range got {
		if e.TextType != "url" || e.ContentType != "text" {
			t.Errorf("url 结果混入非 url 条目: %+v", e)
		}
	}

	// 空串 smartType 仍应走 content_type='text' 过滤（等价 text 全部），不应把图片算进来。
	allText, err := d.ListClipboardEntriesBySmartType(0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range allText {
		if e.ContentType != "text" {
			t.Errorf("空 smartType 结果混入非 text 条目: %+v", e)
		}
	}
	if len(allText) != 4 {
		t.Fatalf("空 smartType 应返回 4 条文本，实际 %d 条", len(allText))
	}
}

// TestBackfillClipboardTextType 验证存量（text_type 为空的）文本条目能被补齐，
// 且只更新空值的行、不动已有类型。
func TestBackfillClipboardTextType(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	// 直接写两条无 text_type 的老数据（模拟迁移前的库）。
	if _, err := d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, copy_count, created_at) VALUES (?, 'text', ?, 1, 1)",
		"old-url", "https://legacy.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, copy_count, created_at) VALUES (?, 'text', ?, 1, 2)",
		"old-plain", "just text"); err != nil {
		t.Fatal(err)
	}
	// 已带类型的行不应被覆盖。
	if _, err := d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, text_type, copy_count, created_at) VALUES (?, 'text', ?, 'email', 1, 3)",
		"already", "x@y.com"); err != nil {
		t.Fatal(err)
	}

	if err := d.backfillClipboardTextType(); err != nil {
		t.Fatalf("回填失败: %v", err)
	}

	check := func(id, want string) {
		e, err := d.GetClipboardEntry(id)
		if err != nil {
			t.Fatalf("回读 %s 失败: %v", id, err)
		}
		if e.TextType != want {
			t.Errorf("%s 的 text_type = %q，期望 %q", id, e.TextType, want)
		}
	}
	check("old-url", "url")
	check("old-plain", "")
	check("already", "email")

	// 再次回填应为幂等：无空值行，立即返回、不产生更新。
	if err := d.backfillClipboardTextType(); err != nil {
		t.Fatalf("二次回填失败: %v", err)
	}
	check("old-url", "url")
}
