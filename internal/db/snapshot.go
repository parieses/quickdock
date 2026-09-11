package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ---- 快照 ----

func (d *Database) ListSnapshots() ([]Snapshot, error) {
	rows, err := d.Query("SELECT id, kind, label, note, payload, size, created_at FROM snapshots ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToSnapshot), nil
}

func (d *Database) GetSnapshot(id string) (*Snapshot, error) {
	row, err := d.QueryOne("SELECT id, kind, label, note, payload, size, created_at FROM snapshots WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("快照不存在")
	}
	s := mapToSnapshot(row)
	return &s, nil
}

func (d *Database) CreateSnapshot(s *Snapshot) error {
	return d.BulkInsert("snapshots", []map[string]interface{}{structToMap(s)})
}

func (d *Database) DeleteSnapshot(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM snapshots WHERE id = ?", id)
	return err
}

// restoreCoreTables 核心表：恢复时无条件清空后再写入（历史行为，保持兼容）。
// 顺序与外键/依赖无关，仅保持确定性。
var restoreCoreTables = []struct {
	name string
	sql  string
}{
	{"items", "DELETE FROM items"},
	{"collections", "DELETE FROM collections"},
	{"scenes", "DELETE FROM scenes"},
	{"workspaces", "DELETE FROM workspaces"},
	{"tools", "DELETE FROM tools"},
}

// syncExtraTables 参与备份的扩展表（核心表之外的用户数据与配置）。
//
// 选择标准：用户自己创造或配置、丢了会心疼的内容。刻意排除：
//   - 日志类：activity / monitor_logs / plugin_exec_logs
//   - 隐私且量大：clipboard_entries
//   - 会自我嵌套：snapshots
//   - 机器绑定：app_state（存 sync_config / webdav_config，密码为本机密钥加密，换机解不开）
//
// 这些表行数多、列随版本演进（CREATE TABLE + ALTER 叠加），故不做强类型映射，
// 统一走 SnapshotPayload.Tables 的通用通道。
var syncExtraTables = []string{
	"notes",           // 笔记树
	"todos",           // 待办 / 看板
	"scheduled_tasks", // 定时任务
	"monitors",        // 站点监控
	"plugins",         // 已装插件记录与配置（不含插件文件）
	"plugin_data",     // 插件写入的数据
	"usage_frecency",  // 使用频率（影响列表排序）
}

// restorePayload 将完整快照载荷写回数据库（在事务内调用）。
// RestoreSnapshot 与 RestoreFromJSON 共用此逻辑，避免两份恢复代码漂移。
func restorePayload(tx *sql.Tx, payload *SnapshotPayload) error {
	for _, t := range restoreCoreTables {
		if _, err := tx.Exec(t.sql); err != nil {
			return fmt.Errorf("清除%s失败: %w", t.name, err)
		}
	}

	for i := range payload.Workspaces {
		w := &payload.Workspaces[i]
		if _, err := tx.Exec(
			"INSERT INTO workspaces (id, name, storage, remark, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			w.ID, w.Name, w.Storage, w.Remark, w.CreatedAt, w.UpdatedAt,
		); err != nil {
			return fmt.Errorf("恢复工作空间失败: %w", err)
		}
	}
	for i := range payload.Scenes {
		s := &payload.Scenes[i]
		if _, err := tx.Exec(
			"INSERT INTO scenes (id, workspace_id, name, type, description, icon, color, favorite, unbound, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			s.ID, s.WorkspaceID, s.Name, s.Type, s.Description, s.Icon,
			s.Color, s.Favorite, s.Unbound, s.UsageCount, s.Sort, s.CreatedAt, s.UpdatedAt,
		); err != nil {
			return fmt.Errorf("恢复场景失败: %w", err)
		}
	}
	for i := range payload.Collections {
		c := &payload.Collections[i]
		if _, err := tx.Exec(
			"INSERT INTO collections (id, workspace_id, scene_id, name, type, description, default_tool_id, tool, icon, color, open_strategy, favorite, recent, recent_at, unbound, plugin_id, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			c.ID, c.WorkspaceID, c.SceneID, c.Name, c.Type, c.Description,
			c.DefaultToolID, c.Tool, c.Icon, c.Color, c.OpenStrategy, c.Favorite,
			c.Recent, c.RecentAt, c.Unbound, c.PluginID, c.UsageCount, c.Sort, c.CreatedAt, c.UpdatedAt,
		); err != nil {
			return fmt.Errorf("恢复集合失败: %w", err)
		}
	}
	for i := range payload.Items {
		it := &payload.Items[i]
		if _, err := tx.Exec(
			"INSERT INTO items (id, workspace_id, collection_id, name, type, value, working_directory, tool_id, tool, args, icon, color, remark, plugin_data, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			it.ID, it.WorkspaceID, it.CollectionID, it.Name, it.Type, it.Value,
			it.WorkingDirectory, it.ToolID, it.Tool, it.Args, it.Icon, it.Color,
			it.Remark, it.PluginData, it.UsageCount, it.Sort, it.CreatedAt, it.UpdatedAt,
		); err != nil {
			return fmt.Errorf("恢复项目失败: %w", err)
		}
	}
	for i := range payload.Tools {
		t := &payload.Tools[i]
		if _, err := tx.Exec(
			"INSERT INTO tools (id, name, type, path, args, is_default) VALUES (?, ?, ?, ?, ?, ?)",
			t.ID, t.Name, t.Type, t.Path, t.Args, t.IsDefault,
		); err != nil {
			return fmt.Errorf("恢复工具失败: %w", err)
		}
	}

	// 扩展表：只有备份中确实带了这张表（map 中存在该 key）才清空并写入。
	// 旧版备份没有 Tables 字段，此时不动这些表，避免把用户后来的笔记 / 待办等清空。
	for _, name := range syncExtraTables {
		rows, ok := payload.Tables[name]
		if !ok {
			continue
		}
		if err := replaceTable(tx, name, rows); err != nil {
			return err
		}
	}
	return nil
}

// replaceTable 在事务内用 rows 覆盖整表：先清空，再按当前库的实际列逐行插入。
// 备份中来自其他版本的未知列会被忽略，因此对新旧库的列差异免疫。
func replaceTable(tx *sql.Tx, table string, rows []map[string]interface{}) error {
	if err := validateTable(table); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM " + table); err != nil {
		return fmt.Errorf("清除%s失败: %w", table, err)
	}
	if len(rows) == 0 {
		return nil
	}
	cols, err := tableColumnsTx(tx, table)
	if err != nil {
		return fmt.Errorf("读取%s表结构失败: %w", table, err)
	}
	for _, row := range rows {
		pairs := make([]kvPair, 0, len(row))
		for col, val := range row {
			if !cols[col] {
				continue // 备份中属于其他版本的列，当前库没有，跳过
			}
			pairs = append(pairs, kvPair{col, val})
		}
		if len(pairs) == 0 {
			continue
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].col < pairs[j].col })

		names := make([]string, len(pairs))
		values := make([]interface{}, len(pairs))
		placeholders := make([]string, len(pairs))
		for i, p := range pairs {
			names[i] = p.col
			values[i] = p.val
			placeholders[i] = "?"
		}
		query := "INSERT INTO " + table + " (" + strings.Join(names, ", ") +
			") VALUES (" + strings.Join(placeholders, ", ") + ")"
		if _, err := tx.Exec(query, values...); err != nil {
			return fmt.Errorf("恢复%s失败: %w", table, err)
		}
	}
	return nil
}

// kvPair 一列名与列值，用于按列名排序后再拼 SQL（map 遍历顺序随机）。
type kvPair struct {
	col string
	val interface{}
}

// tableColumnsTx 读取表在当前库中的实际列集合（表名须先经 validateTable 校验）。
// 用 PRAGMA 而非硬编码白名单：列集合即真实结构，新增/删除列无需同步维护。
func tableColumnsTx(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             interface{}
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// collectSnapshotPayload 收集全部业务表为快照载荷。
func (d *Database) collectSnapshotPayload() (*SnapshotPayload, error) {
	workspaces, err := d.ListWorkspaces()
	if err != nil {
		return nil, fmt.Errorf("收集工作空间失败: %w", err)
	}
	sceneRows, err := d.ListTable("scenes")
	if err != nil {
		return nil, fmt.Errorf("收集场景失败: %w", err)
	}
	scenes := mapSlice(sceneRows, mapToScene)
	collectionRows, err := d.ListTable("collections")
	if err != nil {
		return nil, fmt.Errorf("收集集合失败: %w", err)
	}
	collections := mapSlice(collectionRows, mapToCollection)
	itemRows, err := d.ListTable("items")
	if err != nil {
		return nil, fmt.Errorf("收集项目失败: %w", err)
	}
	items := mapSlice(itemRows, mapToItem)
	tools, err := d.ListTools()
	if err != nil {
		return nil, fmt.Errorf("收集工具失败: %w", err)
	}

	// 扩展表按真实表名收集（不做类型映射）。即使表为空也要写入 key，
	// 这样恢复时能识别「备份确实包含这张表」，从而正确清空目标表。
	tables := make(map[string][]map[string]interface{}, len(syncExtraTables))
	for _, name := range syncExtraTables {
		rows, err := d.ListTable(name)
		if err != nil {
			return nil, fmt.Errorf("收集%s失败: %w", name, err)
		}
		tables[name] = rows
	}

	return &SnapshotPayload{
		Workspaces:  workspaces,
		Scenes:      scenes,
		Collections: collections,
		Items:       items,
		Tools:       tools,
		Tables:      tables,
	}, nil
}

func (d *Database) CreateFullSnapshot(label, note string) (*Snapshot, error) {
	payload, err := d.collectSnapshotPayload()
	if err != nil {
		return nil, err
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化快照失败: %w", err)
	}

	s := &Snapshot{
		ID:        newID(),
		Kind:      "full",
		Label:     label,
		Note:      note,
		Payload:   string(payloadBytes),
		Size:      int64(len(payloadBytes)),
		CreatedAt: now(),
	}

	if err := d.CreateSnapshot(s); err != nil {
		return nil, fmt.Errorf("保存快照失败: %w", err)
	}

	return s, nil
}

func (d *Database) RestoreSnapshot(id string) error {
	s, err := d.GetSnapshot(id)
	if err != nil {
		return err
	}

	var payload SnapshotPayload
	if err := json.Unmarshal([]byte(s.Payload), &payload); err != nil {
		return fmt.Errorf("解析快照载荷失败: %w", err)
	}

	return d.Transaction(func(tx *sql.Tx) error {
		return restorePayload(tx, &payload)
	})
}

// ExportFullDataAsJSON 导出全部数据为 JSON 字符串（不创建快照记录）
func (d *Database) ExportFullDataAsJSON() (string, error) {
	payload, err := d.collectSnapshotPayload()
	if err != nil {
		return "", err
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化数据失败: %w", err)
	}
	return string(payloadBytes), nil
}

// RestoreFromJSON 从 JSON 数据恢复（与 RestoreSnapshot 相同逻辑）
func (d *Database) RestoreFromJSON(jsonStr string) error {
	var payload SnapshotPayload
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return fmt.Errorf("解析数据失败: %w", err)
	}

	return d.Transaction(func(tx *sql.Tx) error {
		return restorePayload(tx, &payload)
	})
}
