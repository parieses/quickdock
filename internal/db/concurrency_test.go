package db

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
)

// TestConnectionPragmas 连接池放开后，每条新建连接都必须带上连接级 PRAGMA。
// foreign_keys / busy_timeout 只在执行它的那条连接生效——若 DSN 未下发，
// 新连接会静默丢失外键约束，并发写也会直接报 SQLITE_BUSY 而不是等待。
func TestConnectionPragmas(t *testing.T) {
	d := openTestDB(t)

	cases := []struct {
		pragma string
		want   int64
	}{
		{"PRAGMA foreign_keys", 1},
		{"PRAGMA busy_timeout", 5000},
	}

	var wg sync.WaitGroup
	// 并发数大于连接池上限，强制池新建多条连接
	for i := 0; i < dbMaxOpenConns*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range cases {
				rows, err := d.Query(c.pragma)
				if err != nil {
					t.Errorf("%s 查询失败: %v", c.pragma, err)
					return
				}
				if len(rows) != 1 {
					t.Errorf("%s 返回 %d 行，期望 1 行", c.pragma, len(rows))
					return
				}
				// PRAGMA 返回的列名随 SQLite 版本而变，取唯一值避免依赖列名
				var got int64
				for _, v := range rows[0] {
					got, _ = v.(int64)
				}
				if got != c.want {
					t.Errorf("%s = %d，期望 %d（新连接未继承 PRAGMA）", c.pragma, got, c.want)
				}
			}
		}()
	}
	wg.Wait()
}

// TestConcurrentReadWrite 并发读 + 并发写不得死锁、不得破坏数据。
// 同时压 hasColumn 的列结构缓存：读方法只持 mu.RLock 也会写 colCache，
// 该缓存必须由 colMu 保护，否则 race detector 会直接报数据竞争。
func TestConcurrentReadWrite(t *testing.T) {
	d := openTestDB(t)

	insert := func(id, content string) error {
		return d.Transaction(func(tx *sql.Tx) error {
			_, err := tx.Exec(
				"INSERT INTO notes (id, keyword, content, created_at) VALUES (?, ?, ?, ?)",
				id, "k", content, "2026-09-12 10:00:00",
			)
			return err
		})
	}
	if err := insert("n1", "初始内容"); err != nil {
		t.Fatalf("插入初始数据失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < dbMaxOpenConns+4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				// 读：走 RLock，并触发 hasColumn 缓存填充
				if _, err := d.ListTable("notes"); err != nil {
					t.Errorf("并发读失败: %v", err)
					return
				}
				// 写：走独占锁
				if err := d.ExecuteParams("UPDATE notes SET content = ? WHERE id = ?",
					[]interface{}{fmt.Sprintf("w%d-%d", i, j), "n1"}); err != nil {
					t.Errorf("并发写失败: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	list, err := d.ListTable("notes")
	if err != nil {
		t.Fatalf("收尾读取失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("并发写后行数 = %d，期望 1（并发写破坏了一致性）", len(list))
	}
}
