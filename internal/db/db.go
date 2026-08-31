package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// dbConn 接口化 *sql.DB，方便测试替换
type dbConn interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Prepare(query string) (*sql.Stmt, error)
	Begin() (*sql.Tx, error)
	Close() error
}

// 已知表的白名单（所有允许在 SQL 拼接中出现的表名）
var validTables = map[string]bool{
	"workspaces":        true,
	"scenes":            true,
	"collections":       true,
	"items":             true,
	"tools":             true,
	"activity":          true,
	"snapshots":         true,
	"tombstones":        true,
	"app_state":         true,
	"schema_version":    true,
	"clipboard_entries": true,
	"snippets":          true,
	"usage_frecency":    true,
	"todos":             true,
	"scheduled_tasks":   true,
	"monitors":          true,
	"monitor_logs":      true,
	"plugin_exec_logs":  true,
	"ai_conversations":  true,
	"ai_messages":       true,
	"api_requests":      true,
	"http_projects":     true,
	"http_environments": true,
	"http_folders":      true,
	"http_docs":         true,
	"db_connections":    true,
	"http_request_history": true,
	"plugins":           true,
	"plugin_data":       true,
}

// 已知列名的白名单（允许在 SQL 拼接中出现的列名，不含反引号/引号）
var validColumns = map[string]bool{
	"id": true, "name": true, "type": true, "value": true,
	"workspace_id": true, "scene_id": true, "collection_id": true,
	"tool_id": true, "default_tool_id": true,
	"storage": true, "remark": true, "description": true,
	"icon": true, "color": true, "status": true,
	"favorite": true, "unbound": true, "usage_count": true,
	"sort": true, "is_default": true, "is_pinned": true,
	"open_strategy": true, "tool": true,
	"recent": true, "recent_at": true,
	"plugin_id": true, "plugin_data": true,
	"collection": true,
	"working_directory": true, "args": true,
	"path": true, "version": true, "capability": true,
	"permissions": true, "manifest": true, "configurable": true, "built_in": true,
	"installed": true, "enabled": true,
	"kind": true, "label": true, "note": true, "payload": true, "size": true,
	"key": true,
	"text": true, "content_type": true, "text_content": true, "image_path": true,
	"source_app": true,
	"title":       true,
	"summary":     true,
	"conv_id":     true,
	"role":        true,
	"copy_count": true,
	"category": true,
	"keyword":        true,
	"content":        true,
	"reasoning_content": true,
	"image_hash": true,
	"count":      true,
	"prompt_tokens": true, "completion_tokens": true,
	"last_used":  true,
	"created_at": true, "updated_at": true, "deleted_at": true,
	"method": true, "url": true, "headers": true, "body": true,
	"body_type": true, "auth_type": true, "auth_token": true,
	"auth_user": true, "auth_pass": true,
	"project_id": true, "variables": true,
	"parent_id": true, "folder_id": true,
	"db_type": true, "host": true, "port": true, "username": true,
	"password": true, "database": true, "file_path": true,
	// schedule 任务表列
	"working_dir": true, "http_method": true, "http_headers": true, "http_body": true,
	"schedule_kind": true, "run_at": true, "interval_sec": true, "time_of_day": true,
	"weekdays": true, "notify": true, "next_run": true,
	"last_run": true, "last_status": true, "last_result": true,
	// 监控表列
	"timeout_sec": true, "expected_status": true,
	"down_alert_threshold": true, "consecutive_down": true,
	// HTTP 请求历史列
	"status_code": true, "created_ts": true,
	"ok": true,
	// 待办表列
	"done": true, "priority": true, "due_date": true,
	"start_time": true, "end_time": true, "reminder_time": true, "reminder_sent": true,
	"tags": true, "recurrence": true, "completed_at": true,
	// 插件执行日志列
	"command_id": true, "executed_at": true, "executed_ts": true,
	"success": true, "duration_ms": true, "result": true, "error": true, "trigger": true,
	// frecency 附加列
	"input": true,
	// snippets 笔记树列
	"is_folder": true, "is_note": true, "format": true,
}

// validateTable 检查表名是否在白名单中
func validateTable(table string) error {
	if !validTables[table] {
		return fmt.Errorf("非法表名: %s", table)
	}
	return nil
}

// validateColumn 检查列名是否在白名单中
func validateColumn(col string) error {
	if !validColumns[col] {
		return fmt.Errorf("非法列名: %s", col)
	}
	return nil
}

// validateColumns 批量检查列名
func validateColumns(columns []string) error {
	for _, col := range columns {
		if !validColumns[col] {
			return fmt.Errorf("非法列名: %s", col)
		}
	}
	return nil
}

// Database 包装 SQLite 连接，提供互斥锁保护
type Database struct {
	mu   sync.Mutex
	conn dbConn
	path string

	// colCache 表结构列名缓存：避免每次查询都执行 pragma_table_info
	// （orderByClause 每次调用 2 次 pragma，快照/列表高频调用时开销放大）。
	// 仅在 d.mu 持锁时访问，无需额外同步。
	colCache map[string]map[string]bool
}

// Open 创建或打开指定路径的 SQLite 数据库
func Open(path string) (*Database, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	// 所有 PRAGMA 必须在连接后显式执行
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	conn.SetMaxOpenConns(1)

	// 连接级 PRAGMA（SetMaxOpenConns(1) 保证始终同一连接）
	// 显式检查错误，PRAGMA 失败时可能引发外键约束不生效等严重问题
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("设置 WAL 模式失败: %w", err)
	}
	if _, err := conn.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("设置 busy_timeout 失败: %w", err)
	}
	if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("启用外键约束失败: %w", err)
	}

	db := &Database{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	return db, nil
}

// Close 关闭数据库连接
func (d *Database) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn.Close()
}

// Path 返回数据库文件路径
func (d *Database) Path() string {
	return d.path
}

// Execute 执行不返回结果集的 SQL 语句（仅限内部安全调用）
// 注意：此方法直接拼接 SQL，调用方必须保证 SQL 中不包含用户可控的标识符。
func (d *Database) Execute(sqlStr string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(sqlStr)
	return err
}

// ExecuteParams 执行带参数且不返回结果集的 SQL 语句（仅限内部安全调用）
func (d *Database) ExecuteParams(sqlStr string, params []interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(sqlStr, params...)
	return err
}

// hasColumn 通过参数化查询安全检测列是否存在（结果缓存，仅首次查 pragma）
func (d *Database) hasColumn(table, col string) bool {
	// 白名单校验：表名和列名都必须是已知的
	if err := validateTable(table); err != nil {
		return false
	}
	if err := validateColumn(col); err != nil {
		return false
	}
	if d.colCache == nil {
		d.colCache = make(map[string]map[string]bool)
	}
	cols, ok := d.colCache[table]
	if !ok {
		cols = make(map[string]bool)
		rows, err := d.conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil {
					cols[name] = true
				}
			}
		}
		d.colCache[table] = cols
	}
	return cols[col]
}

// orderByClause 根据表是否有 sort/created_at 列生成 ORDER BY 子句
func (d *Database) orderByClause(table string) string {
	if d.hasColumn(table, "sort") && d.hasColumn(table, "created_at") {
		return " ORDER BY sort ASC, created_at ASC"
	} else if d.hasColumn(table, "created_at") {
		return " ORDER BY created_at ASC"
	} else if d.hasColumn(table, "sort") {
		return " ORDER BY sort ASC"
	}
	return ""
}

// ListTable 返回表中所有行（检测可用列排序）
func (d *Database) ListTable(table string) ([]map[string]interface{}, error) {
	if err := validateTable(table); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT * FROM " + table + d.orderByClause(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
}

// whereHasOrderBy 判断 where 条件是否已自带 ORDER BY，避免重复拼接导致语法错误
func whereHasOrderBy(where string) bool {
	return strings.Contains(strings.ToUpper(where), "ORDER BY")
}

// ListTableWhere 返回符合 WHERE 条件的行（检测可用列排序）
// where 参数经 params 参数化，不会导致注入。
// 若 where 已包含 ORDER BY（如 AI 模块自定义排序），则不再追加默认排序。
func (d *Database) ListTableWhere(table, where string, params ...interface{}) ([]map[string]interface{}, error) {
	if err := validateTable(table); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// orderByClause 会访问 colCache，注释明确要求只能在持锁时访问；不能放在加锁前
	orderClause := ""
	if !whereHasOrderBy(where) {
		orderClause = d.orderByClause(table)
	}

	rows, err := d.conn.Query("SELECT * FROM "+table+" WHERE "+where+orderClause, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
}

// Reorder 批量更新表中记录的 sort 值（按 orderedIDs 顺序赋 i*10）。
// 所有记录在同一个事务中更新。table 需在白名单中。
func (d *Database) Reorder(table string, orderedIDs []string) error {
	if err := validateTable(table); err != nil {
		return err
	}
	return d.Transaction(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			if _, err := tx.Exec("UPDATE "+table+" SET sort = ? WHERE id = ?", i*10, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// BulkInsert 批量插入多行
func (d *Database) BulkInsert(table string, rows []map[string]interface{}) error {
	if err := validateTable(table); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, row := range rows {
		placeholders := make([]string, 0, len(row))
		values := make([]interface{}, 0, len(row))

		cols := make([]string, 0, len(row))
		for col := range row {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		for _, col := range cols {
			if err := validateColumn(col); err != nil {
				return err
			}
			placeholders = append(placeholders, "?")
			values = append(values, row[col])
		}

		// 用 ON CONFLICT(id) DO UPDATE SET 替代 INSERT OR REPLACE：
		// OR REPLACE 会在主键冲突时整行替换，若 row map 未包含全部列（如 workspaces 无 sort），
		// 缺失列会被置为默认值（sort=0），造成数据被意外清零。改只更新传入的列。
		colList := strings.Join(cols, ", ")
		placeholderList := strings.Join(placeholders, ", ")
		updateParts := make([]string, 0, len(cols))
		for _, col := range cols {
			if col == "id" {
				continue // 不更新主键自身
			}
			updateParts = append(updateParts, col+" = excluded."+col)
		}
		var query string
		if len(updateParts) == 0 {
			query = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(id) DO NOTHING",
				table, colList, placeholderList)
		} else {
			query = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT(id) DO UPDATE SET %s",
				table, colList, placeholderList, strings.Join(updateParts, ", "))
		}

		if _, err := tx.Exec(query, values...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// QueryOne 返回查询结果的第一行
func (d *Database) QueryOne(query string, params ...interface{}) (map[string]interface{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, sql.ErrNoRows
	}
	return results[0], nil
}

// Query 返回查询结果的所有行
func (d *Database) Query(query string, params ...interface{}) ([]map[string]interface{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
}

// appStateKeyValid 校验 key 是否允许读写 app_state 表，防止覆盖内置配置或误写。
// 只允许已知前缀的 key：theme/locale 等用户设置、plugin_icon_* 插件图标、
// 以及内部服务用的 ai_* / sync_config / notify_webhook 等。
var appStateKeyValid = regexp.MustCompile(`^(theme|locale|lastWorkspaceId|lastSceneId|plugin_icon_|webdav_config|sync_config|ai_profiles|ai_active_profile|ai_config|notify_webhook|qd_)`)

func validateAppStateKey(key string) error {
	if !appStateKeyValid.MatchString(key) {
		return fmt.Errorf("invalid app_state key: %q", key)
	}
	return nil
}

// GetValue 从 app_state 表中读取值（委托 GetSetting，ErrNoRows 时返回空字符串不报错）
func (d *Database) GetValue(key string) (string, error) {
	if err := validateAppStateKey(key); err != nil {
		return "", err
	}
	val, err := d.GetSetting(key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetValue 向 app_state 表中写入值（委托 SetSetting）
func (d *Database) SetValue(key, value string) error {
	if err := validateAppStateKey(key); err != nil {
		return err
	}
	return d.SetSetting(key, value)
}

// CountWhere 返回符合 WHERE 条件的行数
// where 通过 params 参数化，但表名需要白名单校验。
func (d *Database) CountWhere(table, where string, params ...interface{}) (int, error) {
	if err := validateTable(table); err != nil {
		return 0, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var count int
	err := d.conn.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", table, where), params...).Scan(&count)
	return count, err
}

// Transaction 在事务中执行函数 f。如果 f 返回错误，事务回滚；否则提交。
// 在事务期间，数据库的互斥锁保持锁定。
func (d *Database) Transaction(f func(tx *sql.Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // 如果已提交，Rollback 是空操作

	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteWhere 删除符合 WHERE 条件的行
func (d *Database) DeleteWhere(table, where string, params ...interface{}) error {
	if err := validateTable(table); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", table, where), params...)
	return err
}

// scanRows 将 sql.Rows 转为 map 切片
func scanRows(rows *sql.Rows) ([]map[string]interface{}, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		row := make(map[string]interface{}, len(columns))
		for i, col := range columns {
			row[col] = values[i]
		}
		results = append(results, row)
	}

	return results, rows.Err()
}
