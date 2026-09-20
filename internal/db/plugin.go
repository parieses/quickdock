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

// InsertPluginFull 插入插件全部字段（含 capabilities / permissions / category / icon）
// iconData 是 base64 data URI，由调用者从插件目录读取
// refreshUpdatedAt 控制 ON CONFLICT（记录已存在）时是否刷新 updated_at：
//   - true：安装 / 更新插件时传 true，updated_at 刷成当前时间，供列表「最近安装/更新」排序；
//   - false：启动时内置插件注册传 false，保留原值——否则每次启动都会把所有内置插件刷成「最新」，
//     冲乱排序。（installed_at 首次写入后两种情况都不再变。）
func (d *Database) InsertPluginFull(id, name, version, author, description, category, iconData string, capabilities []string, permissions map[string]interface{}, refreshUpdatedAt bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	capsJSON, _ := json.Marshal(capabilities)
	permJSON, _ := json.Marshal(permissions)
	ts := now() // 本地时区 RFC3339，与全库其他表的时间格式保持一致（勿用 SQLite 的 UTC datetime('now')）

	// updated_at 的冲突更新表达式按需二选一（均为内部常量，无注入面）
	updatedAtExpr := "plugins.updated_at"
	if refreshUpdatedAt {
		updatedAtExpr = "excluded.updated_at"
	}

	_, err := d.conn.Exec(
		fmt.Sprintf(`INSERT INTO plugins (id, name, version, author, description, category, icon, enabled, capabilities, permissions, installed_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   version = excluded.version,
		   author = excluded.author,
		   description = excluded.description,
		   category = excluded.category,
		   icon = excluded.icon,
		   enabled = plugins.enabled, -- 保留用户当前的启用/禁用状态，避免下次启动把用户禁用改回启用
		   capabilities = excluded.capabilities,
		   permissions = excluded.permissions,
		   updated_at = %s`, updatedAtExpr),
		id, name, version, author, description, category, iconData, string(capsJSON), string(permJSON), ts, ts,
	)
	if err != nil {
		return fmt.Errorf("写入插件记录失败: %w", err)
	}
	return nil
}

// ListAllPluginIDs 列出所有插件记录 ID（含已禁用），用于清理残留记录
func (d *Database) ListAllPluginIDs() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT id FROM plugins")
	if err != nil {
		return nil, fmt.Errorf("查询插件记录失败: %w", err)
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

// ListPluginVersions 返回所有插件记录的 id → 安装版本（含已禁用）。
// 供插件市场页判定「已安装 / 有新版」——判定源必须与本地插件列表一致（都是 DB 记录）；
// 磁盘目录只代表「有文件」，不代表「已注册」。
func (d *Database) ListPluginVersions() (map[string]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query("SELECT id, version FROM plugins")
	if err != nil {
		return nil, fmt.Errorf("查询插件版本失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var id, version string
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		out[id] = version
	}
	return out, rows.Err()
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

// PluginTimestamp 插件记录的安装 / 更新时间（RFC3339 本地时区）。
type PluginTimestamp struct {
	InstalledAt string
	UpdatedAt   string
}

// GetPluginTimestamps 返回所有插件 id → 安装 / 更新时间，供列表按「最近安装/更新」排序。
func (d *Database) GetPluginTimestamps() (map[string]PluginTimestamp, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.conn.Query("SELECT id, installed_at, updated_at FROM plugins")
	if err != nil {
		return nil, fmt.Errorf("查询插件时间失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]PluginTimestamp)
	for rows.Next() {
		var id string
		var ts PluginTimestamp
		if err := rows.Scan(&id, &ts.InstalledAt, &ts.UpdatedAt); err != nil {
			return nil, err
		}
		out[id] = ts
	}
	return out, rows.Err()
}
