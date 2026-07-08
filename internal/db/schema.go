package db

import "fmt"

// baseTables 所有表结构（首次初始化时全部创建）
// 使用 CREATE TABLE IF NOT EXISTS 确保幂等
var baseTables = []string{
	`CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		storage TEXT DEFAULT '',
		remark TEXT DEFAULT '',
		sort INTEGER DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS scenes (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		type TEXT DEFAULT '通用',
		description TEXT DEFAULT '',
		icon TEXT DEFAULT '',
		color TEXT DEFAULT '',
		favorite INTEGER DEFAULT 0,
		unbound INTEGER DEFAULT 0,
		usage_count INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS collections (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		scene_id TEXT REFERENCES scenes(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		type TEXT DEFAULT '目录集合',
		description TEXT DEFAULT '',
		default_tool_id TEXT DEFAULT '',
		tool TEXT DEFAULT '',
		icon TEXT DEFAULT '',
		color TEXT DEFAULT '',
		open_strategy TEXT DEFAULT 'single',
		favorite INTEGER DEFAULT 0,
		recent INTEGER DEFAULT 0,
		recent_at TEXT,
		unbound INTEGER DEFAULT 0,
		plugin_id TEXT,
		usage_count INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS items (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		type TEXT DEFAULT '目录',
		value TEXT NOT NULL,
		working_directory TEXT,
		tool_id TEXT,
		tool TEXT DEFAULT '',
		args TEXT,
		icon TEXT DEFAULT '',
		color TEXT DEFAULT '',
		remark TEXT,
		plugin_data TEXT,
		usage_count INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS tools (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		path TEXT NOT NULL,
		args TEXT DEFAULT '',
		is_default INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS activity (
		id TEXT PRIMARY KEY,
		text TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS snapshots (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		label TEXT,
		note TEXT,
		payload TEXT NOT NULL,
		size INTEGER DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS tombstones (
		collection TEXT NOT NULL,
		id TEXT NOT NULL,
		deleted_at TEXT NOT NULL,
		PRIMARY KEY (collection, id)
	)`,

	`CREATE TABLE IF NOT EXISTS app_state (
		key TEXT PRIMARY KEY,
		value TEXT
	)`,

	`CREATE TABLE IF NOT EXISTS clipboard_entries (
		id TEXT PRIMARY KEY,
		content_type TEXT NOT NULL DEFAULT 'text',
		text_content TEXT,
		image_path TEXT,
		image_hash TEXT,
		source_app TEXT DEFAULT '',
		is_pinned INTEGER DEFAULT 0,
		copy_count INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_clipboard_created_at ON clipboard_entries(created_at)`,

	`CREATE TABLE IF NOT EXISTS snippets (
		id TEXT PRIMARY KEY,
		keyword TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		category TEXT DEFAULT '',
		created_at TEXT NOT NULL
	)`,
}

// migrate 执行首次初始化（兼容现有数据库）
// 所有表使用 CREATE TABLE IF NOT EXISTS，安全重复执行
func (d *Database) migrate() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 创建所有表
	for _, sql := range baseTables {
		if _, err := d.conn.Exec(sql); err != nil {
			return fmt.Errorf("创建表失败: %w", err)
		}
	}

	// 安全兜底：检查 clipboard_entries 是否有 copy_count 列
	// （仅对 2026-07-07 之前创建的旧数据库生效）
	var count int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('clipboard_entries') WHERE name = 'copy_count'`).Scan(&count)
	if err == nil && count == 0 {
		_, err = d.conn.Exec(`ALTER TABLE clipboard_entries ADD COLUMN copy_count INTEGER DEFAULT 0`)
	}
	// 安全兜底：检查 image_hash 列
	err = d.conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('clipboard_entries') WHERE name = 'image_hash'`).Scan(&count)
	if err == nil && count == 0 {
		_, err = d.conn.Exec(`ALTER TABLE clipboard_entries ADD COLUMN image_hash TEXT`)
	}

	return err
}
