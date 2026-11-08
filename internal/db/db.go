package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"

	"modernc.org/sqlite"
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
	"clipboard_rules":   true,
	"notifications":     true,
	"notes":             true,
	"usage_frecency":    true,
	"todos":             true,
	"scheduled_tasks":   true,
	"schedule_runs":     true,
	"monitors":          true,
	"monitor_logs":      true,
	"plugin_exec_logs":  true,
	"ai_conversations":  true,
	"ai_messages":       true,
	"ai_spaces":         true,
	"agent_runs":        true,
	"agent_nodes":       true,
	"agent_events":      true,
	"plugins":           true,
	"plugin_data":       true,
	"ssh_hosts":         true,
	"ssh_known_hosts":   true,
	// AI 提示词库：需要走通用表操作（回收站整行恢复、快照按表遍历），
	// 少了它这里会以「未知表」被拒。
	"ai_prompts": true,
	// 文件置物架：路径清单跟着快照走，换机器不该丢。
	"file_shelf": true,
	// 互链边：要参与快照按表遍历与回收站整行恢复，不登记会被当成「未知表」拒掉。
	"entity_links": true,
	// SSH 命令片段：用户攒的常用命令，跟快照走；不登记会在恢复时整表丢。
	"ssh_commands": true,
	// 专注会话历史：跟快照走，不登记会在恢复时被当成「未知表」拒掉。
	"focus_sessions": true,
	// 知识库：源与切片要能走通用表操作（回收站整行恢复、按表遍历统计）。
	// kb_chunks_fts 是 FTS5 虚拟表，与 items_fts 一样不登记。
	"kb_libraries": true,
	"kb_sources":   true,
	"kb_chunks":    true,
}

// 已知列名的白名单（允许在 SQL 拼接中出现的列名，不含反引号/引号）。
//
// ⚠️ 全局共用（P12）：白名单不是按表分的。structToMap 会把结构体的每个导出字段
// 都转成列，BulkInsert 写入前逐一校验此表。漏掉任何一个被用到的列 → 该表所有插入
// 报「非法列名」。新增结构体字段 / 新增表列时，务必来这里同步补白名单。
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
	"collection":        true,
	"working_directory": true, "args": true,
	"path": true, "version": true, "capability": true,
	"env":          true, // scenes.env：场景绑定的环境服务 JSON
	"jump_host_id": true, // ssh_hosts：跳板机引用的另一台主机 id（空 = 直连）
	"permissions":  true, "manifest": true, "configurable": true, "built_in": true,
	"installed": true, "enabled": true,
	"kind": true, "label": true, "note": true, "payload": true, "size": true,
	"key":  true,
	"text": true, "content_type": true, "text_type": true, "text_content": true, "image_path": true,
	"source_app": true,
	"title":      true,
	"summary":    true,
	"conv_id":    true,
	"role":       true,
	"copy_count": true,
	"category":   true,
	"keyword":    true,
	"content":    true,
	// ai_prompts：内置条目标记与稳定身份（builtin 只影响能否删除，source_key 决定
	// 升级时会不会重复插一份）。白名单全局共用，漏了就是写入时「非法列名」。
	"builtin":           true,
	"source_key":        true,
	"reasoning_content": true,
	"images":            true, // ai_messages：图片引用 JSON 数组（仅 user 消息）
	"cwd":               true, // ai_conversations / ai_spaces：绑定的工作目录
	// ai_conversations：所属空间 id（空 = 普通任务）与创建时选定的模式。
	// 白名单是全局的、不按表分，漏一个就是写入时「非法列名」。
	"space_id": true,
	"starred":  true, // ai_conversations：星标（0/1）
	"mode":     true,
	// ai_conversations：这条对话选中的技能名（JSON 数组）。同样是全局白名单，
	// 漏了它 = 建会话/写选择时「非法列名」，而且回收站恢复整行转插入也会在这一列炸。
	"skills": true,
	// ai_conversations：执行权限（空 = 默认权限，'full' = 完全访问）。同样是全局白名单，
	// 漏了它 = 建会话/切换权限时「非法列名」，回收站恢复整行转插入也会在这一列炸。
	"permission": true,
	// ai_conversations：Worktree 隔离工作区。worktree_path 是这个任务独占的
	// git worktree 目录（空 = 没开），worktree_branch 是它基于的分支名。
	// 同样是全局白名单，漏了它 = 开/关工作区时「非法列名」。
	"worktree_path":   true,
	"worktree_branch": true,
	// ai_messages：工具调用记录与轮次（内置 agent）。白名单是全局的、不按表分，
	// 漏一个就是写入时「非法列名」——而 structToMap 会把零值字段也转成列。
	"tool_calls":    true,
	"tool_rounds":   true,
	"image_hash":    true,
	"count":         true,
	"prompt_tokens": true, "completion_tokens": true,
	"last_used":  true,
	"created_at": true, "updated_at": true, "deleted_at": true,
	// entity_links：全局列名白名单不按表分，漏一个会在「恢复/快照导入」时才报非法列名。
	"src_type": true, "src_id": true, "dst_type": true, "dst_id": true, "dst_label": true,
	"method": true, "url": true, "headers": true, "body": true,
	"body_type": true, "auth_type": true, "auth_token": true,
	"auth_user": true, "auth_pass": true,
	"project_id": true, "variables": true,
	"ref_id":    true, // activity：关联对象 id（项目/主机/插件命令），供前端跳转
	"source":    true, // activity：操作来源（ui=用户自己点 / mcp=AI 通过 MCP 工具触发）
	"parent_id": true, "folder_id": true,
	"db_type": true, "host": true, "port": true, "username": true,
	"password": true, "database": true, "file_path": true,
	// schedule 任务表列
	"working_dir": true, "http_method": true, "http_headers": true, "http_body": true,
	"schedule_kind": true, "run_at": true, "interval_sec": true, "time_of_day": true,
	"weekdays": true, "notify": true, "next_run": true,
	// notify_channels：回收站恢复定时任务时整行都会被转成插入列，
	// 漏它就会在「恢复」这一步报「非法列名: notify_channels」——
	// 报错发生在删除之后，看起来像数据丢了。
	"notify_channels": true,
	// retry_count 同理：回收站恢复定时任务时整行转插入列，漏了就在恢复那一步炸。
	"retry_count": true,
	// cron_expr / day_of_month 同理：回收站恢复 / 快照导入时整行转插入列，
	// 漏一个就在「恢复」那一步报非法列名（此时原记录已经删掉了）。
	"cron_expr": true, "day_of_month": true,
	"last_run": true, "last_status": true, "last_result": true,
	// "action" 已在 clipboard_rules 处声明（白名单全局共用，重复声明无意义）
	// target：回收站恢复定时任务必需——整行 21 个列都会被转成插入列，漏一个就报
	// 「非法列名」，而且报错发生在实际删 Bespoke 定时任务之后的恢复环节，很难联想到这里。
	"target": true,
	// schedule 执行历史表列（run_at/status/created_ts/duration_ms 已在上文声明）
	"task_id": true, "task_name": true,
	// 监控表列
	"timeout_sec": true, "expected_status": true,
	"down_alert_threshold": true, "consecutive_down": true,
	"group_name": true, "latency_alert_ms": true,
	// monitors：告警静默窗口（snapshot 整表导出时会经过 structToMap，
	// 漏这列就会在导出/导入那一步报「非法列名」）
	"mute_until": true,
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
	// notes 笔记树列
	"is_folder": true, "is_note": true, "format": true,
	// notifications 通知中心列（kind / title / body / ref_id 已在上文声明）
	"level": true, "ref_type": true, "read": true,

	// 知识库列。白名单全局共用、不按表分，漏一个就是写入/恢复时「非法列名」。
	// err 与 error 是两个不同的列名（错误表用 error，知识库用 err），不要合并。
	"ext": true, "mtime": true, "hash": true,
	"chunk_count": true, "page_count": true, "err": true,
	"source_id": true, "idx": true, "heading": true, "char_count": true,
	"page": true, "vec": true, "dim": true, "embed_model": true,
	// 知识库分组：library_id 给 kb_sources（icon 在上文 plugins 处已声明，
	// 白名单全局共用，重复声明会编译失败）。sort 同理。
	"library_id": true,

	// clipboard_rules 剪贴板自动规则列（priority 已在上文 todos 处声明）
	"match_kind": true, "match_expr": true,
	"action": true, "action_arg": true, "write_back": true, "updated_ts": true,
	// AI 用量统计列（model 是多张表都可能用到的通用列名，白名单全局共用故只声明一次）
	"model": true, "request_count": true, "cached_tokens": true,
	// stopped：ai_messages 的「这条回复被中断」标记
	"stopped": true,
	// ai_checkpoints 回滚点表列（id / conv_id / seq / tool / created_at 已在上文声明，
	// 白名单全局共用故只补缺的）。漏一个就在「回收站恢复 / 批量导入」那一步报
	// 「非法列名」，而那时已经和「新加了一张表」联想不到一起了。
	"files": true, "tool_call_id": true,
	// ai_approvals 审批明细表列（id / conv_id / tool / created_at 已在上文声明，
	// decision 与 remember 是这张表独有的）
	"decision": true, "remember": true,
	// ai_jobs 后台任务表列（id / cwd / status / error / started_at / ended_at
	// 已在上文声明）。command 与 command_id 不是一个东西：后者是插件命令的 id。
	"command": true, "pid": true, "log_path": true, "exit_code": true,

	// ── 多 agent 编排三表（agent_runs / agent_nodes / agent_events）─────────
	//
	// 这三张表的写入走的是显式列名的参数化 SQL（internal/db/agent_run.go），
	// 不经过 structToMap，所以当前不登记也不会报错。仍然逐条补上：
	// 白名单是全局的，一旦以后有人对这些表用 BulkInsert（例如回收站恢复、
	// 快照导入），漏列就会变成「非法列名」——而那时的报错点在恢复流程里，
	// 和「新加了个字段」完全联想不到一起。
	"goal": true, "budget_tokens": true, "used_tokens": true,
	"task": true, "seq": true, "started_at": true, "ended_at": true,
	// focus_sessions：白名单全局共用，started_at / ended_at / done / title /
	// created_at 上面已有，这里只补本表独有的两列，漏了会在快照恢复时报错。
	"todo_id": true, "minutes": true,
	// run_id / node_id：agent_* 三表与 ai_usage 共用（成本归集维度）
	"run_id": true, "node_id": true,
	// file_shelf 独有的列（id / path / name / kind / sort / created_ts 上文已登记）；
	// 白名单全局共用，漏一个就在快照导入时变成「非法列名」。
	"last_opened_ts": true,
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

// Database 包装 SQLite 连接
//
// 并发模型：WAL 支持「多读 + 单写」，故这里用 RWMutex 而不是全程串行——
// 读方法（Query/ListTable/…）持读锁可并发，写方法（Execute/Transaction/…）持写锁独占。
// 连接池放开到 dbMaxOpenConns，让并发读真正落到不同连接上（WAL 下读写互不阻塞）。
type Database struct {
	mu   sync.RWMutex
	conn dbConn
	path string

	// colCache 表结构列名缓存：避免每次查询都执行 pragma_table_info
	// （orderByClause 每次调用 2 次 pragma，快照/列表高频调用时开销放大）。
	// 读方法只持 mu.RLock 也会写它，因此由 colMu 单独保护，不能依赖 mu。
	colMu    sync.RWMutex
	colCache map[string]map[string]bool
}

// dbMaxOpenConns 连接池上限。写操作被 mu 串行化，同一时刻最多一个写者；
// 读操作可并发，池大小决定并发读的上限（超过则排队，不会失败）。
const dbMaxOpenConns = 4

// WAL 初始化遇到 1546 时的重试参数。
//
// 1546 = SQLITE_IOERR_TRUNCATE（SQLITE_IOERR | 6<<8）。SQLite 3.50.0 起，Windows 上
// 打开 WAL 库的第一个连接拿到 -shm 的 DMS 独占锁后会把它截断为 0；若此时另一个
// 进程仍映射着 -shm（典型：托盘重启时旧实例刚退出，Windows 先放句柄和锁、后解除
// 内存映射），SetEndOfFile 报 ERROR_USER_MAPPED_FILE(1224)，驱动直接返回 1546 而不是
// 等待。上游给的临时方案就是「遇到 1546 重试打开最多 3 秒」，这里照做：不重试的话，
// 这种几十毫秒~几秒的竞态会让 ServiceStartup 直接失败、弹「QuickDock 无法启动」，
// 用户只能手工结束占用进程。
const (
	sqliteIOErrTruncate = 1546
	walRetryTimeout     = 3 * time.Second
	walRetryInterval    = 100 * time.Millisecond
)

// isTransientWALError 只认 1546：其它 IOERR（真磁盘故障、目录不可写、文件损坏）
// 重试也救不回来，应立刻失败并保留原始错误，别把真实故障藏进 3 秒延迟里。
//
// 驱动返回的 *sqlite.Error.Code() 就是扩展码（日志里的 "disk I/O error (1546)" 正是
// 由它格式化出来的）。文本兜底留给 DSN pragma 建连路径：那条路径上错误可能被包一层，
// 拿不到类型时仍能识别，避免重试被静默跳过。
func isTransientWALError(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == sqliteIOErrTruncate
	}
	return strings.Contains(err.Error(), "(1546)")
}

// openWALOnce 建一条连接并设 WAL，失败时返回失败的阶段名供上层拼错误信息。
//
// 为什么每轮都要重新 sql.Open：失败的连接可能已被放回连接池，复用它只会重复命中
// 同一份未释放的 -shm 状态；换一条新连接才会重新走 DMS 锁与截断。DSN 里的
// journal_mode(WAL) 随建连执行，所以重试必须覆盖「建连 + 显式 PRAGMA」整个过程，
// 不能只重试最后那条 Exec。
func openWALOnce(dsn string) (*sql.DB, string, error) {
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "打开数据库", err
	}
	conn.SetMaxOpenConns(dbMaxOpenConns)

	// 显式连通性检查：sql.Open 是惰性的，DSN 错误（如路径特殊字符导致解析失败）
	// 要在这里就暴露，而不是留到第一次业务查询才炸。
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = conn.Close()
		return nil, "设置 WAL 模式", err
	}
	return conn, "", nil
}

// openWithWALRetry 正常路径只走一次；只有 1546 才进入有限重试。
func openWithWALRetry(path, dsn string) (*sql.DB, error) {
	deadline := time.Now().Add(walRetryTimeout)
	for attempt := 1; ; attempt++ {
		conn, stage, err := openWALOnce(dsn)
		if err == nil {
			if attempt > 1 {
				logger.W("[db] WAL 初始化遇到 1546，第 %d 次尝试成功 path=%s", attempt, path)
			}
			return conn, nil
		}
		// 下次等待会越过 deadline 就收手：总等待不超过 walRetryTimeout。
		if !isTransientWALError(err) || time.Now().Add(walRetryInterval).After(deadline) {
			// 非 1546 的错误沿用原来的文案，别让日志格式无谓变化；只有重试耗尽
			// 才补上次数，方便从一条日志看出「等了 3 秒仍被占用」。
			if attempt > 1 {
				return nil, fmt.Errorf("%s失败（1546 重试 %d 次仍失败）: %w", stage, attempt-1, err)
			}
			return nil, fmt.Errorf("%s失败: %w", stage, err)
		}
		if attempt == 1 {
			logger.W("[db] %s遇到 1546（-shm 可能仍被上一实例映射），开始重试 path=%s 最长=%v", stage, path, walRetryTimeout)
		}
		time.Sleep(walRetryInterval)
	}
}

// Open 创建或打开指定路径的 SQLite 数据库
func Open(path string) (*Database, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	// busy_timeout / foreign_keys 是「连接级」PRAGMA，只在执行它的那条连接上生效。
	// 连接池放开后每次新建的连接都是全新的，必须用 DSN 下发，否则新连接会静默丢失
	// 外键约束与写锁等待（后果：外键形同虚设、并发写直接报 SQLITE_BUSY）。
	//
	// synchronous=NORMAL 是 WAL 模式下的官方推荐组合：WAL 已经保证崩溃后数据库
	// 结构完整，NORMAL 只在检查点时才 fsync，单条写不再各自刷盘。剪贴板、监控日志、
	// 活动流水都是「一条 INSERT 一次写事务」，默认 FULL 下每条都付一次磁盘同步。
	// 代价仅是掉电时可能丢最后几条已提交事务（不是损坏），对本地桌面工具可接受。
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	conn, err := openWithWALRetry(path, dsn)
	if err != nil {
		return nil, err
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

// hasColumn 通过参数化查询安全检测列是否存在（结果缓存，仅首次查 pragma）。
// colCache 由 colMu 保护：调用方持 mu 的读锁或写锁都可能进入这里。
func (d *Database) hasColumn(table, col string) bool {
	// 白名单校验：表名和列名都必须是已知的
	if err := validateTable(table); err != nil {
		return false
	}
	if err := validateColumn(col); err != nil {
		return false
	}

	d.colMu.RLock()
	cols, ok := d.colCache[table]
	d.colMu.RUnlock()
	if ok {
		return cols[col]
	}

	d.colMu.Lock()
	defer d.colMu.Unlock()
	if cols, ok = d.colCache[table]; ok { // 双重检查：等锁期间可能已被别的 goroutine 填充
		return cols[col]
	}
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
	if d.colCache == nil {
		d.colCache = make(map[string]map[string]bool)
	}
	d.colCache[table] = cols
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

	d.mu.RLock()
	defer d.mu.RUnlock()

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

	d.mu.RLock()
	defer d.mu.RUnlock()

	// orderByClause 内部按需查 pragma 并写 colCache，该缓存由 colMu 独立保护，
	// 并发读安全；仍放在持锁后计算，避免表结构查询与结果查询之间出现语义空隙。
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
	d.mu.RLock()
	defer d.mu.RUnlock()

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
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanRows(rows)
}

// appStateKeyValid 校验 key 是否允许读写 app_state 表，防止覆盖内置配置或误写。
// 只允许已知前缀的 key：theme/locale 等用户设置、lastWorkspaceId/lastSceneId 工作区记忆、
// openedSceneIds:* 已打开场景标签页（workspace.ts 按 workspaceId 拼接）、plugin_icon_* 插件图标、
// 以及内部服务用的 ai_* / sync_config / notify_webhook / dsh_*（dsh_auto_start 等）/ git_*
// （git_repos 仓库清单等，git 面板专用）/ qd_ 等。
// 注意：后端内部写入走 SetSetting（不经此校验），本白名单只守 SetValue/GetValue 这一前端 API 入口。
var appStateKeyValid = regexp.MustCompile(`^(theme|locale|lastWorkspaceId|lastSceneId|openedSceneIds|plugin_icon_|webdav_config|sync_config|ai_profiles|ai_active_profile|ai_config|notify_webhook|dsh_|git_|qd_)`)

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

	d.mu.RLock()
	defer d.mu.RUnlock()

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
