package db

import (
	"fmt"
	"sync/atomic"
	"time"
)

// PluginExecLog 插件命令执行日志（5.2）
type PluginExecLog struct {
	ID         string `json:"id"`
	PluginID   string `json:"pluginId"`
	CommandID  string `json:"commandId"`
	ExecutedAt string `json:"executedAt"`
	ExecutedTs int64  `json:"executedTs"`
	Success    bool   `json:"success"`
	DurationMs int    `json:"durationMs"`
	Result     string `json:"result"` // 执行返回（截断存储，首 2000 字符）
	Error      string `json:"error"`
	Trigger    string `json:"trigger"` // manual | hotkey | mcp
}

const maxPluginExecLogs = 500

// execLogTrimTick 周期性裁剪计数器：避免每次写入都跑 COUNT(*)，消除高频命令
// （如 task-status 每秒轮询）对 plugin_exec_logs 的写放大。
var execLogTrimTick int64

// execLogTrimInterval 每多少次插入才检查一次裁剪。把 O(n) 的 COUNT(*) 从
// 「每写必查」降为「低频抽查」，高频轮询命令的 DB 压力下降约该倍数。
const execLogTrimInterval = 50

func scanPluginExecLog(rows interface{ Scan(...interface{}) error }) (PluginExecLog, error) {
	var l PluginExecLog
	var success int
	err := rows.Scan(&l.ID, &l.PluginID, &l.CommandID, &l.ExecutedAt, &l.ExecutedTs,
		&success, &l.DurationMs, &l.Result, &l.Error, &l.Trigger)
	l.Success = success != 0
	return l, err
}

// AddPluginExecLog 写入一条执行日志，并按需裁剪到 maxPluginExecLogs 条。
// 裁剪不再每次插入都触发，而是每 execLogTrimInterval 次插入检查一次，
// 避免高频命令把日志表当成轮询计数器、拖慢整库写入。
func (d *Database) AddPluginExecLog(l *PluginExecLog) error {
	l.ID = newID()
	l.ExecutedAt = time.Now().Format(time.RFC3339)
	l.ExecutedTs = time.Now().Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(
		`INSERT INTO plugin_exec_logs
			(id, plugin_id, command_id, executed_at, executed_ts, success, duration_ms, result, error, trigger)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.PluginID, l.CommandID, l.ExecutedAt, l.ExecutedTs, b2i(l.Success),
		l.DurationMs, l.Result, l.Error, l.Trigger,
	); err != nil {
		return err
	}
	if atomic.AddInt64(&execLogTrimTick, 1)%execLogTrimInterval != 0 {
		return nil
	}
	var cnt int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM plugin_exec_logs").Scan(&cnt); err != nil {
		return err
	}
	if cnt > maxPluginExecLogs {
		if _, err := d.conn.Exec(
			`DELETE FROM plugin_exec_logs WHERE id NOT IN (
				SELECT id FROM plugin_exec_logs ORDER BY executed_ts DESC LIMIT ?)`, maxPluginExecLogs); err != nil {
			return err
		}
	}
	return nil
}

// ListPluginExecLogs 返回最近 limit 条执行日志（按时间倒序）
// DeletePluginExecLogs 删除某插件的全部执行日志（卸载时调用）。
// 插件已卸载，这些日志再无查询入口，留着只是孤儿数据。
func (d *Database) DeletePluginExecLogs(pluginID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec("DELETE FROM plugin_exec_logs WHERE plugin_id = ?", pluginID); err != nil {
		return fmt.Errorf("删除插件执行日志失败: %w", err)
	}
	return nil
}

func (d *Database) ListPluginExecLogs(limit int) ([]PluginExecLog, error) {
	return d.ListPluginExecLogsBy("", limit)
}

// ListPluginExecLogsBy 返回最近 limit 条执行日志，pluginID 非空时只取该插件的记录。
//
// 过滤放在 SQL 而不是取回后内存筛：日志表按时间倒序，内存筛会先被别的插件
// （尤其 1 秒一次的 -status 轮询，虽然不落库，但其它高频命令会）挤掉目标记录，
// 表现为「明明刚跑过，历史里查不到」。
func (d *Database) ListPluginExecLogsBy(pluginID string, limit int) ([]PluginExecLog, error) {
	if limit <= 0 {
		limit = 100
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	const cols = `SELECT id, plugin_id, command_id, executed_at, executed_ts, success, duration_ms, result, error, trigger
		 FROM plugin_exec_logs`
	q := cols
	args := make([]interface{}, 0, 2)
	if pluginID != "" {
		q += ` WHERE plugin_id = ?`
		args = append(args, pluginID)
	}
	q += ` ORDER BY executed_ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PluginExecLog
	for rows.Next() {
		l, err := scanPluginExecLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
