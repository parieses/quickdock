package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openTestDB 建一个临时库，自动清理。
func openTestDB(t *testing.T) *Database {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedExtraTables 往扩展表里塞几条代表性数据。
func seedExtraTables(t *testing.T, d *Database) {
	t.Helper()
	err := d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			"INSERT INTO notes (id, keyword, content, created_at) VALUES (?, ?, ?, ?)",
			"n1", "k1", "笔记内容A", "2026-09-11 10:00:00",
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			"INSERT INTO todos (id, title, done, priority, created_at, sort) VALUES (?, ?, ?, ?, ?, ?)",
			"t1", "待办A", 0, "high", "2026-09-11 10:00:00", 0,
		); err != nil {
			return err
		}
		// usage_frecency 主键是 key，不是 id，单独插一条验证非 id 主键表的往返
		if _, err := tx.Exec(
			"INSERT INTO usage_frecency (key, count, last_used, type, label) VALUES (?, ?, ?, ?, ?)",
			"f1", 3, "2026-09-11 10:00:00", "app", "常用项",
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("写入扩展表失败: %v", err)
	}
}

// TestSnapshotPayloadIncludesExtraTables 导出必须带 tables 字段且覆盖全部扩展表。
func TestSnapshotPayloadIncludesExtraTables(t *testing.T) {
	d := openTestDB(t)
	seedExtraTables(t, d)

	out, err := d.ExportFullDataAsJSON()
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if !strings.Contains(out, `"tables"`) {
		t.Fatalf("导出结果缺少 tables 字段: %s", out)
	}
	for _, name := range syncExtraTables {
		if !strings.Contains(out, `"`+name+`"`) {
			t.Errorf("导出结果缺少扩展表 %s", name)
		}
	}
}

// TestRestoreRoundTripExtraTables 导出 → 清空 → 恢复，扩展表数据必须回来。
func TestRestoreRoundTripExtraTables(t *testing.T) {
	d := openTestDB(t)
	seedExtraTables(t, d)

	out, err := d.ExportFullDataAsJSON()
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	// 破坏现场：清空扩展表
	if err := d.Transaction(func(tx *sql.Tx) error {
		for _, name := range syncExtraTables {
			if _, err := tx.Exec("DELETE FROM " + name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("清空失败: %v", err)
	}

	if err := d.RestoreFromJSON(out); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	notes, err := d.ListTable("notes")
	if err != nil {
		t.Fatalf("读取 notes 失败: %v", err)
	}
	if len(notes) != 1 || notes[0]["keyword"] != "k1" {
		t.Fatalf("notes 未正确恢复: %+v", notes)
	}

	todos, err := d.ListTable("todos")
	if err != nil {
		t.Fatalf("读取 todos 失败: %v", err)
	}
	if len(todos) != 1 || todos[0]["title"] != "待办A" {
		t.Fatalf("todos 未正确恢复: %+v", todos)
	}

	fr, err := d.ListTable("usage_frecency")
	if err != nil {
		t.Fatalf("读取 usage_frecency 失败: %v", err)
	}
	if len(fr) != 1 || fr[0]["key"] != "f1" {
		t.Fatalf("usage_frecency 未正确恢复: %+v", fr)
	}
}

// TestRestoreTextColumnStaysQueryable 恢复后的数据必须在 SQL 层仍能按文本匹配。
// 通用通道把 JSON 反序列化出的值直接绑定回参数，此断言防止将来在恢复路径引入
// 类型转换（如统一转 []byte、二次序列化）而破坏列的类型亲和性。
func TestRestoreTextColumnStaysQueryable(t *testing.T) {
	d := openTestDB(t)
	seedExtraTables(t, d)

	out, err := d.ExportFullDataAsJSON()
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if err := d.RestoreFromJSON(out); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	rows, err := d.ListTableWhere("notes", "content = ?", "笔记内容A")
	if err != nil {
		t.Fatalf("按文本查询失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("按文本查询未命中，恢复路径的类型处理破坏了列亲和性: %+v", rows)
	}
}

// TestRestoreLegacyPayloadKeepsExtraTables 旧版备份（无 tables 字段）恢复时，
// 不得清空扩展表 —— 否则用户升级后恢复旧备份会丢掉笔记 / 待办。
func TestRestoreLegacyPayloadKeepsExtraTables(t *testing.T) {
	d := openTestDB(t)
	seedExtraTables(t, d)

	legacy := `{"workspaces":[],"scenes":[],"collections":[],"items":[],"tools":[]}`
	if err := d.RestoreFromJSON(legacy); err != nil {
		t.Fatalf("恢复旧格式数据失败: %v", err)
	}

	notes, err := d.ListTable("notes")
	if err != nil {
		t.Fatalf("读取 notes 失败: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("旧备份恢复后 notes 被清空: %+v", notes)
	}
}

// TestRestoreUnknownColumnIgnored 备份里含当前库没有的列时应忽略该列，而非整表失败。
func TestRestoreUnknownColumnIgnored(t *testing.T) {
	d := openTestDB(t)

	payload := `{"tables":{"notes":[{"id":"n9","keyword":"k9","content":"c","created_at":"2026-09-11 10:00:00","expand_enabled":"1","col_from_future":"x"}]}}`
	if err := d.RestoreFromJSON(payload); err != nil {
		t.Fatalf("含未知列的恢复应当成功: %v", err)
	}

	notes, err := d.ListTable("notes")
	if err != nil {
		t.Fatalf("读取 notes 失败: %v", err)
	}
	if len(notes) != 1 || notes[0]["keyword"] != "k9" {
		t.Fatalf("忽略未知列后数据未落库: %+v", notes)
	}
}
