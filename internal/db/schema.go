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
// 索引不写在这里：老库的列可能少于建表语句（新增列靠 columnMigrations
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
		group_name TEXT DEFAULT '',
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
		group_name TEXT DEFAULT '',
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
	// 注意：activity 的 created_ts 索引不能放这里，见 postMigrations。

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
		-- cron_expr / day_of_month：标准 cron 与「每月几号」的显式字段。老库由
		-- columnMigrations 补列，默认 '' / 0 与升级前行为一致（0 = 沿用旧口径）。
		cron_expr TEXT NOT NULL DEFAULT '',
		day_of_month INTEGER NOT NULL DEFAULT 0,
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
		-- kind/target：探测类型与目标。http 走 url（老列，牵着证书检查与地址校验）；
		-- tcp/process/cmd 走 target。默认 'http' 让老库升级后行为不变。
		kind TEXT NOT NULL DEFAULT 'http',
		target TEXT NOT NULL DEFAULT '',
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
	// mode 是创建时就选定的任务类型（对话/翻译/总结/解释/使用教程），
	// 存库是因为它要跟着这个任务走——切走再回来、甚至重启应用，模式都不该变。
	// skills 是这条对话选中的技能名（JSON 数组，空串 = 从未选过 → 走全局默认）。
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
		skills TEXT NOT NULL DEFAULT '',
		permission TEXT NOT NULL DEFAULT '',
		profile_id TEXT NOT NULL DEFAULT '',
		worktree_path TEXT NOT NULL DEFAULT '',
		worktree_branch TEXT NOT NULL DEFAULT '',
		starred INTEGER NOT NULL DEFAULT 0,
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

	// AI 编辑快照（回滚点）：一次写盘操作一行，文件本体落在数据目录
	// ai_checkpoints/<conv_id>/<seq>/ 下。
	//
	// 为什么文件不进库：一个快照文件动辄几百 KB，塞进 payload 会让这张表比整个库
	// 还大，而那之后 VACUUM 与备份都会变成用户能感知的卡顿。
	//
	// files 是 JSON 数组（见 db.AICheckpointFile）：一条调用现在只改一个文件，
	// 将来批量改多个时不用动表结构。
	`CREATE TABLE IF NOT EXISTS ai_checkpoints (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL DEFAULT '',
		seq INTEGER NOT NULL DEFAULT 0,
		tool_call_id TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		files TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,

	// AI 审批结果明细：一次「停下来问用户」一行。
	//
	// 只记真的问过人的那些（auto 放行不记，否则读文件会把这张表灌满）。
	// 为什么要有它：闸门设得太严时用户的表现是「AI 变笨了」，然后一路点拒绝，
	// 最后把整个功能关掉——只有「拒绝了几次、卡在哪几个工具上」这个数能说明问题，
	// 没有它就只能靠猜。
	`CREATE TABLE IF NOT EXISTS ai_approvals (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL DEFAULT '',
		call_id TEXT NOT NULL DEFAULT '',
		tool TEXT NOT NULL DEFAULT '',
		decision TEXT NOT NULL DEFAULT '',
		remember INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	// AI 后台任务：模型用 run_command(background=true) 起的临时进程。
	//
	// 不并进 scheduled_tasks：那是用户配的定时任务（有调度、有重试、用户看得见），
	// 这是模型为了「跑个 dev server 看看」临时起的东西，生命周期完全不同，
	// 混在一起会让「我配的定时任务呢」在列表里找不着。
	//
	// log_path 指向 <dataDir>/ai_jobs/<id>.log：输出可能很长（一条 build 几十 MB），
	// 塞进库会让备份与 VACUUM 变成用户能感知的卡顿。
	// pid 留着是为了退出时能整组杀掉——它是独立进程组，宿主退出不会带走它。
	`CREATE TABLE IF NOT EXISTS ai_jobs (
		id TEXT PRIMARY KEY,
		conv_id TEXT NOT NULL DEFAULT '',
		command TEXT NOT NULL DEFAULT '',
		cwd TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		exit_code INTEGER NOT NULL DEFAULT 0,
		log_path TEXT NOT NULL DEFAULT '',
		pid INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		ended_at TEXT NOT NULL DEFAULT ''
	)`,

	// AI 用量明细：一次模型调用一行。
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
		-- 跳板机：另一台 ssh_hosts 主机的 id，空串 = 直连。链式可多级，
		-- 环由门面层在保存/连接时挡住（DB 不建外键，删跳板由 DeleteSshHost 解绑）。
		jump_host_id TEXT NOT NULL DEFAULT '',
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

	// AI 提示词库：用户自己的「一句话指令」模板。
	//
	// 为什么不复用 skills：技能是 skills/<名字>/SKILL.md，装 = 把目录拷进去，
	// 作用是让模型知道「这类活有现成套路」，生命周期跟着文件走；
	// 提示词是用户自己的话术，要在界面上增删改、要能带变量（{{date}} / {{selection}}），
	// 点一下把正文填进输入框，生命周期跟着库走（进备份、进快照）。
	// 两者入口也不同：技能是 / 里的胶囊，提示词是 + 面板里的一段正文。
	//
	// source_key 是内置条目的稳定身份：升级时按它判「这条是不是已经有了」。
	// 不用 name 判断——用户改名后就匹配不上，会重复插一份新的。
	// 空串表示用户自建（不受版本影响，也不允许被内置文案覆盖）。
	`CREATE TABLE IF NOT EXISTS ai_prompts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		builtin INTEGER NOT NULL DEFAULT 0,
		source_key TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// 文件置物架：只记路径、不搬文件。kind 是加入时判定的 file/dir，
	// 用于选图标；原文件后来被删也不自动清理，由 service 层 os.Stat 标失效。
	`CREATE TABLE IF NOT EXISTS file_shelf (
		id TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		created_ts INTEGER NOT NULL DEFAULT 0,
		last_opened_ts INTEGER NOT NULL DEFAULT 0
	)`,

	// 知识库分组（多库隔离）。
	//
	// 为什么单独一张表而不是在 kb_sources 上放个字符串分类：分类要能改名、
	// 排序、显示文档数，而且删库时要连带清掉整个库的索引——那是三张表的事务，
	// 没有实体表就没地方挂这个操作。
	//
	// name 允许为空串：默认库用空名字，界面按当前语言显示「默认知识库」，
	// 这样中英文界面不会出现一份写死的「默认知识库」文案。
	// icon 存一个 emoji，仅作视觉区分，不参与逻辑。
	`CREATE TABLE IF NOT EXISTS kb_libraries (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		sort INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// 知识库源文件：只登记路径与指纹，不搬文件本体。
	//
	// 为什么原地引用：用户的文档在自己的目录里持续演进，复制进 dataDir 就等于
	// 制造一份永不更新的死副本。这里存 mtime+size 做快筛、hash 做确认，
	// 变化时按 hash 判要不要重建索引（同一份文件反复「另存」不会重复调 embedding）。
	//
	// status 是索引流水线的状态机：
	//   pending → parsing → chunking → embedding → ready
	//   任一步失败 → failed（err 带原因）；文件不见了 → missing；
	//   换过 embedding 模型 → stale（旧向量与新模型不可比，等用户点重建）
	// 刻意区分 failed 与「向量未建立」：后者源仍是 ready，检索降级为纯关键词。
	//
	// path 仍然是全局 UNIQUE 而不是 (library_id, path)：一份文档只属于一个库。
	// 允许重复登记会让「改了文件、两个库各自重建」产生两份向量，
	// 检索时还会因为 source 去重逻辑把另一份挤掉。
	`CREATE TABLE IF NOT EXISTS kb_sources (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		library_id TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL UNIQUE,
		ext TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		mtime INTEGER NOT NULL DEFAULT 0,
		hash TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		chunk_count INTEGER NOT NULL DEFAULT 0,
		page_count INTEGER NOT NULL DEFAULT 0,
		err TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// 知识库切片：检索的最小单位。
	//
	// vec 是归一化后的 float32 小端数组（dim*4 字节）。归一化之后余弦退化成点积，
	// 检索时省掉每行一次开方——这是纯 Go 无 CGO 环境下唯一可行的向量方案
	// （sqlite-vec 需要加载 C 扩展，modernc 驱动不支持）。
	//
	// embed_model 存在行上而不是全局配置：换模型时按行比对就知道哪些要重算，
	// 也允许分两批用不同模型建索引而不互相污染。
	`CREATE TABLE IF NOT EXISTS kb_chunks (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		idx INTEGER NOT NULL DEFAULT 0,
		heading TEXT NOT NULL DEFAULT '',
		text TEXT NOT NULL DEFAULT '',
		char_count INTEGER NOT NULL DEFAULT 0,
		page INTEGER NOT NULL DEFAULT 0,
		vec BLOB,
		dim INTEGER NOT NULL DEFAULT 0,
		embed_model TEXT NOT NULL DEFAULT '',
		UNIQUE(source_id, idx)
	)`,

	// 条目 / 笔记 / 待办三者的互链边。单独建表而不是在三张表里塞 JSON：
	// 「谁链接了我」是核心体验，行内 JSON 只能全表扫三遍且要双向对称维护。
	// 不在删除主体时级联清理 —— 回收站整行恢复后 id 不变，关联应自动回来；
	// 悬空的边由查询侧按 label 容错展示，不假装目标还在。
	`CREATE TABLE IF NOT EXISTS entity_links (
		id TEXT PRIMARY KEY,
		src_type TEXT NOT NULL,
		src_id TEXT NOT NULL,
		dst_type TEXT NOT NULL,
		dst_id TEXT NOT NULL DEFAULT '',
		dst_label TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'manual',
		created_at TEXT NOT NULL
	)`,

	// SSH 命令片段：用户自己攒的常用命令，跟着快照走。
	// 表名不能叫 snippets —— 上面的迁移会把 snippets 合并/重命名为 notes。
	// 也不往 notes 里塞 category：那会污染笔记列表与 AI 上下文。
	`CREATE TABLE IF NOT EXISTS ssh_commands (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		command TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		usage_count INTEGER NOT NULL DEFAULT 0,
		sort INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// 专注会话：番茄钟此前只把历史写进 localStorage，换设备/清缓存即丢，
	// 也无法做周月统计与「按任务累计耗时」。这里只记「一次专注的结果」，
	// 不记运行中的计时状态 —— 后者仍归前端的 localStorage（切页恢复用），
	// 两者边界刻意分明：数据库里只留已结束、可统计的事实。
	// todo_id / title 在结束时快照：待办之后被删或改名，历史仍读得懂。
	`CREATE TABLE IF NOT EXISTS focus_sessions (
		id TEXT PRIMARY KEY,
		todo_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		minutes INTEGER NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL,
		ended_at TEXT NOT NULL,
		done INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,

	// 下载任务元数据。进度/速度/状态刻意不落库：aria2 是唯一真相源，
	// 双写必然出现「界面 62% 但文件已经下完」这类不一致。
	`CREATE TABLE IF NOT EXISTS dl_tasks (
		gid TEXT PRIMARY KEY,
		url TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		save_dir TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0
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

	// 知识库切片索引。两处和 items_fts 不同，都是刻意的：
	//
	// 1) tokenize='trigram'：unicode61 对中文完全无效——中文没有空格，整句会变成
	//    一个 token，搜「数据库」永远命中不了。trigram 按 3 字滑窗切，中文子串
	//    匹配可用，且零依赖（jieba 那一档的分词器是另一个量级的依赖）。
	//    代价是索引膨胀约 3 倍、查询短于 3 字无结果。
	//
	// 2) 不用 content=''（contentless）：contentless 表删行时必须回传原始文本，
	//    只有 SQLite 3.43+ 且显式开 contentless_delete 才能直接 DELETE。为省一半
	//    空间去赌驱动打包的版本特性不划算——个人规模多存一份正文就是几百 MB。
	//
	// rowid 与 kb_chunks.rowid 对齐，检索时 JOIN 回主表取 vec 与来源信息。
	`CREATE VIRTUAL TABLE IF NOT EXISTS kb_chunks_fts USING fts5(
		text,
		heading,
		tokenize='trigram'
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

	// 内置提示词：只补缺、不覆盖（详见 seedAIPrompts 注释）。
	// 放在建表之后、列补齐之前：它只依赖 ai_prompts 自身的列，与其它表的 ALTER 无关。
	if err := d.seedAIPrompts(); err != nil {
		return fmt.Errorf("写入内置提示词失败: %w", err)
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
		// 后台任务归属会话：老库补成空串（未归属），升级后面板按当前会话过滤时
		// 这些历史行自然不再出现，但行本身与日志都保留，仍由 PurgeAIJobs 到期清理。
		{"ai_jobs", "conv_id", "TEXT NOT NULL DEFAULT ''"},
		{"ssh_hosts", "jump_host_id", "TEXT NOT NULL DEFAULT ''"},
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
		// monitors: 多类型探活（tcp / process / cmd）。默认 'http' 让老库升级后
		// 完全按原行为跑——不加默认值的话老行的 kind 会是 NULL，scan 到空串再归一化也行，
		// 但 SQL 里 `WHERE kind='http'` 这类查询会把老行漏掉，不如在迁移时就定死。
		{"monitors", "kind", "TEXT NOT NULL DEFAULT 'http'"},
		{"monitors", "target", "TEXT NOT NULL DEFAULT ''"},
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
		// ai_conversations: 星标（置顶收藏）。0/1 而不是 TEXT：只在 WHERE/排序里用，
		// 而 SQLite 的布尔就是整数，存 'true' 反而要和前端来回转换。
		{"ai_conversations", "starred", "INTEGER NOT NULL DEFAULT 0"},
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
		// ai_conversations: 这条对话选中的技能名（JSON 数组）。空串 = 从未选过，
		// 走 skills.json 的全局默认——老会话因此保持升级前的行为。
		// 技能做成会话级而不是全局开关：「这一步要用翻译技能」是这条任务的事，
		// 不该让另一个对话也跟着变。
		{"ai_conversations", "skills", "TEXT NOT NULL DEFAULT ''"},
		// ai_conversations: 这条对话的执行权限。空串 = 默认权限（敏感操作逐个确认），
		// 'full' = 用户在这个对话里按过「允许完全访问」。
		//
		// 空串当默认值而不是 'default'：老会话升级后自然是空串，不必回填，
		// 而「默认」本来就该是没写过的样子——多一个要校验的取值只会多一处漏判。
		{"ai_conversations", "permission", "TEXT NOT NULL DEFAULT ''"},
		// ai_conversations: 这条任务选中的模型档案 id。老会话补成空串 = 从未单独
		// 选过 → 继续用全局 ai_active_profile，升级不会把用户已经在用的模型换掉。
		{"ai_conversations", "profile_id", "TEXT NOT NULL DEFAULT ''"},
		// ai_conversations: Worktree 隔离工作区。worktree_path 是这条任务独占的
		// git worktree 目录（开任务时建的，空 = 没开），worktree_branch 记它基于
		// 哪个分支。两列都随会话走：关掉再打开、重启应用，工作区归属都还在。
		{"ai_conversations", "worktree_path", "TEXT NOT NULL DEFAULT ''"},
		{"ai_conversations", "worktree_branch", "TEXT NOT NULL DEFAULT ''"},
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
		// scenes/collections: 侧栏分组（空=未分组）。group 是 SQL 保留字，列名用 group_name。
		{"scenes", "group_name", "TEXT DEFAULT ''"},
		{"collections", "group_name", "TEXT DEFAULT ''"},
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
		// monitors: 告警静默窗口（继续探测、只压推送），与维护窗口互补。
		{"monitors", "mute_until", "INTEGER NOT NULL DEFAULT 0"},
		// scheduled_tasks: 通知渠道拆成 CSV（system=系统通知 / webhook=机器人）。
		// 老库一律补成「两个都要」，与升级前"notify 一开就双发"的行为一致。
		{"scheduled_tasks", "notify_channels", "TEXT NOT NULL DEFAULT 'system,webhook'"},
		// scheduled_tasks: 失败重试次数（0 = 不重试，与升级前行为一致）。
		{"scheduled_tasks", "retry_count", "INTEGER NOT NULL DEFAULT 0"},
		// scheduled_tasks: 标准 cron 表达式 + monthly 的「每月几号」。
		// 存量行拿到 '' / 0，分别代表「不是 cron」与「沿用旧的创建日口径」。
		{"scheduled_tasks", "cron_expr", "TEXT NOT NULL DEFAULT ''"},
		{"scheduled_tasks", "day_of_month", "INTEGER NOT NULL DEFAULT 0"},
		// ai_usage: 多 agent 编排的成本归集维度（老数据为空 = 不属于任何 run）。
		{"ai_usage", "run_id", "TEXT NOT NULL DEFAULT ''"},
		{"ai_usage", "node_id", "TEXT NOT NULL DEFAULT ''"},
		// kb_sources: 知识库分组归属。老库补成空串，由下面的数据迁移统一
		// 归到默认库——注意顺序：补列在前、建索引在后、写数据最后，
		// 挪动任意一步都会在升级路径上炸。
		{"kb_sources", "library_id", "TEXT NOT NULL DEFAULT ''"},
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
	// 顺序契约：所有索引都放这里，不放进 baseTables。
	// baseTables 里 CREATE TABLE IF NOT EXISTS 对已存在的老表是空操作，
	// 老表的列可能比建表语句少（新增列靠下面的 columnMigrations 补），
	// 索引若紧跟建表语句执行，引用的新列此时还不存在，直接报
	// `SQL logic error: no such column: xxx` 并让整个迁移失败 ——
	// 应用起不来，而日志里只有一句 SQL 报错，完全看不出是顺序问题。
	// v0.0.25 的 activity.created_ts 就是这么崩的。
	postMigrations := []string{
		`CREATE INDEX IF NOT EXISTS idx_dl_tasks_created_at ON dl_tasks(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_collection ON items(collection_id)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_created_at ON clipboard_entries(created_at)`,
		// 列表默认排序与「按类型筛选后再排序」是两条固定形态，条目上万后没有索引
		// 就是每次全表扫 + filesort；搜索下推后端后这两条路径都变频繁了。
		`CREATE INDEX IF NOT EXISTS idx_clipboard_order ON clipboard_entries(is_pinned DESC, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_type_order ON clipboard_entries(content_type, text_type, is_pinned DESC, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_text ON clipboard_entries(content_type, text_content)`,
		`CREATE INDEX IF NOT EXISTS idx_clipboard_dedup_image ON clipboard_entries(content_type, image_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_logs_mid_ts ON monitor_logs(monitor_id, checked_ts)`,
		`CREATE INDEX IF NOT EXISTS idx_plugin_exec_logs_ts ON plugin_exec_logs(executed_ts)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_messages_conv ON ai_messages(conv_id, created_at)`,
		// 面板每次打开/轮询都按 conv_id 取当前会话的任务，没有这条就是全表扫。
		`CREATE INDEX IF NOT EXISTS idx_ai_jobs_conv ON ai_jobs(conv_id, started_at DESC)`,
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
		// 删跳板机时要找出「谁引用了它」并解绑，没这条索引就是全表扫。
		`CREATE INDEX IF NOT EXISTS idx_ssh_hosts_jump ON ssh_hosts(jump_host_id)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_created_ts ON activity(created_ts DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_schedule_runs_task_ts ON schedule_runs(task_id, created_ts DESC)`,
		// 回收站过期清理按 deleted_at 比较，没索引就是每次全表扫 + 把整份 payload 读一遍。
		`CREATE INDEX IF NOT EXISTS idx_tombstones_deleted_at ON tombstones(deleted_at)`,
		// 回滚点：列表按 (conv_id, seq DESC) 取，seq 的唯一性同时由这条索引兜住
		// ——CreateAICheckpoint 用 MAX+1 分配编号，撞车时靠约束报错触发重试，
		// 没有唯一约束就会静默产生两条同号回滚点（表现为「回滚到第 3 步」结果不确定）。
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_checkpoints_conv ON ai_checkpoints(conv_id, seq)`,
		// 审批明细按时间区间聚合「哪个工具老是被拦」，日期列必须有索引：
		// 这张表条数不少（一次提问可能问好几次），而统计永远是按天扫。
		`CREATE INDEX IF NOT EXISTS idx_ai_approvals_created ON ai_approvals(created_at)`,
		// 互链：正向列表按 (src_type,src_id) 取，反链按 (dst_type,dst_id) 取；
		// 唯一索引让「同一篇笔记正文里重复写 [[X]]」只落一条边。
		// 命令片段列表按分类分组后按 sort / 使用次数排，没有这条就是全表扫 + filesort。
		`CREATE INDEX IF NOT EXISTS idx_ssh_commands_order ON ssh_commands(category, sort, usage_count DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_links_uniq ON entity_links(src_type,src_id,dst_type,dst_id,dst_label,kind)`,
		`CREATE INDEX IF NOT EXISTS idx_entity_links_dst ON entity_links(dst_type,dst_id)`,
		`CREATE INDEX IF NOT EXISTS idx_entity_links_src ON entity_links(src_type,src_id)`,
		// 专注统计固定按时间区间扫（今日/7 天/30 天），按任务累计则按 todo_id 分组。
		`CREATE INDEX IF NOT EXISTS idx_focus_sessions_started ON focus_sessions(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_focus_sessions_todo ON focus_sessions(todo_id)`,
		// 知识库：切片永远按来源取（重建索引时整份删、预览时整份列），没有这条
		// 就是每次全表扫。source_id+idx 的唯一性已由建表的 UNIQUE 约束保证。
		`CREATE INDEX IF NOT EXISTS idx_kb_chunks_source ON kb_chunks(source_id, idx)`,
		// 向量补齐靠「vec IS NULL」找待办，扫描全库时按 embed_model 判是否过期。
		`CREATE INDEX IF NOT EXISTS idx_kb_chunks_vec ON kb_chunks(embed_model)`,
		`CREATE INDEX IF NOT EXISTS idx_kb_sources_status ON kb_sources(status)`,
		// 分组视图按 library_id 过滤，删除知识库时也靠它找「这个库有哪些源」。
		`CREATE INDEX IF NOT EXISTS idx_kb_sources_library ON kb_sources(library_id, name)`,
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

	// 数据迁移：知识库分组。
	//
	// 两步都幂等：① 只在 kb_libraries 为空时建默认库——用户把库全删了再启动
	// 不会莫名其妙多出一个空库；② 只补 library_id 为空的行，已经是别的库的
	// 源不会被抢回来。默认库的 name 故意留空，界面按当前语言显示，
	// 免得英文界面里冒出一个写死的中文库名。
	if _, err := d.conn.Exec(`INSERT INTO kb_libraries (id, name, icon, sort, created_at, updated_at)
		SELECT ?, '', '📚', 0, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM kb_libraries)`,
		KBLibraryDefaultID, now, now); err != nil {
		return fmt.Errorf("初始化知识库默认分组失败: %w", err)
	}
	if _, err := d.conn.Exec(`UPDATE kb_sources SET library_id = ?
		WHERE library_id IS NULL OR library_id = ''`, KBLibraryDefaultID); err != nil {
		return fmt.Errorf("回填知识库分组失败: %w", err)
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
