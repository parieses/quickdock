package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// dbConn 接口化 *sql.DB，允许包装日志拦截器
type dbConn interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
	Prepare(query string) (*sql.Stmt, error)
	Begin() (*sql.Tx, error)
	Close() error
}

// sqlLogger 包装 *sql.DB 并在执行前打印 SQL
type sqlLogger struct {
	inner *sql.DB
}

func (l *sqlLogger) Exec(query string, args ...interface{}) (sql.Result, error) {
	logSQL(query, args...)
	return l.inner.Exec(query, args...)
}

func (l *sqlLogger) Query(query string, args ...interface{}) (*sql.Rows, error) {
	logSQL(query, args...)
	return l.inner.Query(query, args...)
}

func (l *sqlLogger) QueryRow(query string, args ...interface{}) *sql.Row {
	logSQL(query, args...)
	return l.inner.QueryRow(query, args...)
}

func (l *sqlLogger) Prepare(query string) (*sql.Stmt, error) {
	logSQL(query)
	return l.inner.Prepare(query)
}

func (l *sqlLogger) Begin() (*sql.Tx, error) {
	fmt.Println("[SQL] BEGIN TRANSACTION")
	return l.inner.Begin()
}

func (l *sqlLogger) Close() error {
	return l.inner.Close()
}

// logSQL 打印 SQL 语句和参数
func logSQL(query string, args ...interface{}) {
	if len(args) > 0 {
		fmt.Printf("[SQL] %s | args: %v\n", query, args)
	} else {
		fmt.Printf("[SQL] %s\n", query)
	}
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
	"copy_count": true,
	"category": true,
	"keyword":    true,
	"content":    true,
	"image_hash": true,
	"created_at": true, "updated_at": true, "deleted_at": true,
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

	db := &Database{conn: &sqlLogger{inner: conn}, path: path}
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

// hasColumn 通过参数化查询安全检测列是否存在
func (d *Database) hasColumn(table, col string) bool {
	// 白名单校验：表名和列名都必须是已知的
	if err := validateTable(table); err != nil {
		return false
	}
	if err := validateColumn(col); err != nil {
		return false
	}
	var count int
	d.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&count)
	return count > 0
}

// ListTable 返回表中所有行（检测可用列排序）
func (d *Database) ListTable(table string) ([]map[string]interface{}, error) {
	if err := validateTable(table); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	orderBy := ""
	if d.hasColumn(table, "sort") && d.hasColumn(table, "created_at") {
		orderBy = " ORDER BY sort ASC, created_at ASC"
	} else if d.hasColumn(table, "created_at") {
		orderBy = " ORDER BY created_at ASC"
	} else if d.hasColumn(table, "sort") {
		orderBy = " ORDER BY sort ASC"
	}

	rows, err := d.conn.Query("SELECT * FROM " + table + orderBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
}

// ListTableWhere 返回符合 WHERE 条件的行（检测可用列排序）
// where 参数经 params 参数化，不会导致注入。
func (d *Database) ListTableWhere(table, where string, params ...interface{}) ([]map[string]interface{}, error) {
	if err := validateTable(table); err != nil {
		return nil, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	orderBy := ""
	if d.hasColumn(table, "sort") && d.hasColumn(table, "created_at") {
		orderBy = " ORDER BY sort ASC, created_at ASC"
	} else if d.hasColumn(table, "created_at") {
		orderBy = " ORDER BY created_at ASC"
	} else if d.hasColumn(table, "sort") {
		orderBy = " ORDER BY sort ASC"
	}

	rows, err := d.conn.Query("SELECT * FROM "+table+" WHERE "+where+orderBy, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
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
		columns := make([]string, 0, len(row))
		placeholders := make([]string, 0, len(row))
		values := make([]interface{}, 0, len(row))

		for col, val := range row {
			if err := validateColumn(col); err != nil {
				return err
			}
			columns = append(columns, col)
			placeholders = append(placeholders, "?")
			values = append(values, val)
		}

		query := fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) VALUES (%s)",
			table, joinStrings(columns, ", "), joinStrings(placeholders, ", "))

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
		return nil, fmt.Errorf("无结果")
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

// GetValue 从 app_state 表中读取值
func (d *Database) GetValue(key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var value string
	err := d.conn.QueryRow("SELECT value FROM app_state WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetValue 向 app_state 表中写入值
func (d *Database) SetValue(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("INSERT OR REPLACE INTO app_state (key, value) VALUES (?, ?)", key, value)
	return err
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

// joinStrings 拼接字符串切片
func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
