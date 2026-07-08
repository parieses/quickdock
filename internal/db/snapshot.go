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

func (d *Database) CreateFullSnapshot(label, note string) (*Snapshot, error) {
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

	payload := SnapshotPayload{
		Workspaces:  workspaces,
		Scenes:      scenes,
		Collections: collections,
		Items:       items,
		Tools:       tools,
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
		if _, err := tx.Exec("DELETE FROM items"); err != nil {
			return fmt.Errorf("清除项目失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM collections"); err != nil {
			return fmt.Errorf("清除集合失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM scenes"); err != nil {
			return fmt.Errorf("清除场景失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM workspaces"); err != nil {
			return fmt.Errorf("清除工作空间失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM tools"); err != nil {
			return fmt.Errorf("清除工具失败: %w", err)
		}

		for i := range payload.Workspaces {
			if _, err := tx.Exec(
				"INSERT INTO workspaces (id, name, storage, remark, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
				payload.Workspaces[i].ID, payload.Workspaces[i].Name, payload.Workspaces[i].Storage,
				payload.Workspaces[i].Remark, payload.Workspaces[i].CreatedAt, payload.Workspaces[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复工作空间失败: %w", err)
			}
		}
		for i := range payload.Scenes {
			if _, err := tx.Exec(
				"INSERT INTO scenes (id, workspace_id, name, type, description, icon, color, favorite, unbound, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Scenes[i].ID, payload.Scenes[i].WorkspaceID, payload.Scenes[i].Name,
				payload.Scenes[i].Type, payload.Scenes[i].Description, payload.Scenes[i].Icon,
				payload.Scenes[i].Color, payload.Scenes[i].Favorite, payload.Scenes[i].Unbound,
				payload.Scenes[i].UsageCount, payload.Scenes[i].Sort, payload.Scenes[i].CreatedAt,
				payload.Scenes[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复场景失败: %w", err)
			}
		}
		for i := range payload.Collections {
			if _, err := tx.Exec(
				"INSERT INTO collections (id, workspace_id, scene_id, name, type, description, default_tool_id, tool, icon, color, open_strategy, favorite, recent, recent_at, unbound, plugin_id, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Collections[i].ID, payload.Collections[i].WorkspaceID, payload.Collections[i].SceneID,
				payload.Collections[i].Name, payload.Collections[i].Type, payload.Collections[i].Description,
				payload.Collections[i].DefaultToolID, payload.Collections[i].Tool, payload.Collections[i].Icon,
				payload.Collections[i].Color, payload.Collections[i].OpenStrategy, payload.Collections[i].Favorite,
				payload.Collections[i].Recent, payload.Collections[i].RecentAt, payload.Collections[i].Unbound,
				payload.Collections[i].PluginID, payload.Collections[i].UsageCount, payload.Collections[i].Sort,
				payload.Collections[i].CreatedAt, payload.Collections[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复集合失败: %w", err)
			}
		}
		for i := range payload.Items {
			if _, err := tx.Exec(
				"INSERT INTO items (id, workspace_id, collection_id, name, type, value, working_directory, tool_id, tool, args, icon, color, remark, plugin_data, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Items[i].ID, payload.Items[i].WorkspaceID, payload.Items[i].CollectionID,
				payload.Items[i].Name, payload.Items[i].Type, payload.Items[i].Value,
				payload.Items[i].WorkingDirectory, payload.Items[i].ToolID, payload.Items[i].Tool,
				payload.Items[i].Args, payload.Items[i].Icon, payload.Items[i].Color,
				payload.Items[i].Remark, payload.Items[i].PluginData, payload.Items[i].UsageCount,
				payload.Items[i].Sort, payload.Items[i].CreatedAt, payload.Items[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复项目失败: %w", err)
			}
		}
		for i := range payload.Tools {
			if _, err := tx.Exec(
				"INSERT INTO tools (id, name, type, path, args, is_default) VALUES (?, ?, ?, ?, ?, ?)",
				payload.Tools[i].ID, payload.Tools[i].Name, payload.Tools[i].Type,
				payload.Tools[i].Path, payload.Tools[i].Args, payload.Tools[i].IsDefault,
			); err != nil {
				return fmt.Errorf("恢复工具失败: %w", err)
			}
		}
		return nil
	})
}

// ExportFullDataAsJSON 导出全部数据为 JSON 字符串（不创建快照记录）
func (d *Database) ExportFullDataAsJSON() (string, error) {
	workspaces, err := d.ListWorkspaces()
	if err != nil {
		return "", fmt.Errorf("收集工作空间失败: %w", err)
	}
	sceneRows, err := d.ListTable("scenes")
	if err != nil {
		return "", fmt.Errorf("收集场景失败: %w", err)
	}
	scenes := mapSlice(sceneRows, mapToScene)
	collectionRows, err := d.ListTable("collections")
	if err != nil {
		return "", fmt.Errorf("收集集合失败: %w", err)
	}
	collections := mapSlice(collectionRows, mapToCollection)
	itemRows, err := d.ListTable("items")
	if err != nil {
		return "", fmt.Errorf("收集项目失败: %w", err)
	}
	items := mapSlice(itemRows, mapToItem)
	tools, err := d.ListTools()
	if err != nil {
		return "", fmt.Errorf("收集工具失败: %w", err)
	}

	payload := SnapshotPayload{
		Workspaces:  workspaces,
		Scenes:      scenes,
		Collections: collections,
		Items:       items,
		Tools:       tools,
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
		if _, err := tx.Exec("DELETE FROM items"); err != nil {
			return fmt.Errorf("清除项目失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM collections"); err != nil {
			return fmt.Errorf("清除集合失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM scenes"); err != nil {
			return fmt.Errorf("清除场景失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM workspaces"); err != nil {
			return fmt.Errorf("清除工作空间失败: %w", err)
		}
		if _, err := tx.Exec("DELETE FROM tools"); err != nil {
			return fmt.Errorf("清除工具失败: %w", err)
		}

		for i := range payload.Workspaces {
			if _, err := tx.Exec(
				"INSERT INTO workspaces (id, name, storage, remark, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
				payload.Workspaces[i].ID, payload.Workspaces[i].Name, payload.Workspaces[i].Storage,
				payload.Workspaces[i].Remark, payload.Workspaces[i].CreatedAt, payload.Workspaces[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复工作空间失败: %w", err)
			}
		}
		for i := range payload.Scenes {
			if _, err := tx.Exec(
				"INSERT INTO scenes (id, workspace_id, name, type, description, icon, color, favorite, unbound, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Scenes[i].ID, payload.Scenes[i].WorkspaceID, payload.Scenes[i].Name,
				payload.Scenes[i].Type, payload.Scenes[i].Description, payload.Scenes[i].Icon,
				payload.Scenes[i].Color, payload.Scenes[i].Favorite, payload.Scenes[i].Unbound,
				payload.Scenes[i].UsageCount, payload.Scenes[i].Sort, payload.Scenes[i].CreatedAt,
				payload.Scenes[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复场景失败: %w", err)
			}
		}
		for i := range payload.Collections {
			if _, err := tx.Exec(
				"INSERT INTO collections (id, workspace_id, scene_id, name, type, description, default_tool_id, tool, icon, color, open_strategy, favorite, recent, recent_at, unbound, plugin_id, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Collections[i].ID, payload.Collections[i].WorkspaceID, payload.Collections[i].SceneID,
				payload.Collections[i].Name, payload.Collections[i].Type, payload.Collections[i].Description,
				payload.Collections[i].DefaultToolID, payload.Collections[i].Tool, payload.Collections[i].Icon,
				payload.Collections[i].Color, payload.Collections[i].OpenStrategy, payload.Collections[i].Favorite,
				payload.Collections[i].Recent, payload.Collections[i].RecentAt, payload.Collections[i].Unbound,
				payload.Collections[i].PluginID, payload.Collections[i].UsageCount, payload.Collections[i].Sort,
				payload.Collections[i].CreatedAt, payload.Collections[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复集合失败: %w", err)
			}
		}
		for i := range payload.Items {
			if _, err := tx.Exec(
				"INSERT INTO items (id, workspace_id, collection_id, name, type, value, working_directory, tool_id, tool, args, icon, color, remark, plugin_data, usage_count, sort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				payload.Items[i].ID, payload.Items[i].WorkspaceID, payload.Items[i].CollectionID,
				payload.Items[i].Name, payload.Items[i].Type, payload.Items[i].Value,
				payload.Items[i].WorkingDirectory, payload.Items[i].ToolID, payload.Items[i].Tool,
				payload.Items[i].Args, payload.Items[i].Icon, payload.Items[i].Color,
				payload.Items[i].Remark, payload.Items[i].PluginData, payload.Items[i].UsageCount,
				payload.Items[i].Sort, payload.Items[i].CreatedAt, payload.Items[i].UpdatedAt,
			); err != nil {
				return fmt.Errorf("恢复项目失败: %w", err)
			}
		}
		for i := range payload.Tools {
			if _, err := tx.Exec(
				"INSERT INTO tools (id, name, type, path, args, is_default) VALUES (?, ?, ?, ?, ?, ?)",
				payload.Tools[i].ID, payload.Tools[i].Name, payload.Tools[i].Type,
				payload.Tools[i].Path, payload.Tools[i].Args, payload.Tools[i].IsDefault,
			); err != nil {
				return fmt.Errorf("恢复工具失败: %w", err)
			}
		}
		return nil
	})
}
