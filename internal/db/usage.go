package db

import (
	"database/sql"
	"strings"
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

// queryUsage 执行 frecency 查询并扫描结果，消除三个 GetXxxUsage 中的重复循环
func (d *Database) queryUsage(query string, args ...interface{}) ([]FrecencyEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query(query, args...)
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

// GetAllUsage 返回全部 frecency 记录（用于前端初始化一次性加载）
func (d *Database) GetAllUsage() ([]FrecencyEntry, error) {
	return d.queryUsage("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY last_used DESC")
}

// GetRecentUsage 返回最近使用的 N 条记录（命令面板「最近使用」专用）
func (d *Database) GetRecentUsage(limit int) ([]FrecencyEntry, error) {
	if limit <= 0 {
		limit = 8
	}
	return d.queryUsage("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY last_used DESC LIMIT ?", limit)
}

// GetTopUsage 返回使用次数最多的 N 条记录
func (d *Database) GetTopUsage(limit int) ([]FrecencyEntry, error) {
	return d.queryUsage("SELECT key, type, label, description, count, last_used FROM usage_frecency ORDER BY count DESC, last_used DESC LIMIT ?", limit)
}

// GetAllPluginUsageCounts 一条 SQL 查出所有插件的使用次数，返回 map[pluginID]sum
// 替代原来 ListPlugins() 里对每个插件单独查 GetPluginUsageCount 的 N+1 模式
func (d *Database) GetAllPluginUsageCounts() (map[string]int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT key, SUM(count) FROM usage_frecency WHERE key LIKE 'plugin:%' GROUP BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var key string
		var total sql.NullInt64
		if err := rows.Scan(&key, &total); err != nil {
			return nil, err
		}
		if !total.Valid {
			continue
		}
		// key = "plugin:{pluginID}.{commandID}"，取 plugin: 之后、最后一个 . 之前的部分
		k := strings.TrimPrefix(key, "plugin:")
		if idx := strings.LastIndex(k, "."); idx > 0 {
			pid := k[:idx]
			out[pid] += int(total.Int64)
		}
	}
	return out, rows.Err()
}
