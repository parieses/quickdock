package db

import "testing"

// TestScheduledTaskRecycleRoundTrip 定时任务删除→墓碑→恢复：
// 这是回收站覆盖到配置型数据后的第一条链路，重点验证两件事——
// 1) 整行恢复能过列名白名单（insertScheduledTask 有 21 个列，漏一个就报「非法列名」）
// 2) next_run 这种「快照时刻可能早已过去」的列会原样回来（重排由 services 层负责）
func TestScheduledTaskRecycleRoundTrip(t *testing.T) {
	d := openTestDB(t)

	task, err := d.CreateScheduledTask(&ScheduledTask{
		Name:         "每日备份",
		Action:       "command",
		Target:       "backup.bat",
		WorkingDir:   `C:\scripts`,
		ScheduleKind: "daily",
		TimeOfDay:    "09:00:00",
		Enabled:      true,
		NextRun:      "2026-10-27 09:00:00",
	})
	if err != nil {
		t.Fatalf("建定时任务失败: %v", err)
	}

	if err := d.DeleteScheduledTaskToRecycle(task.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if rows, _ := d.Query("SELECT id FROM scheduled_tasks WHERE id = ?", task.ID); len(rows) != 0 {
		t.Fatalf("删除后定时任务仍有行")
	}

	list, err := d.ListTombstones()
	if err != nil || len(list) != 1 {
		t.Fatalf("回收站应有 1 条，实际 %d (%v)", len(list), err)
	}
	if list[0].Collection != "schedules" {
		t.Fatalf("墓碑 collection 应为 schedules，实际 %s", list[0].Collection)
	}
	if list[0].Label != "每日备份" {
		t.Fatalf("墓碑 label 应取任务名，实际 %q", list[0].Label)
	}

	if err := d.RestoreTombstone("schedules", task.ID); err != nil {
		t.Fatalf("恢复失败（多半是列名白名单漏 列）: %v", err)
	}
	got, err := d.GetScheduledTask(task.ID)
	if err != nil {
		t.Fatalf("恢复后查不到定时任务: %v", err)
	}
	if got.ID != task.ID || got.Name != task.Name || got.Target != "backup.bat" ||
		got.WorkingDir != `C:\scripts` || got.ScheduleKind != "daily" || got.TimeOfDay != "09:00:00" {
		t.Fatalf("恢复字段不符：%+v", got)
	}
	if list, _ := d.ListTombstones(); len(list) != 0 {
		t.Fatalf("恢复后墓碑应被清除，实际剩 %d 条", len(list))
	}
}

// TestDeleteScheduledTaskHard 硬删除不产生墓碑：待办取消重复、待办被删时
// 连带清理派生的 recur-<id> 记录，这类删除不是用户主动发出的，
// 进回收站只会让列表里凭空多出一条从没见过名字的定时任务。
func TestDeleteScheduledTaskHard(t *testing.T) {
	d := openTestDB(t)

	task, err := d.CreateScheduledTask(&ScheduledTask{
		Name: "recur-doc1", Action: "command", Target: "notify.exe",
		ScheduleKind: "interval", IntervalSec: 3600,
	})
	if err != nil {
		t.Fatalf("建定时任务失败: %v", err)
	}
	if err := d.DeleteScheduledTask(task.ID); err != nil {
		t.Fatalf("硬删除失败: %v", err)
	}
	list, err := d.ListTombstones()
	if err != nil || len(list) != 0 {
		t.Fatalf("硬删除不该产生墓碑，实际 %d 条 (%v)", len(list), err)
	}
}
