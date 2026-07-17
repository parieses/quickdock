package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// restoreTables 定义了恢复时清空表的顺序（与外键/依赖无关，仅保持确定性）。
var restoreTables = []struct {
	name string
	sql  string
}{
	{"items", "DELETE FROM items"},
	{"collections", "DELETE FROM collections"},
	{"scenes", "DELETE FROM scenes"},
	{"workspaces", "DELETE FROM workspaces"},
	{"tools", "DELETE FROM tools"},
}

// restorePayload 将完整快照载荷写回数据库（在事务内调用）。
// RestoreSnapshot 与 RestoreFromJSON 共用此逻辑，避免两份恢复代码漂移。
func restorePayload(tx *sql.Tx, payload *SnapshotPayload) error {
	for _, t := range restoreTables {
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
	return nil
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

	return &SnapshotPayload{
		Workspaces:  workspaces,
		Scenes:      scenes,
		Collections: collections,
		Items:       items,
		Tools:       tools,
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
