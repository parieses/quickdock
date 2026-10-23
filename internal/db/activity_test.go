package db

import (
	"sync"
	"testing"
	"time"
)

// resetActivityDedup 清掉包级去重缓存。
//
// 去重表是包级 sync.Map（埋点来自任意 goroutine，不做实例字段），
// 测试之间会互相污染：上一条用例记过的 item:x 会让下一条同样 key 的写入被静默丢弃。
func resetActivityDedup() {
	activitySeen = sync.Map{}
}

// TestActivityAddAndList 验证写入与倒序读取。
func TestActivityAddAndList(t *testing.T) {
	resetActivityDedup()
	d := openTestDB(t)

	if err := d.AddActivity(ActivityItemOpened, "i1", "项目甲"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 两条记录可能落在同一毫秒内，才需要显式错开 created_ts 来锁定顺序
	time.Sleep(2 * time.Millisecond)
	if err := d.AddActivity(ActivitySshConnected, "h1", "prod-01"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	list, err := d.ListActivity(10)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("期望 2 条，实际 %d 条", len(list))
	}
	if list[0].Text != "prod-01" || list[0].Kind != ActivitySshConnected {
		t.Errorf("最新一条应是 SSH 记录，实际 %+v", list[0])
	}
	if list[1].Text != "项目甲" {
		t.Errorf("第二条应是项目名，实际 %q", list[1].Text)
	}
	if list[0].CreatedTs <= 0 {
		t.Errorf("createdTs 应为 Unix 毫秒，实际 %d", list[0].CreatedTs)
	}
	if list[0].CreatedAt == "" {
		t.Error("createdAt 不应为空")
	}
}

// TestActivityDedup 验证冷却窗口内同一对象只记一条，不同对象互不影响。
func TestActivityDedup(t *testing.T) {
	resetActivityDedup()
	d := openTestDB(t)

	for i := 0; i < 3; i++ {
		if err := d.AddActivity(ActivityItemOpened, "same", "同一个项目"); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	list, _ := d.ListActivity(10)
	if len(list) != 1 {
		t.Fatalf("冷却窗口内重复记录应被抑制，期望 1 条，实际 %d 条", len(list))
	}

	// 换 refID 不受影响
	if err := d.AddActivity(ActivityItemOpened, "other", "另一个项目"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 换 kind 不受影响：同一 id 的「打开」和「连接」是两件事
	if err := d.AddActivity(ActivitySshConnected, "same", "同名主机"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	list, _ = d.ListActivity(10)
	if len(list) != 3 {
		t.Fatalf("期望 3 条，实际 %d 条", len(list))
	}
}

// TestActivityEmptyNameSkipped 验证空对象名不落库（没有意义的流水不占位置）。
func TestActivityEmptyNameSkipped(t *testing.T) {
	resetActivityDedup()
	d := openTestDB(t)

	if err := d.AddActivity(ActivityItemOpened, "x", ""); err != nil {
		t.Fatalf("空名不该报错，实际 %v", err)
	}
	list, _ := d.ListActivity(10)
	if len(list) != 0 {
		t.Fatalf("空名不应落库，实际 %d 条", len(list))
	}
}

// TestActivityTrim 验证超过上限后按 created_ts 裁掉最旧的。
func TestActivityTrim(t *testing.T) {
	resetActivityDedup()
	d := openTestDB(t)

	// 直接 SQL 灌到上限之上，绕过 AddActivity 的去重与逐条裁剪。
	// 必须走一个事务：805 条单条 Exec 各自 fsync，在普通磁盘上要跑一分多钟，
	// 单测慢到这个量级就没人愿意跑了。
	total := maxActivityRows + 5
	tx, err := d.conn.Begin()
	if err != nil {
		t.Fatalf("开启事务失败: %v", err)
	}
	for i := 0; i < total; i++ {
		if _, err := tx.Exec(
			`INSERT INTO activity (id, kind, ref_id, text, created_at, created_ts) VALUES (?, 'item', ?, ?, '', ?)`,
			newID(), "r", "旧记录", int64(1_600_000_000_000+i),
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("灌数据失败: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	// 把计数器推到「下一次写入即触发裁剪」
	activityTrimTick = activityTrimInterval - 1
	if err := d.AddActivity(ActivityItemOpened, "fresh", "新记录"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	var cnt int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM activity").Scan(&cnt); err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if cnt != maxActivityRows {
		t.Fatalf("期望裁剪到 %d 条，实际 %d 条", maxActivityRows, cnt)
	}
	// 最新那条必须还在
	list, _ := d.ListActivity(1)
	if len(list) != 1 || list[0].Text != "新记录" {
		t.Fatalf("裁剪后最新记录应保留，实际 %+v", list)
	}
}

// TestActivityClear 验证清空。
func TestActivityClear(t *testing.T) {
	resetActivityDedup()
	d := openTestDB(t)

	_ = d.AddActivity(ActivityItemOpened, "a", "甲")
	_ = d.AddActivity(ActivityItemOpened, "b", "乙")
	if err := d.ClearActivity(); err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	list, _ := d.ListActivity(10)
	if len(list) != 0 {
		t.Fatalf("清空后应为空，实际 %d 条", len(list))
	}
}
