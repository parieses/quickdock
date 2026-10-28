package db

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestListClipboardEntriesByTypeFiltersCorrect 验证按 content_type 过滤的
// SQL 下推确实只返回该类型条目。
//
// 背景：剪贴板「图片/文件」标签曾完全筛不出东西——前端把 image/file 标签
// 和智能分类标签合用一个分支，统一套了 `contentType === 'text' &&` 前置条件，
// 两个条件互斥。后端本来就有 ListClipboardEntriesByType，但前端压根没调用它。
func TestListClipboardEntriesByTypeFiltersCorrect(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	// 三种类型各插一条。
	if _, err := d.InsertClipboardEntry("纯文本内容", "app"); err != nil {
		t.Fatalf("插入文本条目失败: %v", err)
	}
	if _, err := d.InsertClipboardImageEntry("img-1", filepath.Join(dir, "a.png"), "hash-1", "", "app"); err != nil {
		t.Fatalf("插入图片条目失败: %v", err)
	}
	if _, err := d.InsertClipboardFileEntry(`C:\tmp\a.txt`, "app"); err != nil {
		t.Fatalf("插入文件条目失败: %v", err)
	}

	all, err := d.ListClipboardEntries(0)
	if err != nil {
		t.Fatalf("列出全部条目失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("应有 3 条条目，实际 %d 条", len(all))
	}

	cases := []struct {
		typ  string
		want int
	}{
		{"image", 1},
		{"file", 1},
		{"text", 1},
		{"", 3}, // 空串等价于不按类型过滤
	}
	for _, c := range cases {
		got, err := d.ListClipboardEntriesByType(0, c.typ)
		if err != nil {
			t.Fatalf("按 %q 过滤失败: %v", c.typ, err)
		}
		if len(got) != c.want {
			t.Errorf("content_type=%q 应返回 %d 条，实际 %d 条", c.typ, c.want, len(got))
		}
		// 过滤结果里的 content_type 必须与请求一致（空串时不限）。
		if c.typ != "" {
			for _, e := range got {
				if e.ContentType != c.typ {
					t.Errorf("content_type=%q 的结果里混入了 %q 的条目", c.typ, e.ContentType)
				}
			}
		}
	}
}

// TestListClipboardEntriesByTypeRespectsLimit 验证 limit 生效，
// 避免下推后把整表拉回来。
func TestListClipboardEntriesByTypeRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	for i := 0; i < 5; i++ {
		if _, err := d.InsertClipboardEntry("文本", "app"); err != nil {
			t.Fatalf("插入失败: %v", err)
		}
		// id 是主键，必须逐条不同；image_hash 是去重键，也逐条不同。
		if _, err := d.InsertClipboardImageEntry(
			fmt.Sprintf("img-%d", i),
			filepath.Join(dir, fmt.Sprintf("a%d.png", i)),
			fmt.Sprintf("hash-%d", i),
			"",
			"app",
		); err != nil {
			t.Fatalf("插入图片失败: %v", err)
		}
	}

	got, err := d.ListClipboardEntriesByType(2, "image")
	if err != nil {
		t.Fatalf("按类型过滤失败: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("limit=2 应返回 2 条，实际 %d 条", len(got))
	}

	// limit 超上限时应被夹到 MAX_CLIPBOARD_ROWS 而不是报错。
	if _, err := d.ListClipboardEntriesByType(999999, "image"); err != nil {
		t.Errorf("超大 limit 不应报错: %v", err)
	}
}

// TestListClipboardEntriesByTypeEmptyTable 空表不应报错。
func TestListClipboardEntriesByTypeEmptyTable(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer d.Close()

	got, err := d.ListClipboardEntriesByType(10, "image")
	if err != nil {
		t.Fatalf("空表查询失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空表应返回 0 条，实际 %d 条", len(got))
	}
	_ = os.Remove(filepath.Join(dir, "test.db"))
}
