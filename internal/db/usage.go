package db

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// FrecencyEntry 前端需要的 frecency 数据
type FrecencyEntry struct {
	Key         string `json:"key"`
	Type        string `json:"type"`        // item | note | plugin | system | app
	Label       string `json:"label"`       // 显示标题
	Description string `json:"description"` // 副标题
	Input       string `json:"input"`       // 插件命令的附加输入（如端口号），供「最近使用」回放
	Count       int    `json:"count"`
	LastUsed    int64  `json:"lastUsed"` // Unix ms，与前端 Date.now() 单位一致
}

// RecordUsage 记录一次使用（兼容旧调用，无 metadata）
func (d *Database) RecordUsage(key string) error {
	return d.RecordUsageEx(key, "", "", "", "")
}

// RecordUsageEx 记录一次使用：count+1，last_used 更新，同时存储 type/label/desc/input
func (d *Database) RecordUsageEx(key, type_, label, desc, input string) error {
	now := time.Now().UnixMilli()

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec(
		`INSERT INTO usage_frecency (key, type, label, description, input, count, last_used)
		 VALUES (?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   type = CASE WHEN ? <> '' THEN ? ELSE type END,
		   label = CASE WHEN ? <> '' THEN ? ELSE label END,
		   description = CASE WHEN ? <> '' THEN ? ELSE description END,
		   input = CASE WHEN ? <> '' THEN ? ELSE input END,
		   count = count + 1,
		   last_used = ?`,
		key, type_, label, desc, input, now,
		type_, type_,
		label, label,
		desc, desc,
		input, input,
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
		if err := rows.Scan(&e.Key, &e.Type, &e.Label, &e.Description, &e.Input, &e.Count, &e.LastUsed); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

// GetAllUsage 返回全部 frecency 记录（用于前端初始化一次性加载）
func (d *Database) GetAllUsage() ([]FrecencyEntry, error) {
	return d.queryUsage("SELECT key, type, label, description, input, count, last_used FROM usage_frecency ORDER BY last_used DESC")
}

// ClearUsage 清空使用频率榜（用户手动重置，与逐条记录无关）。
// 清的是「用得频不频」这个统计本身，不是任何业务数据——重置后
// 命令面板的「最近使用」与设置里的最常用排行都从零开始攒。
func (d *Database) ClearUsage() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM usage_frecency")
	return err
}

// GetRecentUsage 返回最近使用的 N 条记录（命令面板「最近使用」专用）
func (d *Database) GetRecentUsage(limit int) ([]FrecencyEntry, error) {
	if limit <= 0 {
		limit = 8
	}
	return d.queryUsage("SELECT key, type, label, description, input, count, last_used FROM usage_frecency ORDER BY last_used DESC LIMIT ?", limit)
}

// GetTopUsage 返回使用次数最多的 N 条记录
func (d *Database) GetTopUsage(limit int) ([]FrecencyEntry, error) {
	return d.queryUsage("SELECT key, type, label, description, input, count, last_used FROM usage_frecency ORDER BY count DESC, last_used DESC LIMIT ?", limit)
}

// GetAllPluginUsageCounts 查出所有已安装插件的使用次数，返回 map[pluginID]sum
//
// 归组方式：以 plugins 表里的已安装 id 为前缀（"plugin:<id>."）最长匹配。
// 为什么不能从 key 反解：key = "plugin:{pluginID}.{commandID}"，而 pluginID
// （io.github.parieses.database）和 commandID（table.rows）都自带点号，
// 无论按第一个还是最后一个 "." 切分都会切错 —— 反解出的 pid 变成
// io.github.parieses.database.table，与任何真实 id 都对不上，计数恒为 0
// （只有"命令名不含点"的插件碰巧正确，如 disk-analyzer.disk-scan-root）。
// 卸载时 usage 已被 PurgePlugin 清掉，故只统计已安装 id 足够。
func (d *Database) GetAllPluginUsageCounts() (map[string]int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ids, err := d.installedPluginIDs()
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// 最长 id 优先，避免某个 id 恰好是另一个 id 的前缀时误归组
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })

	rows, err := d.conn.Query("SELECT key, count FROM usage_frecency WHERE key LIKE 'plugin:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var n sql.NullInt64
		if err := rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		if !n.Valid {
			continue
		}
		for _, id := range ids {
			if strings.HasPrefix(key, "plugin:"+id+".") {
				out[id] += int(n.Int64)
				break
			}
		}
	}
	return out, rows.Err()
}

// installedPluginIDs 读取已安装插件 id（调用方须持 d.mu）
func (d *Database) installedPluginIDs() ([]string, error) {
	rows, err := d.conn.Query("SELECT id FROM plugins")
	if err != nil {
		return nil, err
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
