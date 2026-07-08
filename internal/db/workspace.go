package db

import (
	"database/sql"
	"fmt"
)

// ---- 工作空间 ----

func (d *Database) ListWorkspaces() ([]Workspace, error) {
	rows, err := d.ListTable("workspaces")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToWorkspace), nil
}

func (d *Database) CreateWorkspace(name string) (*Workspace, error) {
	name = validateName(name)
	if name == "" {
		return nil, fmt.Errorf("工作空间名称不能为空")
	}

	exists, err := d.nameExists("workspaces", "name = ?", name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("名称已存在")
	}

	w := &Workspace{
		ID:        newID(),
		Name:      name,
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	err = d.BulkInsert("workspaces", []map[string]interface{}{structToMap(w)})
	return w, err
}

func (d *Database) UpdateWorkspace(id, name string) error {
	name = validateName(name)
	if name == "" {
		return fmt.Errorf("工作空间名称不能为空")
	}
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	return d.ExecuteParams("UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?",
		[]interface{}{name, now(), id})
}

func (d *Database) DeleteWorkspace(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM items WHERE workspace_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM collections WHERE workspace_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM scenes WHERE workspace_id = ?", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM workspaces WHERE id = ?", id)
		return err
	})
}

func (d *Database) GetWorkspace(id string) (*Workspace, error) {
	row, err := d.QueryOne("SELECT * FROM workspaces WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("工作空间不存在")
	}
	w := mapToWorkspace(row)
	return &w, nil
}

func (d *Database) ReorderWorkspaces(orderedIDs []string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			if _, err := tx.Exec("UPDATE workspaces SET sort = ? WHERE id = ?", i*10, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- 场景 ----

func (d *Database) ListScenes(workspaceID string) ([]Scene, error) {
	rows, err := d.ListTableWhere("scenes", "workspace_id = ?", workspaceID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToScene), nil
}

func (d *Database) CreateScene(workspaceID, name, sceneType string) (*Scene, error) {
	name = validateName(name)
	if name == "" {
		return nil, fmt.Errorf("场景名称不能为空")
	}
	if workspaceID == "" {
		return nil, fmt.Errorf("工作空间 ID 不能为空")
	}

	exists, err := d.nameExists("scenes", "workspace_id = ? AND name = ?", workspaceID, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, fmt.Errorf("名称已存在")
	}

	s := &Scene{
		ID:          newID(),
		WorkspaceID: workspaceID,
		Name:        name,
		Type:        sceneType,
		CreatedAt:   now(),
		UpdatedAt:   now(),
	}
	err = d.BulkInsert("scenes", []map[string]interface{}{structToMap(s)})
	return s, err
}

func (d *Database) UpdateScene(id string, updates map[string]interface{}) error {
	if id == "" {
		return fmt.Errorf("id 不能为空")
	}
	if name, ok := updates["name"]; ok {
		if s, ok2 := name.(string); ok2 && validateName(s) == "" {
			return fmt.Errorf("场景名称不能为空")
		}
	}
	updates["updated_at"] = now()
	return d.updateByID("scenes", id, updates)
}

func (d *Database) DeleteScene(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM items WHERE collection_id IN (SELECT id FROM collections WHERE scene_id = ?)", id); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM collections WHERE scene_id = ?", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM scenes WHERE id = ?", id)
		return err
	})
}

func (d *Database) ReorderScenes(orderedIDs []string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			if _, err := tx.Exec("UPDATE scenes SET sort = ? WHERE id = ?", i*10, id); err != nil {
				return err
			}
		}
		return nil
	})
}
