package db

import (
	"database/sql"
	"fmt"
)

func (d *Database) ListCollections(sceneID string) ([]Collection, error) {
	rows, err := d.ListTableWhere("collections", "scene_id = ?", sceneID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToCollection), nil
}

func (d *Database) ListUnboundCollections(workspaceID string) ([]Collection, error) {
	rows, err := d.ListTableWhere("collections", "workspace_id = ? AND scene_id IS NULL", workspaceID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToCollection), nil
}

func (d *Database) CreateCollection(workspaceID, sceneID, name, collType, openStrategy string) (*Collection, error) {
	name = validateName(name)
	if name == "" {
		return nil, fmt.Errorf("集合名称不能为空")
	}
	if workspaceID == "" {
		return nil, fmt.Errorf("工作空间 ID 不能为空")
	}

	exists, err := d.nameExists("collections", "scene_id = ? AND name = ?", sceneID, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("名称已存在")
	}

	c := &Collection{
		ID:           newID(),
		WorkspaceID:  workspaceID,
		SceneID:      sceneID,
		Name:         name,
		Type:         collType,
		OpenStrategy: openStrategy,
		CreatedAt:    now(),
		UpdatedAt:    now(),
	}
	err = d.BulkInsert("collections", []map[string]interface{}{structToMap(c)})
	return c, err
}

func (d *Database) UpdateCollection(id string, updates map[string]interface{}) error {
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	if name, ok := updates["name"]; ok {
		if s, ok2 := name.(string); ok2 && validateName(s) == "" {
			return fmt.Errorf("集合名称不能为空")
		}
	}
	updates["updated_at"] = now()
	return d.updateByID("collections", id, updates)
}

func (d *Database) DeleteCollection(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM items WHERE collection_id = ?", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM collections WHERE id = ?", id)
		return err
	})
}

func (d *Database) ReorderCollections(orderedIDs []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, id := range orderedIDs {
		if _, err := d.conn.Exec("UPDATE collections SET sort = ? WHERE id = ?", i*10, id); err != nil {
			return err
		}
	}
	return nil
}
