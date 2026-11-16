package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 合并的两条核心约定：
//  1. 同名主键保留本地那份（本地优先），只把新行并进来；
//  2. 显式排除的表（app_state、credentials 等）一个字节都不动。
func TestMergeDatabaseFileKeepsLocalAndAddsNew(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.db")
	impPath := filepath.Join(dir, "imp.db")

	exec := func(path string, stmts ...string) {
		t.Helper()
		c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		for _, s := range stmts {
			if _, err := c.Exec(s); err != nil {
				t.Fatalf("执行 %q 失败: %v", s, err)
			}
		}
	}

	// 本地：t1 有 id=1（标题本地），app_state 有本机密钥。
	exec(mainPath,
		`CREATE TABLE t1 (id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO t1 (id, title) VALUES ('1', 'local')`,
		`INSERT INTO app_state (key, value) VALUES ('cred_dek', 'local-key')`,
	)
	// 导入包：同 id 的 t1 是另一份内容，另加一条 id=2；app_state 带另一台机器的密钥。
	exec(impPath,
		`CREATE TABLE t1 (id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE app_state (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO t1 (id, title) VALUES ('1', 'remote')`,
		`INSERT INTO t1 (id, title) VALUES ('2', 'remote-only')`,
		`INSERT INTO app_state (key, value) VALUES ('cred_dek', 'remote-key')`,
	)

	st, err := MergeDatabaseFile(mainPath, impPath)
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if st.Rows != 1 {
		t.Errorf("应只新增 1 行，实际 %d", st.Rows)
	}

	c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(mainPath))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var title string
	if err := c.QueryRow(`SELECT title FROM t1 WHERE id = '1'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "local" {
		t.Errorf("同名主键应保留本地内容，实际 %q", title)
	}
	var n int
	if err := c.QueryRow(`SELECT COUNT(*) FROM t1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应有 2 行，实际 %d", n)
	}
	var dek string
	if err := c.QueryRow(`SELECT value FROM app_state WHERE key = 'cred_dek'`).Scan(&dek); err != nil {
		t.Fatal(err)
	}
	if dek != "local-key" {
		t.Errorf("app_state 不该被导入覆盖，实际 %q", dek)
	}
}
