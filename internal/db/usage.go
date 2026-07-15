package db

import (
	"database/sql"
	"time"
)

// FrecencyEntry 前端需要的 frecency 数据
type FrecencyEntry struct {
	Key         string `json:"key"`
	Type        string `json:"type"`        // item | snippet | plugin | system | app
	Label       string `json:"label"`       // 显示标题
	Description string `json:"description"` // 副标题
	Count       int    `json:"count"`
	LastUsed    int64  `json:"lastUsed"` // Unix ms，与前端 Date.now() 单位一致
}

// RecordUsage 记录一次使用（兼容旧调用，无 metadata）
func (d *Database) RecordUsage(key string) error {
	return d.RecordUsageEx(key, "", "", "")
}

// RecordUsageEx 记录一次使用：count+1，last_used 更新，同时存储 type/label/desc
func (d *Database) RecordUsageEx(key, type_, label, desc string) error {
	now := time.Now().UnixMilli()

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		`INSERT INTO usage_frecency (key, type, label, description, count, last_used)
		 VALUES (?, ?, ?, ?, 1, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   type = CASE WHEN ? <> '' THEN ? ELSE type END,
		   label = CASE WHEN ? <> '' THEN ? ELSE label END,
		   description = CASE WHEN ? <> '' THEN ? ELSE description END,
		   count = count + 1,
		   last_used = ?`,
		key, type_, label, desc, now,
		type_, type_,
		label, label,
		desc, desc,
		now,
	)
	return err
}

// GetAllUsage 返回全部 frecency 记录（用于前端初始化一次性加载）
func (d *Database) GetAllUsage() ([]FrecencyEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY last_used DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FrecencyEntry
	for rows.Next() {
		var e FrecencyEntry
		if err := rows.Scan(&e.Key, &e.Type, &e.Label, &e.Description, &e.Count, &e.LastUsed); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// GetRecentUsage 返回最近使用的 N 条记录（命令面板「最近使用」专用）
func (d *Database) GetRecentUsage(limit int) ([]FrecencyEntry, error) {
	if limit <= 0 {
		limit = 8
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY last_used DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FrecencyEntry
	for rows.Next() {
		var e FrecencyEntry
		if err := rows.Scan(&e.Key, &e.Type, &e.Label, &e.Description, &e.Count, &e.LastUsed); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// GetTopUsage 返回使用次数最多的 N 条记录
func (d *Database) GetTopUsage(limit int) ([]FrecencyEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY count DESC, last_used DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FrecencyEntry
	for rows.Next() {
		var e FrecencyEntry
		if err := rows.Scan(&e.Key, &e.Type, &e.Label, &e.Description, &e.Count, &e.LastUsed); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// GetPluginUsageCount 返回插件的总使用次数（汇总所有 plugin:{id}.xxx 的记录）
func (d *Database) GetPluginUsageCount(pluginID string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	prefix := "plugin:" + pluginID + "%"
	var total sql.NullInt64
	err := d.conn.QueryRow("SELECT SUM(count) FROM usage_frecency WHERE key LIKE ?", prefix).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total.Valid {
		return int(total.Int64), nil
	}
	return 0, nil
}
