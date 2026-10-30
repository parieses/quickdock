package db

import (
	"fmt"
	"regexp"
	"time"

	"quickdock/internal/logger"
)

// baseTables 所有表结构（首次初始化时全部创建）
// 使用 CREATE TABLE IF NOT EXISTS 确保幂等
//
// **索引不写在这里**：老库的列可能少于建表语句（新增列靠 columnMigrations
// 补），索引一旦引用新列就会在补列之前执行而报 no such column，
// 导致整个迁移失败、应用起不来。索引统一下方 postMigrations。
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
		env TEXT NOT NULL DEFAULT '',
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
		updated_at TEXT NOT NULL,
		env TEXT DEFAULT ''
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

	// 操作流水：谁在什么时候做了什么（打开项目 / 连主机 / 跑插件命令…）。
	// 该表与 snapshots 同批建好但长期没有写入方，属于空置表；启用时补上
	// kind（分类，驱动图标与配色）、ref_id（关联对象 id，供前端跳转）、
	// created_ts（Unix 毫秒，排序与前端直接 new Date）与 source（谁触发的：
	// 用户自己点 / AI 通过 MCP 工具，见 ActivitySource*）。
	`CREATE TABLE IF NOT EXISTS activity (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL DEFAULT '',
		ref_id TEXT NOT NULL DEFAULT '',
		text TEXT NOT NULL,
		created_at TEXT NOT NULL,
		created_ts INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT 'ui'
	)`,
	// 注意：activity 的 created_ts 索引**不能**放这里，见 postMigrations。

	`CREATE TABLE IF NOT EXISTS snapshots (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		label TEXT,
		note TEXT,
		payload TEXT NOT NULL,
		size INTEGER DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	// 删除墓碑（回收站）：collection=主体表名，id=主体 id，label=展示名，
	// payload=恢复载荷 JSON（级联删除时含整棵子树，见 tombstone.go）。
	`CREATE TABLE IF NOT EXISTS tombstones (
		collection TEXT NOT NULL,
		id TEXT NOT NULL,
		deleted_at TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		payload TEXT NOT NULL DEFAULT '',
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
		note TEXT DEFAULT '',
		created_at INTEGER NOT NULL,
		text_type TEXT DEFAULT ''
	)`,

	// 剪贴板自动规则：复制到文本后按优先级做一次转换。
	// 注意表名不是 snippets（历史迁移会删它），也不要和 clipboard_entries 混用。
	`CREATE TABLE IF NOT EXISTS clipboard_rules (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		priority INTEGER NOT NULL DEFAULT 0,
		match_kind TEXT NOT NULL DEFAULT 'all',
		match_expr TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL DEFAULT '',
		action_arg TEXT NOT NULL DEFAULT '',
		write_back INTEGER NOT NULL DEFAULT 0,
		created_ts INTEGER NOT NULL,
		updated_ts INTEGER NOT NULL
	)`,

	// notifications 应用内通知（收口通知中心）。
	// 与系统托盘通知是两条路：托盘那条会消失、不保留历史，这里留档给「回头看」。
	`CREATE TABLE IF NOT EXISTS notifications (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL DEFAULT 'system',
		level TEXT NOT NULL DEFAULT 'info',
		title TEXT NOT NULL DEFAULT '',
		body TEXT NOT NULL DEFAULT '',
		ref_type TEXT NOT NULL DEFAULT '',
		ref_id TEXT NOT NULL DEFAULT '',
		read INTEGER NOT NULL DEFAULT 0,
		created_ts INTEGER NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS notes (
		id TEXT PRIMARY KEY,
		keyword TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		category TEXT DEFAULT '',
		name TEXT DEFAULT '',
		parent_id TEXT DEFAULT '',
		is_folder INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		tags TEXT DEFAULT '',
		is_note INTEGER DEFAULT 0,
		format TEXT DEFAULT 'markdown',
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
		notify_channels TEXT NOT NULL DEFAULT 'system,webhook',
		-- retry_count：定时触发失败后的重试次数（0=不重试）。新增列，老库由
		-- columnMigrations 补上；默认 0 保证升级后行为与之前一致。
		retry_count INTEGER NOT NULL DEFAULT 0,
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
		down_alert_threshold INTEGER NOT NULL DEFAULT 1,
		consecutive_down INTEGER NOT NULL DEFAULT 0,
		group_name TEXT NOT NULL DEFAULT '',
		latency_alert_ms INTEGER NOT NULL DEFAULT 0,
		-- 自定义请求头 / 请求体：POST/PUT/PATCH 类健康检查（带 token 的接口、
		-- 需要提交 JSON 的探针）没有这两列就只能带空 body 发出去，等于方法形同虚设。
		-- headers 存 "Name: Value" 每行一条，便于编辑与人工核对。
		http_headers TEXT NOT NULL DEFAULT '',
		http_body TEXT NOT NULL DEFAULT '',
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

	// ai_spaces 空间：一组任务的容器，绑定一个目录。
	//
	// 为什么要单独一张表而不是把「是不是空间」打在会话上：空间与任务是一对多，
	// 而会话表里没有「父会话」这个概念。用一条 is_space 标记表达不了
	// 「这个空间里有哪几个任务」，最后只能去 LIKE 标题，那不是结构。
	//
	// 目录（cwd）绑在空间上而不是每个任务各存一份：同一个项目下的十几条对话
	// 几乎总是同一个目录，各存一份意味着换目录要改十几行，且必然改漏。
	// 任务自己的 cwd 仍可用（ai_conversations.cwd）——它表示「这条对话临时换了个目录」，
	// 解析时以任务自己的为准，为空才回退到空间。
	`CREATE TABLE IF NOT EXISTS ai_spaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		cwd TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// ai_conversations: cwd 是这个对话绑定的工作目录（用户选的文件夹）；
	// space_id 非空表示它属于某个空间（侧栏收在空间下而不是「任务」组里）；
	// mode 是**创建时就选定**的任务类型（对话/翻译/总结/解释/使用教程），
	// 存库是因为它要跟着这个任务走——切走再回来、甚至重启应用，模式都不该变。
	//
	// 新库直接带这些列，老库靠 columnMigrations 补——两处都要有，漏一边就是
	// 「新装用户能用、老用户升级后列不存在」。
	`CREATE TABLE IF NOT EXISTS ai_conversations (
		id TEXT PRIMARY KEY,
		title TEXT DEFAULT '',
		summary TEXT DEFAULT '',
		cwd TEXT NOT NULL DEFAULT '',
		space_id TEXT NOT NULL DEFAULT '',
		mode TEXT NOT NULL DEFAULT 'chat',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// ai_messages: tool_calls 是这一条回复期间发生的工具调用（JSON 数组，由
	// services/ai 写入，db 只当中转）。tool_rounds 是「用了几轮」——
	// 与 len(tool_calls) 不是一回事：一轮可以并发调多个工具，两者都要留，
	// 否则界面上「调了几轮」只能靠猜。
	`CREATE TABLE IF NOT EXISTS ai_messages (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		content TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		stopped INTEGER NOT NULL DEFAULT 0,
		tool_calls TEXT NOT NULL DEFAULT '',
		tool_rounds INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	// AI 用量明细：**一次模型调用一行**。
	//
	// 为什么不复用 ai_conversations 上的累加列：那是 upsert 口径，只能回答「这个会话一共
	// 用了多少」，答不了「今天花了多少钱」「哪个模型最费」。日期与模型必须跟这一次调用
	// 绑在一起才有意义，而累加列每次都被覆盖成最新值。
	// 删会话时明细不清（conv_id 不建外键约束）：历史花费不该因为清理对话而消失。
	//
	// run_id / node_id 是「多 agent 编排」的成本归集维度：一次 run 会把 token 摊在
	// 多个子 agent 上，只有 conv 维度时答不了「这次编排到底烧了多少」。
	// 归集用独立列而不是塞进 kind —— kind 已经在表达 chat/ask/title/summary 的语义，
	// 两个维度混在一列里，以后既不能按 kind 统计也不能按 run 统计。
	`CREATE TABLE IF NOT EXISTS ai_usage (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		node_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'chat',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	// ── 多 agent 编排的三张表（run / node / event）──────────────────────────
	//
	// 为什么必须进库而不是只放在内存里：一次 run 要能「跑到一半关窗口、重开后
	// 还能看到跑到哪一步」，也要能回答「上周那次定时编排为什么失败」。
	// 只存在内存里的执行树，重启即失忆，且没法做 run 级成本归集。
	//
	// 三级关系的语义：
	//   run   —— 一次编排（一个目标）。子 agent 共享同一个 run，不另起 run。
	//   node  —— run 里的一个执行单元（一个 role 的一次派活），parent_id 表达谁派的谁，
	//            这棵树就是前端要展示的 run 树。
	//   event —— 执行过程中发生的结构化事件（tool/tool_result/notice/error/done），
	//            按 seq 单调递增，供「轮询增量拉取」与事后回放。
	//
	// token / reasoning 这类流式碎片不落 event 表：一次回答能产生上千帧，
	// 落进去只会让表爆炸；完整正文在 ai_messages 里已经有了。
	`CREATE TABLE IF NOT EXISTS agent_runs (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL DEFAULT '',
		goal TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		cwd TEXT NOT NULL DEFAULT '',
		trigger TEXT NOT NULL DEFAULT 'chat',
		budget_tokens INTEGER NOT NULL DEFAULT 0,
		used_tokens INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// agent_nodes: seq 由库分配（同一 run 内 MAX+1），不信任调用方传入——
	// 并发派活时两个节点可能同时拿到「我要第 3 个」，排序就乱了。
	// ON DELETE CASCADE 让「删 run」一步到底，不必记得先删节点再删事件。
	`CREATE TABLE IF NOT EXISTS agent_nodes (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
		parent_id TEXT NOT NULL DEFAULT '',
		seq INTEGER NOT NULL DEFAULT 0,
		role TEXT NOT NULL DEFAULT '',
		task TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		cwd TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL DEFAULT '',
		ended_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS agent_events (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
		node_id TEXT NOT NULL DEFAULT '',
		seq INTEGER NOT NULL DEFAULT 0,
		kind TEXT NOT NULL DEFAULT '',
		payload TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,

	// SSH 主机与会话树节点（文件夹与主机同表，用 is_folder 区分，与 notes 一致）。
	// parent_id / is_folder / sort / env 是 P2「会话树 + 多标签」的前置字段，
	// 现在就加，避免后期对用户库做数据迁移。
	`CREATE TABLE IF NOT EXISTS ssh_hosts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 22,
		user TEXT NOT NULL DEFAULT '',
		auth_type TEXT NOT NULL DEFAULT 'password',
		password TEXT NOT NULL DEFAULT '',
		key_path TEXT NOT NULL DEFAULT '',
		key_passphrase TEXT NOT NULL DEFAULT '',
		legacy_algorithms INTEGER NOT NULL DEFAULT 0,
		env TEXT NOT NULL DEFAULT 'test',
		parent_id TEXT NOT NULL DEFAULT '',
		is_folder INTEGER NOT NULL DEFAULT 0,
		sort INTEGER NOT NULL DEFAULT 0,
		remark TEXT NOT NULL DEFAULT '',
		last_connected_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// 已信任的主机密钥。主键含 key_type：同一台服务器可同时有 ed25519/ecdsa/rsa，
	// 只按 host+port 存一条会导致「协商到另一个算法就误报指纹变更」。
	`CREATE TABLE IF NOT EXISTS ssh_known_hosts (
		host TEXT NOT NULL,
		port INTEGER NOT NULL,
		key_type TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		added_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (host, port, key_type)
	)`,

	// 定时任务执行历史（append-only）。每条执行（自动到点 / 手动「立即运行」）
	// 落一条；任务名做快照，重命名/删除后仍可追溯。保留策略在 InsertScheduleRun 内执行，
	// 避免无限增长：每任务仅留最近 N 条，并清理超过 D 天的记录。
	`CREATE TABLE IF NOT EXISTS schedule_runs (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		task_name TEXT DEFAULT '',
		run_at TEXT DEFAULT '',
		status TEXT DEFAULT '',
		result TEXT DEFAULT '',
		duration_ms INTEGER NOT NULL DEFAULT 0,
		created_ts INTEGER NOT NULL DEFAULT 0
	)`,
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

	// 数据迁移：snippets 表迁移为 notes（幂等；兼容旧版本库）。
	// 旧库可能处于三种状态：
	//   1) 只有 snippets —— 直接 RENAME；
	//   2) snippets 与 notes 并存（重命名中途/重复迁移遗留）—— 把 snippets 中 notes 没有的
	//      id 合并进 notes，再删除 snippets，避免 "already another table named notes"；
	//   3) 只有 notes —— 跳过。
	if d.tableExists("snippets") {
		if d.tableExists("notes") {
			if _, err := d.conn.Exec(`
				INSERT OR IGNORE INTO notes
					(id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at)
				SELECT id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at
				FROM snippets
				WHERE id NOT IN (SELECT id FROM notes)
			`); err != nil {
				return fmt.Errorf("合并 snippets→notes 失败: %w", err)
			}
			if _, err := d.conn.Exec(`DROP TABLE snippets`); err != nil {
				return fmt.Errorf("删除遗留 snippets 表失败: %w", err)
			}
		} else {
			if _, err := d.conn.Exec(`ALTER TABLE snippets RENAME TO notes`); err != nil {
				return fmt.Errorf("重命名 snippets→notes 失败: %w", err)
			}
		}
	}

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
		{"clipboard_entries", "note", "TEXT DEFAULT ''"},
		{"clipboard_entries", "text_type", "TEXT DEFAULT ''"},
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
		// monitors: 误报抑制（连续 N 次失败才告警）
		{"monitors", "down_alert_threshold", "INTEGER NOT NULL DEFAULT 1"},
		{"monitors", "consecutive_down", "INTEGER NOT NULL DEFAULT 0"},
		// monitors: 分组（列表分区展示）+ 延迟告警阈值（0=关闭；
		// 与连续失败阈值构成「连续 N 次失败且延迟>Xms」多条件告警）
		{"monitors", "group_name", "TEXT NOT NULL DEFAULT ''"},
		{"monitors", "latency_alert_ms", "INTEGER NOT NULL DEFAULT 0"},
		// todos: 子任务层级（单层级 checklist）+ 状态字段（status 权威，done 派生）
		{"todos", "parent_id", "TEXT DEFAULT ''"},
		{"todos", "status", "TEXT DEFAULT 'todo'"},
		// ai_messages: reasoning_content（思考过程）
		{"ai_messages", "reasoning_content", "TEXT DEFAULT ''"},
		// ai_messages: images（图片引用 JSON 数组，仅 user 消息；字节落盘不落库）
		{"ai_messages", "images", "TEXT DEFAULT ''"},
		// ai_conversations: token 用量统计
		{"ai_conversations", "prompt_tokens", "INTEGER DEFAULT 0"},
		{"ai_conversations", "completion_tokens", "INTEGER DEFAULT 0"},
		// ai_conversations: 用量统计按模型拆分所需（model=本次使用的模型名，
		// request_count=该会话累计问答次数 —— token 是累加的，光靠 SUM 推不出请求数）
		{"ai_conversations", "model", "TEXT NOT NULL DEFAULT ''"},
		{"ai_conversations", "request_count", "INTEGER NOT NULL DEFAULT 0"},
		// ai_usage: 输入中命中前缀缓存的部分（不含独立单价就是算不准钱的根因）
		{"ai_usage", "cached_tokens", "INTEGER NOT NULL DEFAULT 0"},
		// ai_messages: 单条回复自己那一次调用的用量，用于「回答下方那一行小字」
		// （原实现只有前端内存里的 statMap，刷新或换会话就看不到了）
		{"ai_messages", "model", "TEXT NOT NULL DEFAULT ''"},
		{"ai_messages", "prompt_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_messages", "completion_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"ai_messages", "cached_tokens", "INTEGER NOT NULL DEFAULT 0"},
		// ai_messages: stopped（这条回复被用户中断，内容是半截的）
		// 中断时后端会把已收到的部分落库，但没有这个标记的话，刷新后看到一句
		// 没说完的话，用户分不清「模型就答了这些」还是「我自己点了停止」。
		{"ai_messages", "stopped", "INTEGER NOT NULL DEFAULT 0"},
		// ai_messages: 工具调用记录与轮次（内置 agent 的审计面）
		{"ai_messages", "tool_calls", "TEXT NOT NULL DEFAULT ''"},
		{"ai_messages", "tool_rounds", "INTEGER NOT NULL DEFAULT 0"},
		// ai_conversations: 这个对话绑定的工作目录（用户选的文件夹，绝对路径；空=没绑定）。
		// 存库而不是只放前端内存：换个页面回来、或者重启应用，AI 还得知道自己该在哪个
		// 目录里读文件、改代码。绑在会话上而不是全局设置，是因为同时开两个项目对话是常态。
		{"ai_conversations", "cwd", "TEXT NOT NULL DEFAULT ''"},
		// ai_conversations: 任务归属的空间，以及创建时选定的模式。
		// 老会话 space_id 为空 = 普通任务（侧栏「任务」组）；mode 默认 chat，
		// 与「以前没这个概念时的行为」一致，不需要回填。
		{"ai_conversations", "space_id", "TEXT NOT NULL DEFAULT ''"},
		{"ai_conversations", "mode", "TEXT NOT NULL DEFAULT 'chat'"},
		// notes 笔记树：新增树形/标题/标签字段
		{"notes", "name", "TEXT DEFAULT ''"},
		{"notes", "parent_id", "TEXT DEFAULT ''"},
		{"notes", "is_folder", "INTEGER DEFAULT 0"},
		{"notes", "sort", "INTEGER DEFAULT 0"},
		{"notes", "tags", "TEXT DEFAULT ''"},
		{"notes", "is_note", "INTEGER DEFAULT 0"},
		{"notes", "format", "TEXT DEFAULT 'markdown'"},
		// scenes: 场景绑定的环境服务（JSON 数组：[{"runtime":"mysql","version":"8.4.3"}]）
		{"scenes", "env", "TEXT NOT NULL DEFAULT ''"},
		// items: 条目绑定的运行时版本（JSON 数组，与 scenes.env 同形），
		// 打开条目时把对应版本 bin 前置到子进程 PATH（项目级版本切换）
		{"items", "env", "TEXT DEFAULT ''"},
		// activity: 启用操作流水时补齐的三列（老库里该表只有 id/text/created_at）
		{"activity", "kind", "TEXT NOT NULL DEFAULT ''"},
		{"activity", "ref_id", "TEXT NOT NULL DEFAULT ''"},
		{"activity", "created_ts", "INTEGER NOT NULL DEFAULT 0"},
		// activity: 操作来源（ui=用户自己点 / mcp=AI 通过 MCP 工具触发）。
		// 老库的历史记录没有来源信息，一律补成 ui——它们绝大多数确实是手动操作，
		// 且「无法追溯」比「随便标一个 mcp」安全得多（后者会让人以为 AI 动过手）。
		{"activity", "source", "TEXT NOT NULL DEFAULT 'ui'"},
		// tombstones: 回收站启用时补齐的展示名与恢复载荷（老表只有三元主键）
		{"tombstones", "label", "TEXT NOT NULL DEFAULT ''"},
		{"tombstones", "payload", "TEXT NOT NULL DEFAULT ''"},
		// monitors: POST/PUT/PATCH 探测需要的请求头与请求体
		{"monitors", "http_headers", "TEXT NOT NULL DEFAULT ''"},
		{"monitors", "http_body", "TEXT NOT NULL DEFAULT ''"},
		// monitors: 软删（进回收站，与定时任务一致）+ 维护窗口（暂停检测到某 unix 秒）。
		// 老库删除会直接抹掉，升级后改为打标，留出"误删可恢复"的余地。
		{"monitors", "deleted_at", "TEXT DEFAULT ''"},
		{"monitors", "maintenance_until", "INTEGER NOT NULL DEFAULT 0"},
		// scheduled_tasks: 通知渠道拆成 CSV（system=系统通知 / webhook=机器人）。
		// 老库一律补成「两个都要」，与升级前"notify 一开就双发"的行为一致。
		{"scheduled_tasks", "notify_channels", "TEXT NOT NULL DEFAULT 'system,webhook'"},
		// scheduled_tasks: 失败重试次数（0 = 不重试，与升级前行为一致）。
		{"scheduled_tasks", "retry_count", "INTEGER NOT NULL DEFAULT 0"},
		// ai_usage: 多 agent 编排的成本归集维度（老数据为空 = 不属于任何 run）。
		{"ai_usage", "run_id", "TEXT NOT NULL DEFAULT ''"},
		{"ai_usage", "node_id", "TEXT NOT NULL DEFAULT ''"},
	}
	var addedCols []string
	for _, m := range columnMigrations {
		added, aerr := d.addColumnIfMissing(m.table, m.col, m.colType)
		if aerr != nil {
			return aerr
		}
		if added {
			addedCols = append(addedCols, m.table+"."+m.col)
		}
	}
	// 只有旧库升级才会补齐列：留痕可确认「新版本字段是否真的落到用户库」，
	// 正常启动不产生日志（避免每次开机刷屏）。
	if len(addedCols) > 0 {
		logger.I("[db] 旧库结构升级：补齐 %d 个缺失列 %v", len(addedCols), addedCols)
	}

	// 补列之后才能建的索引。
	//
	// 顺序契约：**所有索引都放这里，不放进 baseTables**。
	// baseTables 里 CREATE TABLE IF NOT EXISTS 对已存在的老表是空操作，
	// 老表的列可能比建表语句少（新增列靠下面的 columnMigrations 补），
	// 索引若紧跟建表语句执行，引用的新列此时还不存在，直接报
	// `SQL logic error: no such column: xxx` 并让整个迁移失败 ——
	// 应用起不来，而日志里只有一句 SQL 报错，完全看不出是顺序问题。
	// v0.0.25 的 activity.created_ts 就是这么崩的。
	postMigrations := []string{
		`CREATE INDEX IF NOT EXISTS idx_items_collection ON items(collection_id)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_created_at ON clipboard_entries(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_text ON clipboard_entries(content_type, text_content)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_image ON clipboard_entries(content_type, image_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_logs_mid_ts ON monitor_logs(monitor_id, checked_ts)`,
		`CREATE INDEX IF NOT EXISTS idx_plugin_exec_logs_ts ON plugin_exec_logs(executed_ts)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_messages_conv ON ai_messages(conv_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_usage_at ON ai_usage(created_at)`,
		// run 级成本归集：按 run 汇总 token 走这条索引（前端不查全表）。
		`CREATE INDEX IF NOT EXISTS idx_ai_usage_run ON ai_usage(run_id)`,
		// 事件增量拉取是「按 run + seq 递增」的固定形态（500ms 轮询），
		// 没有索引就是每次全表扫 + 排序。
		//
		// UNIQUE 而不是普通索引：seq 的唯一性本来由「同事务内 MAX+1」保证，
		// 但那是代码约定，不是数据库事实。加唯一约束后，未来谁把 AppendAgentEvent
		// 改成「先算后插」的并发写法，会在写入那一刻报错，而不是悄悄产生两条 seq=3
		// ——后者表现为「前端轮询偶尔丢一半事件」，几乎无法从现象反推到原因。
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_events_run_seq ON agent_events(run_id, seq)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_nodes_run ON agent_nodes(run_id, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_runs_conv ON agent_runs(conv_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_ssh_hosts_parent ON ssh_hosts(parent_id, sort)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_created_ts ON activity(created_ts DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_schedule_runs_task_ts ON schedule_runs(task_id, created_ts DESC)`,
		// 回收站过期清理按 deleted_at 比较，没索引就是每次全表扫 + 把整份 payload 读一遍。
		`CREATE INDEX IF NOT EXISTS idx_tombstones_deleted_at ON tombstones(deleted_at)`,
	}
	for _, sql := range postMigrations {
		if _, err := d.conn.Exec(sql); err != nil {
			return fmt.Errorf("创建索引失败: %w", err)
		}
	}

	// 数据迁移：AI 用量明细表刚建立时，把老版本累加在会话上的 token 折成一条历史记录。
	//
	// 老数据只有「会话累计」，没有逐次明细，所以一条会话最多折一行、日期取会话最后活跃日、
	// 模型取该会话记下的最后一个 —— 只能保证「总额不丢」，无法还原真实分布。
	// 幂等条件：该会话在 ai_usage 里没有任何行。新版本每次问答都会同时写两边，
	// 所以有明细的会话不会被重复折算。
	if _, err := d.conn.Exec(`
		INSERT INTO ai_usage (id, conv_id, model, kind, prompt_tokens, completion_tokens, created_at)
		SELECT c.id || '-legacy', c.id, COALESCE(c.model, ''), 'legacy',
		       COALESCE(c.prompt_tokens, 0), COALESCE(c.completion_tokens, 0), c.updated_at
		FROM ai_conversations c
		WHERE (COALESCE(c.prompt_tokens, 0) > 0 OR COALESCE(c.completion_tokens, 0) > 0)
		  AND NOT EXISTS (SELECT 1 FROM ai_usage u WHERE u.conv_id = c.id)`); err != nil {
		return fmt.Errorf("折算历史 AI 用量失败: %w", err)
	}

	// 数据迁移：notes 用 keyword 回填 name（幂等；旧 snippets 表已重命名为 notes）
	if _, err := d.conn.Exec(`UPDATE notes SET name = keyword WHERE (name IS NULL OR name = '')`); err != nil {
		return fmt.Errorf("回填 notes.name 失败: %w", err)
	}

	// 数据迁移：待办状态口径归一 —— status 为权威字段，done/completed_at 全部由它派生。
	// 历史 bug：早期版本两字段各自写入（ToggleTodo 只改 done、看板拖拽只改 status），
	// 出现过 status='done' 但 done=0、或 done=1 但 status 为空的行，列表与筛选结果互相矛盾。
	// 三步全幂等，可重复执行：① status 缺失/非法 → 按 done 回填；② 按 status 重算 done；③ completed_at 对齐。
	if _, err := d.conn.Exec(
		`UPDATE todos SET status = CASE WHEN done = 1 THEN 'done' ELSE 'todo' END
		 WHERE status IS NULL OR status NOT IN ('todo','doing','done')`); err != nil {
		return fmt.Errorf("回填待办 status 失败: %w", err)
	}
	if _, err := d.conn.Exec(
		`UPDATE todos SET done = CASE WHEN status = 'done' THEN 1 ELSE 0 END`); err != nil {
		return fmt.Errorf("按 status 重算待办 done 失败: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	if _, err := d.conn.Exec(
		`UPDATE todos SET completed_at = '' WHERE status <> 'done' AND completed_at <> ''`); err != nil {
		return fmt.Errorf("清理待办 completed_at 失败: %w", err)
	}
	if _, err := d.conn.Exec(
		`UPDATE todos SET completed_at = ? WHERE status = 'done' AND (completed_at IS NULL OR completed_at = '')`, now); err != nil {
		return fmt.Errorf("回填待办 completed_at 失败: %w", err)
	}

	// 数据迁移：CMD 终端工具参数 /c → /k，使命令型项目运行后窗口停留，
	// 可见执行结果/报错，避免静默失败（"无感"）。仅匹配旧值，幂等可重复执行。
	if _, err := d.conn.Exec(`UPDATE tools SET args = '/k {{command}}' WHERE path = 'cmd' AND args = '/c {{command}}'`); err != nil {
		return fmt.Errorf("迁移 CMD 工具参数失败: %w", err)
	}

	// 数据迁移：修复图片条目 created_at 缺失。
	// 历史 bug：InsertClipboardImageEntry 新插入时复用了 findAndBumpLocked 返回的 now，
	// 而该值在“未命中（新插入）”时为 0，导致图片 created_at 被写成 0，
	// ORDER BY created_at DESC 时永远垫底，且会被 DeleteExpiredClipboardEntries
	// 按 created_at < cutoff 误删。此处回填 created_at<=0 的行，按 rowid 保留相对顺序，
	// 幂等可重复执行。
	if _, err := d.conn.Exec(
		`UPDATE clipboard_entries SET created_at = ? + rowid WHERE created_at <= 0`,
		time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("回填 clipboard_entries.created_at 失败: %w", err)
	}

	// 数据迁移：为存量文本条目补齐 text_type 智能分类（入库分类的前置补偿）。
	// 新条目在 InsertClipboardEntry 时已写入；这里只补老数据，幂等。
	if err := d.backfillClipboardTextType(); err != nil {
		return fmt.Errorf("回填 clipboard_entries.text_type 失败: %w", err)
	}

	return nil
}

// addColumnIfMissing 为已有表安全新增列（幂等）。
// table/col 均为代码内置常量，非用户输入，故直接拼接 SQL（pragma 表名无法参数化）。
func (d *Database) addColumnIfMissing(table, col, colType string) (bool, error) {
	var count int
	if err := d.conn.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name = ?`, col,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("检查列 %s.%s 失败: %w", table, col, err)
	}
	if count == 0 {
		if err := validateColumnType(colType); err != nil {
			return false, err
		}
		if _, err := d.conn.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + colType); err != nil {
			return false, fmt.Errorf("新增列 %s.%s 失败: %w", table, col, err)
		}
		return true, nil
	}
	return false, nil
}

// tableExists 判断表是否存在（用于幂等迁移，如 snippets→notes 重命名）。
func (d *Database) tableExists(name string) bool {
	var cnt int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&cnt); err != nil {
		return false
	}
	return cnt > 0
}

// validColTypeRE 严格约束 addColumnIfMissing 的 colType，防止 ALTER 语句注入。
// 仅允许：基础类型 + 可选 NOT NULL + 可选 DEFAULT（字符串字面量或数字）。
var validColTypeRE = regexp.MustCompile(`^(INTEGER|TEXT|REAL|BLOB)(\s+NOT\s+NULL)?(\s+DEFAULT\s+('[^']*'|[0-9]+))?$`)

func validateColumnType(colType string) error {
	if !validColTypeRE.MatchString(colType) {
		return fmt.Errorf("非法列类型: %q（仅允许类型/NOT NULL/DEFAULT 组合）", colType)
	}
	return nil
}
