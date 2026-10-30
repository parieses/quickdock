package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// 数据库文件级导出（VACUUM INTO）。
//
// 与本包的「业务快照」（snapshot.go：导出行数据成 JSON）不是一回事——
// 这里搬的是数据库文件本身，用于数据目录迁移。
//
// 为什么不能直接复制文件：
//  1. WAL 模式下主库文件可能落后于 -wal 预写日志，逐字节复制主库会丢事务。
//  2. 数据库正被本进程独占（-wal/-shm 都有打开的句柄），Windows 上根本无法复制/删除。
//
// 为什么用 VACUUM INTO：
//  SQLite 自己在事务快照内读出全部页并写成新文件，保证「导出的那一刻」的数据一致，
//  不受 WAL 拆分影响，也不需要源库处于静止状态。

// ExportDatabaseFile 把 srcPath 指向的数据库导出成 dstPath 处的独立数据库文件。
//
// srcPath 可以是被其他进程打开着的数据库（WAL 模式亦可）——这正是本函数的用途：
// 数据目录迁移时，源库正被本进程占用，无法用文件复制绕开。
//
// dstPath 若已存在会被覆盖（SQLite 要求目标文件不存在，故先删除）。
// 源库不存在时返回 nil（视为「无需迁移」，调用方按幂等处理）。
func ExportDatabaseFile(srcPath, dstPath string) error {
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("数据库导出路径不能为空")
	}
	if _, err := os.Stat(srcPath); err != nil {
		if os.IsNotExist(err) {
			return nil // 源库不存在：没有可迁移的数据
		}
		return fmt.Errorf("检查源数据库失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("创建导出目录失败: %w", err)
	}
	// VACUUM INTO 要求目标文件不存在。
	if err := os.Remove(dstPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("移除旧导出文件失败: %w", err)
	}

	// 优先以只读方式打开源库：避免在源目录额外创建 -wal/-shm 文件。
	// 只读连接在 WAL 库上需要能写 -shm，权限不足时会失败 —— 此时回退到读写方式。
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(srcPath)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err == nil {
		conn.SetMaxOpenConns(1)
		if err = vacuumInto(conn, dstPath); err == nil {
			_ = conn.Close()
			return nil
		}
		_ = conn.Close()
	}

	conn2, err2 := sql.Open("sqlite", "file:"+filepath.ToSlash(srcPath)+"?_pragma=busy_timeout(5000)")
	if err2 != nil {
		if err != nil {
			return fmt.Errorf("打开源数据库失败: %w", err)
		}
		return fmt.Errorf("打开源数据库失败: %w", err2)
	}
	defer conn2.Close()
	conn2.SetMaxOpenConns(1)
	if err2 := vacuumInto(conn2, dstPath); err2 != nil {
		return fmt.Errorf("导出数据库失败: %w", err2)
	}
	return nil
}

// vacuumInto 执行 VACUUM INTO 导出。
func vacuumInto(conn *sql.DB, dstPath string) error {
	// VACUUM INTO 的目标路径不支持绑定参数（SQLite 语法限制），
	// 故用带引号的字面量；路径中的单引号按 SQL 规则转义为两个单引号。
	q := "VACUUM INTO " + quoteSQLString(filepath.ToSlash(dstPath))
	if _, err := conn.Exec(q); err != nil {
		return err
	}
	return nil
}

// quoteSQLString 把字符串转成 SQL 单引号字面量。
func quoteSQLString(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '\'')
	for _, r := range s {
		if r == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, r)
	}
	out = append(out, '\'')
	return string(out)
}
