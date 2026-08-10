package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSqliteQuerySmoke(t *testing.T) {
	dir, err := os.MkdirTemp("", "qd_dbtest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "test.db")

	// 建表 + 插入
	c1, err := toDbConn(DbConnectionInput{DbType: "sqlite", FilePath: dbPath})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := c1.exec("CREATE TABLE t(id INTEGER, name TEXT); INSERT INTO t VALUES(1,'a'),(2,'b');"); err != nil {
		t.Fatalf("create/insert: %v", err)
	}
	c1.close()

	// 查询
	c2, err := toDbConn(DbConnectionInput{DbType: "sqlite", FilePath: dbPath})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.close()
	res, err := c2.exec("SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !res.Success {
		t.Fatalf("unexpected failure: %+v", res)
	}
	if res.RowCount != 2 {
		t.Fatalf("want 2 rows, got %d", res.RowCount)
	}
	if len(res.Columns) != 2 || res.Columns[0] != "id" || res.Columns[1] != "name" {
		t.Fatalf("columns mismatch: %v", res.Columns)
	}
	if res.Rows[0][0] != "1" || res.Rows[0][1] != "a" {
		t.Fatalf("row0 mismatch: %v", res.Rows[0])
	}

	// 非 SELECT（影响行数）
	_, err = c2.exec("UPDATE t SET name='x' WHERE id=1")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	res2, err := c2.exec("UPDATE t SET name='x' WHERE id=1")
	if err != nil {
		t.Fatalf("update2: %v", err)
	}
	if res2.Affected != 1 {
		t.Fatalf("want affected 1, got %d", res2.Affected)
	}
}
