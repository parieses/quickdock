// Package migration 定义「导航菜单 → 数据范围」的映射。
//
// 整机迁移包现在支持按用户勾选的导航菜单打包：每个导航项对应一组 SQLite 表、
// 数据目录下的子目录，以及是否需要携带密码库便携包。映射只在这里维护一份，
// services 层负责按它裁剪 DB 快照与 zip 条目，前端只传导航 id（文案走 i18n）。
//
// 设计约束：
//   - 目录名用数据目录下的相对路径（斜杠分隔），与 zip 条目保持一致；
//   - 表清单尽量给全（含附属表），避免只搬主表却丢了关联数据；
//   - 被 db.mergeSkipTables 排除的表（app_state / credentials / 日志 / 剪贴板
//     历史 / 下载任务）即使列在这里，导入端也不会合并，别指望靠它迁移。
package migration

// Scope 一个导航菜单项对应的数据范围。
type Scope struct {
	ID     string   `json:"id"`
	Tables []string `json:"tables"`
	Dirs   []string `json:"dirs"`
	Vault  bool     `json:"vault"`
}

// scopes 顺序与前端 pages/registry.ts 的 navEntries 一致。
var scopes = []Scope{
	// 总览是聚合页：它读的表散落在各功能里，勾选总览等于把这些「卡片数据源」
	// 一起带上；不勾总览也不会影响其它功能各自的表。
	{
		ID: "dashboard",
		Tables: []string{
			"todos", "scheduled_tasks", "schedule_runs", "monitors",
			"usage_frecency", "file_shelf", "scripts", "ai_usage",
		},
	},
	// 工作空间（资源树）：四级结构 + 工具/频率/回收站。
	{
		ID: "workspace",
		Tables: []string{
			"workspaces", "scenes", "collections", "items", "tools",
			"usage_frecency", "tombstones", "entity_links", "item_health",
		},
		Dirs: []string{"icons"},
	},
	// 笔记：正文、修订历史、与项目/待办的互链。
	{ID: "notes", Tables: []string{"notes", "note_revisions", "entity_links"}},
	// 待办：任务本体、番茄钟历史、互链。
	{ID: "todo", Tables: []string{"todos", "focus_sessions", "entity_links"}},
	// 定时任务：任务、执行历史、它引用的脚本与 AI 空间。
	{
		ID:     "schedule",
		Tables: []string{"scheduled_tasks", "schedule_runs", "scripts", "script_runs", "ai_spaces"},
		Dirs:   []string{"scripts"},
	},
	// 脚本：正文在数据目录 scripts/<id>/，script_envs 含变量（可能带 secret，
	// 但仍属脚本功能本身；整机包本来就是用户手动导出给自己）。
	{ID: "script", Tables: []string{"scripts", "script_envs", "script_runs"}, Dirs: []string{"scripts"}},
	// 监控：只带配置，探测历史（monitor_logs）本来就不参与合并。
	{ID: "monitor", Tables: []string{"monitors"}},
	// 剪贴板：规则参与合并；历史（clipboard_entries）因隐私与体积被排除，
	// 图片目录还是带上，免得规则引用的旧图路径全断。
	{ID: "clipboard", Tables: []string{"clipboard_rules"}, Dirs: []string{"images"}},
	// 文件搜索：接外部 Everything，无业务表。
	{ID: "filesearch"},
	// 知识库：库/源/切片三张表，源文件本体在用户本机路径，不搬。
	{ID: "knowledge", Tables: []string{"kb_libraries", "kb_sources", "kb_chunks"}},
	// 书签：复用 items 表（type='网页'），还要带上它挂的场景/集合链。
	{ID: "bookmarks", Tables: []string{"items", "item_health", "collections", "scenes", "workspaces"}},
	// 环境管理：运行时与 GOPATH 都在数据目录里，DB 侧没有业务表。
	{ID: "environments", Dirs: []string{"runtime", "gopath"}},
	// 下载器：dl_tasks 参与合并，但 aria2 的 .aria2 控制文件与 gid 绑定，
	// 换机后任务本身多半失效——仍按用户勾选带表数据。
	{ID: "download", Tables: []string{"dl_tasks"}},
	// 网盘 / Git：配置在 app_state（不迁移），仓库与远端文件在外部路径。
	{ID: "netdisk"},
	{ID: "git"},
	// 密码库：DB 里的 credentials 是本机 DPAPI 密文，只能走口令便携包。
	{ID: "credentials", Vault: true},
	// SSH：主机树、已知主机、命令片段。
	{ID: "ssh", Tables: []string{"ssh_hosts", "ssh_known_hosts", "ssh_commands"}},
	// AI：会话/消息/用量/多 agent 轨迹 + 提示词，回滚点与日志在数据目录。
	{
		ID: "ai",
		Tables: []string{
			"ai_spaces", "ai_conversations", "ai_messages", "ai_usage",
			"ai_prompts", "ai_checkpoints", "ai_approvals", "ai_jobs",
			"agent_runs", "agent_nodes", "agent_events",
		},
		Dirs: []string{"ai_checkpoints", "ai_jobs"},
	},
	// DeepSeek Harness：便携 Node 在 runtime/，无业务表。
	{ID: "dsh", Dirs: []string{"runtime"}},
	// 插件：注册表与插件 KV，本体在数据目录 plugins/。
	{ID: "plugins", Tables: []string{"plugins", "plugin_data"}, Dirs: []string{"plugins"}},
}

// All 返回全部可勾选范围（返回内部切片，调用方只读）。
func All() []Scope { return scopes }

// Resolve 把选中的导航 id 并成去重后的表/目录集合，并判断是否需要密码库。
// 未知 id 直接忽略（前端版本比后端新时不必因此失败）。
func Resolve(navIDs []string) (tables []string, dirs []string, vault bool) {
	want := make(map[string]bool, len(navIDs))
	for _, id := range navIDs {
		want[id] = true
	}
	seenT, seenD := make(map[string]bool), make(map[string]bool)
	for _, sc := range scopes {
		if !want[sc.ID] {
			continue
		}
		for _, tb := range sc.Tables {
			if !seenT[tb] {
				seenT[tb] = true
				tables = append(tables, tb)
			}
		}
		for _, d := range sc.Dirs {
			if !seenD[d] {
				seenD[d] = true
				dirs = append(dirs, d)
			}
		}
		if sc.Vault {
			vault = true
		}
	}
	return tables, dirs, vault
}

// Has 判断某个导航 id 是否是已知范围。
func Has(id string) bool {
	for _, sc := range scopes {
		if sc.ID == id {
			return true
		}
	}
	return false
}
