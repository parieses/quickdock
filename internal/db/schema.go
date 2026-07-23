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
	`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_text ON clipboard_entries(content_type, text_content)`,
	`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_image ON clipboard_entries(content_type, image_hash)`,

	`CREATE TABLE IF NOT EXISTS snippets (
		id TEXT PRIMARY KEY,
		keyword TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		category TEXT DEFAULT '',
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS plugins (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		author TEXT DEFAULT '',
		description TEXT DEFAULT '',
		category TEXT DEFAULT '',
		icon TEXT DEFAULT '',
		enabled INTEGER DEFAULT 1,
		usage_count INTEGER DEFAULT 0,
		capabilities TEXT DEFAULT '[]',
		permissions TEXT DEFAULT '{}',
		config TEXT DEFAULT '{}',
		installed_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS plugin_data (
		plugin_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (plugin_id, key)
	)`,

	`CREATE TABLE IF NOT EXISTS usage_frecency (
		key TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT '',
		label TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		count INTEGER NOT NULL DEFAULT 1,
		last_used TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS todos (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		done INTEGER NOT NULL DEFAULT 0,
		priority TEXT NOT NULL DEFAULT 'none',
		due_date TEXT DEFAULT '',
		note TEXT DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		completed_at TEXT DEFAULT ''
	)`,

	`CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		action TEXT NOT NULL DEFAULT 'app',
		target TEXT NOT NULL DEFAULT '',
		working_dir TEXT DEFAULT '',
		http_method TEXT DEFAULT 'GET',
		http_headers TEXT DEFAULT '',
		http_body TEXT DEFAULT '',
		schedule_kind TEXT NOT NULL DEFAULT 'once',
		run_at TEXT DEFAULT '',
		interval_sec INTEGER NOT NULL DEFAULT 0,
		time_of_day TEXT DEFAULT '',
		weekdays TEXT DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		notify INTEGER NOT NULL DEFAULT 1,
		next_run TEXT DEFAULT '',
		last_run TEXT DEFAULT '',
		last_status TEXT DEFAULT '',
		last_result TEXT DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS monitors (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		url TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT 'GET',
		interval_sec INTEGER NOT NULL DEFAULT 60,
		timeout_sec INTEGER NOT NULL DEFAULT 10,
		expected_status TEXT NOT NULL DEFAULT '2xx',
		follow_redirects INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		notify_down INTEGER NOT NULL DEFAULT 1,
		notify_up INTEGER NOT NULL DEFAULT 1,
		last_status TEXT DEFAULT '',
		last_checked_at TEXT DEFAULT '',
		last_checked_ts INTEGER NOT NULL DEFAULT 0,
		last_latency_ms INTEGER NOT NULL DEFAULT 0,
		last_status_code INTEGER NOT NULL DEFAULT 0,
		last_error TEXT DEFAULT '',
		skip_tls_verify INTEGER NOT NULL DEFAULT 0,
		cert_warn_days INTEGER NOT NULL DEFAULT 14,
		cert_expires_at INTEGER NOT NULL DEFAULT 0,
		last_cert_warned INTEGER NOT NULL DEFAULT 0,
		content_match_type TEXT NOT NULL DEFAULT 'none',
		content_match_pattern TEXT NOT NULL DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS monitor_logs (
		id TEXT PRIMARY KEY,
		monitor_id TEXT NOT NULL,
		checked_at TEXT DEFAULT '',
		checked_ts INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'down',
		status_code INTEGER NOT NULL DEFAULT 0,
		latency_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_monitor_logs_mid_ts ON monitor_logs(monitor_id, checked_ts)`,

	`CREATE TABLE IF NOT EXISTS plugin_exec_logs (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		command_id TEXT NOT NULL,
		executed_at TEXT DEFAULT '',
		executed_ts INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		result TEXT DEFAULT '',
		error TEXT DEFAULT '',
		trigger TEXT DEFAULT 'manual'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_plugin_exec_logs_ts ON plugin_exec_logs(executed_ts)`,

	`CREATE TABLE IF NOT EXISTS ai_conversations (
		id TEXT PRIMARY KEY,
		title TEXT DEFAULT '',
		summary TEXT DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS ai_messages (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		content TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_messages_conv ON ai_messages(conv_id, created_at)`,
}

// ftsTables FTS5 全文索引（虚拟表，必须用 CREATE VIRTUAL TABLE）
var ftsTables = []string{
	`CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(
		id UNINDEXED,
		name,
		value,
		content='items',
		content_rowid='rowid',
		tokenize='unicode61'
	)`,
}

// ftsTriggers 同步 items ↔ items_fts 的触发器
var ftsTriggers = []string{
	`CREATE TRIGGER IF NOT EXISTS items_ai AFTER INSERT ON items BEGIN
		INSERT INTO items_fts(rowid, id, name, value) VALUES (new.rowid, new.id, new.name, new.value);
	END`,
	`CREATE TRIGGER IF NOT EXISTS items_ad AFTER DELETE ON items BEGIN
		INSERT INTO items_fts(items_fts, rowid, id, name, value) VALUES ('delete', old.rowid, old.id, old.name, old.value);
	END`,
	`CREATE TRIGGER IF NOT EXISTS items_au AFTER UPDATE ON items BEGIN
		INSERT INTO items_fts(items_fts, rowid, id, name, value) VALUES ('delete', old.rowid, old.id, old.name, old.value);
		INSERT INTO items_fts(rowid, id, name, value) VALUES (new.rowid, new.id, new.name, new.value);
	END`,
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

	// 创建 FTS5 虚拟表
	for _, sql := range ftsTables {
		if _, err := d.conn.Exec(sql); err != nil {
			return fmt.Errorf("FTS5 创建失败: %w", err)
		}
	}

	// 创建 FTS5 同步触发器
	for _, sql := range ftsTriggers {
		if _, err := d.conn.Exec(sql); err != nil {
			return fmt.Errorf("FTS5 触发器创建失败: %w", err)
		}
	}

	// 填充已有的 items 数据到 FTS5 索引
	d.conn.Exec(`INSERT INTO items_fts(rowid, id, name, value)
		SELECT rowid, id, name, value FROM items
		WHERE NOT EXISTS (SELECT 1 FROM items_fts WHERE items_fts.rowid = items.rowid)`)

	// 安全兜底：为旧数据库补齐新增列。
	// 使用统一 helper，任何一步的"检查列"或"ALTER"失败都立即返回，
	// 避免错误被后续步骤覆盖（原实现共用一个 err 变量，前一步错误会被后一步
	// 成功的查询覆盖，导致不完整的 schema 被当作迁移成功）。
	type colMig struct {
		table, col, colType string
	}
	columnMigrations := []colMig{
		{"clipboard_entries", "copy_count", "INTEGER DEFAULT 0"},
		{"clipboard_entries", "image_hash", "TEXT"},
		{"plugins", "installed_at", "TEXT NOT NULL DEFAULT ''"},
		{"plugins", "updated_at", "TEXT NOT NULL DEFAULT ''"},
		{"plugins", "category", "TEXT DEFAULT ''"},
		{"plugins", "capabilities", "TEXT DEFAULT '[]'"},
		{"plugins", "permissions", "TEXT DEFAULT '{}'"},
		{"plugins", "icon", "TEXT DEFAULT ''"},
		{"plugins", "usage_count", "INTEGER DEFAULT 0"},
		{"usage_frecency", "type", "TEXT NOT NULL DEFAULT ''"},
		{"usage_frecency", "label", "TEXT NOT NULL DEFAULT ''"},
		{"usage_frecency", "description", "TEXT NOT NULL DEFAULT ''"},
		{"usage_frecency", "input", "TEXT NOT NULL DEFAULT ''"},
		{"todos", "start_time", "TEXT DEFAULT ''"},
		{"todos", "end_time", "TEXT DEFAULT ''"},
		{"todos", "reminder_time", "TEXT DEFAULT ''"},
		{"todos", "reminder_sent", "INTEGER DEFAULT 0"},
		// todos: 标签系统 + 重复任务
		{"todos", "tags", "TEXT DEFAULT ''"},
		{"todos", "recurrence", "TEXT DEFAULT ''"},
		// monitors: 旧表只有 last_checked_at TEXT，新增 INTEGER 列供精确计算间隔
		{"monitors", "last_checked_ts", "INTEGER NOT NULL DEFAULT 0"},
		{"monitors", "last_checked_at", "TEXT DEFAULT ''"},
	{"monitors", "skip_tls_verify", "INTEGER NOT NULL DEFAULT 0"},
	// monitors: SSL 证书到期提醒 + 内容匹配检测
	{"monitors", "cert_warn_days", "INTEGER NOT NULL DEFAULT 14"},
	{"monitors", "cert_expires_at", "INTEGER NOT NULL DEFAULT 0"},
	{"monitors", "last_cert_warned", "INTEGER NOT NULL DEFAULT 0"},
	{"monitors", "content_match_type", "TEXT NOT NULL DEFAULT 'none'"},
	{"monitors", "content_match_pattern", "TEXT NOT NULL DEFAULT ''"},
	// todos: 子任务层级（单层级 checklist）+ 状态字段（status 权威，done 派生）
	{"todos", "parent_id", "TEXT DEFAULT ''"},
	{"todos", "status", "TEXT DEFAULT 'todo'"},
	// ai_messages: reasoning_content（思考过程）
	{"ai_messages", "reasoning_content", "TEXT DEFAULT ''"},
	// ai_conversations: token 用量统计
	{"ai_conversations", "prompt_tokens", "INTEGER DEFAULT 0"},
	{"ai_conversations", "completion_tokens", "INTEGER DEFAULT 0"},
}
	for _, m := range columnMigrations {
		if err := d.addColumnIfMissing(m.table, m.col, m.colType); err != nil {
			return err
		}
	}

	// 数据迁移：已有 completed(done=1) 待办同步 status='done'（status 为权威字段，done 派生）
	if _, err := d.conn.Exec(`UPDATE todos SET status = 'done' WHERE done = 1 AND (status IS NULL OR status = '')`); err != nil {
		return fmt.Errorf("同步待办 status 失败: %w", err)
	}

	// 数据迁移：CMD 终端工具参数 /c → /k，使命令型项目运行后窗口停留，
	// 可见执行结果/报错，避免静默失败（"无感"）。仅匹配旧值，幂等可重复执行。
	if _, err := d.conn.Exec(`UPDATE tools SET args = '/k {{command}}' WHERE path = 'cmd' AND args = '/c {{command}}'`); err != nil {
		return fmt.Errorf("迁移 CMD 工具参数失败: %w", err)
	}

	return nil
}

// addColumnIfMissing 为已有表安全新增列（幂等）。
// table/col 均为代码内置常量，非用户输入，故直接拼接 SQL（pragma 表名无法参数化）。
func (d *Database) addColumnIfMissing(table, col, colType string) error {
	var count int
	if err := d.conn.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('` + table + `') WHERE name = ?`, col,
	).Scan(&count); err != nil {
		return fmt.Errorf("检查列 %s.%s 失败: %w", table, col, err)
	}
	if count == 0 {
		if _, err := d.conn.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + colType); err != nil {
			return fmt.Errorf("新增列 %s.%s 失败: %w", table, col, err)
		}
	}
	return nil
}
