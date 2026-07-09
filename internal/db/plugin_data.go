package db

import (
	"fmt"
)

// GetPluginData 读取插件专属存储（强制绑定 plugin_id）
func (d *Database) GetPluginData(pluginID, key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var value string
	err := d.conn.QueryRow(
		"SELECT value FROM plugin_data WHERE plugin_id = ? AND key = ?",
		pluginID, key,
	).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("读取插件数据失败: %w", err)
	}
	return value, nil
}

// SetPluginData 写入插件专属存储（强制绑定 plugin_id）
func (d *Database) SetPluginData(pluginID, key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		"INSERT INTO plugin_data (plugin_id, key, value) VALUES (?, ?, ?) "+
			"ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value",
		pluginID, key, value,
	)
	if err != nil {
		return fmt.Errorf("写入插件数据失败: %w", err)
	}
	return nil
}

// DeletePluginData 删除插件专属存储条目
func (d *Database) DeletePluginData(pluginID, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		"DELETE FROM plugin_data WHERE plugin_id = ? AND key = ?",
		pluginID, key,
	)
	if err != nil {
		return fmt.Errorf("删除插件数据失败: %w", err)
	}
	return nil
}

// ListPluginData 列出插件的所有存储条目
func (d *Database) ListPluginData(pluginID string) (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query(
		"SELECT key, value FROM plugin_data WHERE plugin_id = ?",
		pluginID,
	)
	if err != nil {
		return nil, fmt.Errorf("列举插件数据失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, rows.Err()
}

// CleanPluginData 清理插件的所有数据（卸载时调用）
func (d *Database) CleanPluginData(pluginID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("DELETE FROM plugin_data WHERE plugin_id = ?", pluginID)
	return err
}
