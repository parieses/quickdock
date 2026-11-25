package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// 按导航菜单裁剪 DB 快照：把未选功能的表清空，只留表结构。
//
// 为什么是「清空」而不是「删表」：
//   - 导入端 MergeDatabaseFile 会遍历包里所有表，表不存在就整表跳过；删表虽然
//     也能达到效果，但会让包内 schema 与目标版本不一致（列交集逻辑失去意义）；
//   - 保留空表还能让 DataImportInspect 展示「包含哪些功能」的时候直接读 manifest。
//
// 注意：app_state / snapshots / credentials 本来就不参与合并，这里不动它们；
// FTS 索引要单独处理（items_fts 是 external content，必须 rebuild 而不是 DELETE）。
var scopePrunableTables = map[string]bool{
	"workspaces": true, "scenes": true, "collections": true, "items": true, "tools": true,
	"tombstones": true, "clipboard_rules": true, "notifications": true,
	"notes": true, "note_revisions": true, "entity_links": true,
	"todos": true, "focus_sessions": true, "scheduled_tasks": true, "schedule_runs": true,
	"monitors": true, "scripts": true, "script_envs": true, "script_runs": true,
	"ai_spaces": true, "ai_conversations": true, "ai_messages": true, "ai_checkpoints": true,
	"ai_approvals": true, "ai_jobs": true, "ai_usage": true,
	"agent_runs": true, "agent_nodes": true, "agent_events": true, "ai_prompts": true,
	"ssh_hosts": true, "ssh_known_hosts": true, "ssh_commands": true,
	"kb_libraries": true, "kb_sources": true, "kb_chunks": true,
	"file_shelf": true, "plugins": true, "plugin_data": true, "item_health": true,
	"activity": true, "monitor_logs": true, "plugin_exec_logs": true,
	"clipboard_entries": true, "dl_tasks": true,
}

// PruneDatabaseForScope 把 dbPath 里不在 keep 集合中的业务表清空（保留表结构）。
// dbPath 必须是已生成的临时快照（VACUUM INTO 产物），不是正在使用的主库。
func PruneDatabaseForScope(dbPath string, keep map[string]bool) error {
	if dbPath == "" {
		return fmt.Errorf("快照路径不能为空")
	}
	dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(10000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("打开快照失败: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("开启裁剪事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	names, err := tableNames(tx, "main")
	if err != nil {
		return err
	}
	for _, name := range names {
		if !scopePrunableTables[name] || (keep != nil && keep[name]) {
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + quoteIdent(name)); err != nil {
			return fmt.Errorf("清空表 %s 失败: %w", name, err)
		}
	}

	// FTS 索引：items_fts 是 external content，直接 DELETE 会破坏索引一致性，
	// 必须在 items 表裁剪后 rebuild；另外两张是普通 FTS 表，源表被清空时一并清空。
	if hasTable(names, "items_fts") {
		if _, err := tx.Exec("INSERT INTO items_fts(items_fts) VALUES('rebuild')"); err != nil {
			return fmt.Errorf("重建 items_fts 失败: %w", err)
		}
	}
	if (keep == nil || !keep["notes"]) && hasTable(names, "notes_fts") {
		if _, err := tx.Exec("DELETE FROM notes_fts"); err != nil {
			return fmt.Errorf("清空 notes_fts 失败: %w", err)
		}
	}
	if (keep == nil || !keep["kb_chunks"]) && hasTable(names, "kb_chunks_fts") {
		if _, err := tx.Exec("DELETE FROM kb_chunks_fts"); err != nil {
			return fmt.Errorf("清空 kb_chunks_fts 失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交裁剪失败: %w", err)
	}
	return nil
}

func hasTable(names []string, want string) bool {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return true
		}
	}
	return false
}
