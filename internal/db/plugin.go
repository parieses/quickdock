package db

import (
	"encoding/json"
	"fmt"
)

// SetPluginEnabled 设置插件启用状态
func (d *Database) SetPluginEnabled(id string, enabled int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("UPDATE plugins SET enabled = ? WHERE id = ?", enabled, id)
	if err != nil {
		return fmt.Errorf("更新插件状态失败: %w", err)
	}
	return nil
}

// DeletePlugin 删除插件记录
func (d *Database) DeletePlugin(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("DELETE FROM plugins WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("删除插件记录失败: %w", err)
	}
	return nil
}

// InsertPlugin 插入插件记录（安装时调用）
func (d *Database) InsertPlugin(id, name, version, author, description string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		`INSERT INTO plugins (id, name, version, author, description, installed_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   version = excluded.version,
		   author = excluded.author,
		   description = excluded.description,
		   updated_at = datetime('now')`,
		id, name, version, author, description,
	)
	if err != nil {
		return fmt.Errorf("写入插件记录失败: %w", err)
	}
	return nil
}

// InsertPluginFull 插入插件全部字段（含 capabilities / permissions / category / icon）
// iconData 是 base64 data URI，由调用者从插件目录读取
func (d *Database) InsertPluginFull(id, name, version, author, description, category, iconData string, capabilities []string, permissions map[string]interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	capsJSON, _ := json.Marshal(capabilities)
	permJSON, _ := json.Marshal(permissions)

	_, err := d.conn.Exec(
		`INSERT INTO plugins (id, name, version, author, description, category, icon, enabled, capabilities, permissions, installed_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, datetime('now'), datetime('now'))
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   version = excluded.version,
		   author = excluded.author,
		   description = excluded.description,
		   category = excluded.category,
		   icon = excluded.icon,
		   enabled = 1,
		   capabilities = excluded.capabilities,
		   permissions = excluded.permissions,
		   updated_at = datetime('now')`,
		id, name, version, author, description, category, iconData, string(capsJSON), string(permJSON),
	)
	if err != nil {
		return fmt.Errorf("写入插件记录失败: %w", err)
	}
	return nil
}

// ListEnabledPlugins 列出所有已启用插件 ID
func (d *Database) ListEnabledPlugins() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT id FROM plugins WHERE enabled = 1")
	if err != nil {
		return nil, fmt.Errorf("查询已启用插件失败: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
