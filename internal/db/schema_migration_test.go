package db

import (
	"path/filepath"
	"testing"
)

// TestMigrateLegacyActivityTable 老库升级不能因为 activity 表的索引顺序而中断。
//
// 真实事故（v0.0.25 启动即崩）：activity 表早期只有 id/text/created_at，
// 启用操作流水时新增了 kind/ref_id/created_ts。建表语句用 CREATE TABLE
// IF NOT EXISTS，对已存在的老表是空操作；而当时把
// `CREATE INDEX ... ON activity(created_ts)` 紧跟在这条建表语句后面，
// 执行时表上还没有 created_ts 列 —— 直接报
// `SQL logic error: no such column: created_ts (1)`，
// 迁移返回错误，应用启动失败，日志里只有这一句 SQL 报错，
// 完全看不出是「建索引排在补列之前」。
//
// 这条测试复现老库结构：先把库建好，再把 activity 改回旧结构，
// 然后重新 Open 走一次真实升级路径。
func TestMigrateLegacyActivityTable(t *testing.T) {
	resetActivityDedup()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// ① 正常建库，然后人为退回老结构（只有三列，且已有一条历史数据）
	d1, err := Open(path)
	if err != nil {
		t.Fatalf("首次建库失败: %v", err)
	}
	if _, err := d1.conn.Exec(`DROP TABLE activity`); err != nil {
		t.Fatalf("丢弃新表失败: %v", err)
	}
	if _, err := d1.conn.Exec(`CREATE TABLE activity (
		id TEXT PRIMARY KEY,
		text TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("重建老表失败: %v", err)
	}
	if _, err := d1.conn.Exec(
		`INSERT INTO activity (id, text, created_at) VALUES ('old1', '历史记录', '2026-01-01 00:00:00')`,
	); err != nil {
		t.Fatalf("写入历史数据失败: %v", err)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// ② 重新打开 = 走一遍老库升级。这里失败就是启动失败。
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("老库升级失败（这正是启动崩溃的那一步）: %v", err)
	}
	defer func() { _ = d2.Close() }()

	// ③ 三列都补齐，且历史数据没丢
	for _, col := range []string{"kind", "ref_id", "created_ts"} {
		if !d2.hasColumn("activity", col) {
			t.Errorf("升级后仍缺列 activity.%s", col)
		}
	}
	var text string
	if err := d2.conn.QueryRow(`SELECT text FROM activity WHERE id = 'old1'`).Scan(&text); err != nil {
		t.Fatalf("历史数据丢失: %v", err)
	}
	if text != "历史记录" {
		t.Errorf("历史数据被改写: %q", text)
	}

	// ④ 索引必须建上（它就是排在补列之后的那条语句）
	var idxCount int
	if err := d2.conn.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_activity_created_ts'`,
	).Scan(&idxCount); err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("索引 idx_activity_created_ts 未创建")
	}

	// ⑤ 补齐后的表能正常写入读取
	if err := d2.AddActivity(ActivitySshBatch, "h1", "web-1"); err != nil {
		t.Fatalf("写入动态失败: %v", err)
	}
	list, err := d2.ListActivity(10)
	if err != nil {
		t.Fatalf("读取动态失败: %v", err)
	}
	if len(list) == 0 || list[0].Kind != ActivitySshBatch {
		t.Fatalf("升级后读写异常: %+v", list)
	}
}

// TestMigrateIdempotent 重复 Open 不应报错（每次启动都会跑一遍全量迁移）。
func TestMigrateIdempotent(t *testing.T) {
	resetActivityDedup()
	path := filepath.Join(t.TempDir(), "twice.db")
	for i := 0; i < 3; i++ {
		d, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i+1, err)
		}
		if err := d.Close(); err != nil {
			t.Fatalf("第 %d 次关闭失败: %v", i+1, err)
		}
	}
}
