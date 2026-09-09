package db

import (
	"database/sql"
	"fmt"

	"quickdock/internal/logger"
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
	// 级联删除：先删 items → collections → scenes，最后删 workspace
	// 使用事务确保原子性
	return d.Transaction(func(tx *sql.Tx) error {
		// 1. 获取该工作空间下所有 scene IDs
		rows, err := tx.Query("SELECT id FROM scenes WHERE workspace_id = ?", id)
		if err != nil {
			return err
		}
		sceneIDs := make([]string, 0)
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				continue
			}
			sceneIDs = append(sceneIDs, sid)
		}
		rows.Close()

		// 2. 收集所有 collection IDs（通过 scene IDs）
		collectionIDs := make([]string, 0)
		for _, sid := range sceneIDs {
			crows, err := tx.Query("SELECT id FROM collections WHERE scene_id = ?", sid)
			if err != nil {
				continue
			}
			for crows.Next() {
				var cid string
				if err := crows.Scan(&cid); err != nil {
					continue
				}
				collectionIDs = append(collectionIDs, cid)
			}
			crows.Close()
		}

		// 3. 删除 items
		for _, cid := range collectionIDs {
			if _, err := tx.Exec("DELETE FROM items WHERE collection_id = ?", cid); err != nil {
				logger.W("QuickDock: 删除 items 失败 (collection=%s): %v", cid, err)
			}
		}

		// 4. 删除 collections
		for _, sid := range sceneIDs {
			if _, err := tx.Exec("DELETE FROM collections WHERE scene_id = ?", sid); err != nil {
				logger.W("QuickDock: 删除 collections 失败 (scene=%s): %v", sid, err)
			}
		}

		// 5. 删除 scenes
		for _, sid := range sceneIDs {
			if _, err := tx.Exec("DELETE FROM scenes WHERE id = ?", sid); err != nil {
				logger.W("QuickDock: 删除 scene 失败: %v", err)
			}
		}

		// 6. 删除 workspace
		_, err = tx.Exec("DELETE FROM workspaces WHERE id = ?", id)
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
	// 级联删除：先删该 scene 下的 collections → items，最后删 scene
	return d.Transaction(func(tx *sql.Tx) error {
		// 1. 获取该 scene 下的所有 collection IDs
		rows, err := tx.Query("SELECT id FROM collections WHERE scene_id = ?", id)
		if err != nil {
			return err
		}
		collectionIDs := make([]string, 0)
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				continue
			}
			collectionIDs = append(collectionIDs, cid)
		}
		rows.Close()

		// 2. 删除 items
		for _, cid := range collectionIDs {
			if _, err := tx.Exec("DELETE FROM items WHERE collection_id = ?", cid); err != nil {
				logger.W("QuickDock: 删除 items 失败 (collection=%s): %v", cid, err)
			}
		}

		// 3. 删除 collections
		for _, cid := range collectionIDs {
			if _, err := tx.Exec("DELETE FROM collections WHERE id = ?", cid); err != nil {
				logger.W("QuickDock: 删除 collection 失败: %v", err)
			}
		}

		// 4. 删除 scene
		_, err = tx.Exec("DELETE FROM scenes WHERE id = ?", id)
		return err
	})
}

