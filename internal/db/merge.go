package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// 数据库文件级合并（跨机导入用）。
//
// 与 ExportDatabaseFile（VACUUM INTO）配对：导出端产出一份自包含快照，
// 导入端把那份快照里的行**并进**当前库，而不是替换当前库。
//
// 为什么是合并而不是替换：
//   - 用户导入的是「另一台机器上的数据」，本机已有的条目没有任何理由消失；
//   - 替换还要改数据目录、重启，而合并只动行，当前会话立即可见。
//
// 冲突策略：
//   - 主键相同 → 保留本地那份（INSERT OR IGNORE）。同一条记录在两边都改过时，
//     本地是用户眼前正在用的版本，导入的那份是历史。
//   - 列只取两边都有的交集：包可能来自略旧/略新的版本，缺列用本地默认值，
//     多出来的列直接忽略。整行照抄会因为版本差一列而整表失败。

// mergeSkipTables 不参与合并的表。每一条都有具体理由，不是「先跳过等以后再说」：
//
//	app_state        机器绑定：cred_dek（DPAPI 包裹的密码库主密钥）、云同步/WebDAV
//	                 的加密口令、启动锁哈希。搬到另一台机器解不开，覆盖过来还会
//	                 把本机配置直接弄坏。
//	snapshots        自我嵌套（里面是别的表的 JSON），且量大。
//	activity         操作日志，合并过来只是噪声。
//	monitor_logs     监控历史，同上。
//	plugin_exec_logs 插件执行日志，同上。
//	clipboard_entries 剪贴板历史：隐私且量大，不该因为导入数据就带过来。
//	dl_tasks         下载任务的 gid 与磁盘上的 .aria2 控制文件绑定，换机即失效。
//	credentials      密文由本机 DPAPI 的 DEK 加密，逐行搬过来也解不开——
//	                 密码库走独立的便携包（口令加密），见 internal/credentials。
var mergeSkipTables = map[string]bool{
	"app_state":         true,
	"snapshots":         true,
	"activity":          true,
	"monitor_logs":      true,
	"plugin_exec_logs":  true,
	"clipboard_entries": true,
	"dl_tasks":          true,
	"credentials":       true,
}

// MergeStats 合并结果（前端展示「并进来多少」）。
type MergeStats struct {
	Tables int
	Rows   int
	// Skipped 被跳过的表名（见 mergeSkipTables），仅用于日志。
	Skipped []string
}

// MergeDatabaseFile 把 importPath 指向的数据库合并进 srcPath 指向的当前库。
//
// srcPath 正被本进程使用（WAL 模式）：另开一个连接写同一个文件是 SQLite 支持的，
// busy_timeout 用来跳过短时的写锁竞争。调用方应在合并后让界面重新加载数据。
func MergeDatabaseFile(srcPath, importPath string) (MergeStats, error) {
	var st MergeStats
	if srcPath == "" || importPath == "" {
		return st, fmt.Errorf("数据库合并路径不能为空")
	}

	// 与 ExportDatabaseFile 同一套 DSN 写法：正斜杠路径，busy_timeout 兜住瞬时锁。
	dsn := "file:" + filepath.ToSlash(srcPath) + "?_pragma=busy_timeout(10000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return st, fmt.Errorf("打开当前数据库失败: %w", err)
	}
	defer conn.Close()
	// 单连接：ATTACH + 事务都绑在这一个连接上，池里换连接会让 ATTACH 失效。
	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec("ATTACH DATABASE ? AS imp", filepath.ToSlash(importPath)); err != nil {
		return st, fmt.Errorf("挂载导入包数据库失败: %w", err)
	}
	defer func() { _, _ = conn.Exec("DETACH DATABASE imp") }()

	names, err := tableNames(conn, "imp")
	if err != nil {
		return st, err
	}

	tx, err := conn.Begin()
	if err != nil {
		return st, fmt.Errorf("开启合并事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, name := range names {
		if mergeSkipTables[name] {
			st.Skipped = append(st.Skipped, name)
			continue
		}
		// 元数据查询必须走 tx，不能走 conn：ATTACH 是连接级的，所以这里限定
		// SetMaxOpenConns(1)，而事务已经占住那唯一一个连接——此时再 conn.Query
		// 会去池里等一个永远不会归还的连接，整个合并直接卡死（实测卡满 10 分钟
		// 被 go test 超时杀掉，栈停在 Tx.awaitDone）。
		ok, err := tableExists(tx, "main", name)
		if err != nil || !ok {
			continue
		}
		cols, err := commonColumns(tx, name)
		if err != nil || len(cols) == 0 {
			continue
		}
		q := buildMergeSQL(name, cols)
		res, err := tx.Exec(q)
		if err != nil {
			// 单表失败不该让整次导入作废：记进 Skipped 继续下一张。
			// （典型原因：包来自旧版本，某列有本地没有的 NOT NULL 无默认值约束）
			st.Skipped = append(st.Skipped, name+":"+shortErr(err))
			continue
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			st.Rows += int(n)
		}
		st.Tables++
	}
	if err := tx.Commit(); err != nil {
		return st, fmt.Errorf("提交合并失败: %w", err)
	}
	return st, nil
}

// rowQueryer 只读查询的最小接口：*sql.DB 与 *sql.Tx 都满足。
//
// 存在的理由很具体：ATTACH 是连接级的，合并期间必须把池锁死在 1 个连接上，
// 而事务会占住那唯一连接——所以事务开始之后的探测查询只能走 tx 自己，
// 否则就是在池里等一个永远不会归还的连接（死锁，不是慢）。
type rowQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// tableNames 列出某个 schema 下的用户表。
func tableNames(q rowQueryer, schema string) ([]string, error) {
	stmt := fmt.Sprintf(
		"SELECT name FROM %s.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%%' ORDER BY name",
		quoteIdent(schema),
	)
	rows, err := q.Query(stmt)
	if err != nil {
		return nil, fmt.Errorf("读取导入包表清单失败: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func tableExists(q rowQueryer, schema, table string) (bool, error) {
	stmt := fmt.Sprintf(
		"SELECT 1 FROM %s.sqlite_master WHERE type='table' AND name = ?",
		quoteIdent(schema),
	)
	var one int
	err := q.QueryRow(stmt, table).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// commonColumns 两张同名表都存在的列，按导入端顺序返回。
func commonColumns(q rowQueryer, table string) ([]string, error) {
	impCols, err := pragmaColumns(q, "imp", table)
	if err != nil {
		return nil, err
	}
	mainCols, err := pragmaColumns(q, "main", table)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(mainCols))
	for _, c := range mainCols {
		have[c] = true
	}
	out := make([]string, 0, len(impCols))
	for _, c := range impCols {
		if have[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

func pragmaColumns(q rowQueryer, schema, table string) ([]string, error) {
	// PRAGMA 不支持绑定参数，表名走标识符转义。
	stmt := fmt.Sprintf("PRAGMA %s.table_info(%s)", quoteIdent(schema), quoteSQLString(table))
	rows, err := q.Query(stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func buildMergeSQL(table string, cols []string) string {
	quoted := make([]string, 0, len(cols))
	for _, c := range cols {
		quoted = append(quoted, quoteIdent(c))
	}
	list := strings.Join(quoted, ", ")
	return fmt.Sprintf(
		"INSERT OR IGNORE INTO main.%s (%s) SELECT %s FROM imp.%s",
		quoteIdent(table), list, list, quoteIdent(table),
	)
}

// quoteIdent 双引号包裹标识符（内部的 " 按 SQL 规则写成两个）。
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// shortErr 错误摘要，避免把一整行 SQL 塞进进度提示。
func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i > 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}
