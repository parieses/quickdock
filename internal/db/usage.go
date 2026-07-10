package db

import "time"

// FrecencyEntry 前端需要的 frecency 数据
// 注意：count 和 lastUsed 命名与前端 JS 一致
type FrecencyEntry struct {
	Key     string `json:"key"`
	Count   int    `json:"count"`
	LastUsed int64 `json:"lastUsed"` // Unix ms，与前端 Date.now() 单位一致
}

// RecordUsage 记录一次使用：count+1，last_used 更新为当前时间
func (d *Database) RecordUsage(key string) error {
	now := time.Now().UnixMilli()

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		`INSERT INTO usage_frecency (key, count, last_used)
		 VALUES (?, 1, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   count = count + 1,
		   last_used = ?`,
		key, now, now,
	)
	return err
}

// GetAllUsage 返回全部 frecency 记录（用于前端初始化一次性加载）
func (d *Database) GetAllUsage() ([]FrecencyEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query("SELECT key, count, last_used FROM usage_frecency ORDER BY last_used DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FrecencyEntry
	for rows.Next() {
		var e FrecencyEntry
		if err := rows.Scan(&e.Key, &e.Count, &e.LastUsed); err != nil {
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

	rows, err := d.conn.Query("SELECT key, count, last_used FROM usage_frecency ORDER BY count DESC, last_used DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FrecencyEntry
	for rows.Next() {
		var e FrecencyEntry
		if err := rows.Scan(&e.Key, &e.Count, &e.LastUsed); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}
