package db

import (
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
	Trigger    string `json:"trigger"` // manual | hotkey | palette
}

const maxPluginExecLogs = 500

func scanPluginExecLog(rows interface{ Scan(...interface{}) error }) (PluginExecLog, error) {
	var l PluginExecLog
	var success int
	err := rows.Scan(&l.ID, &l.PluginID, &l.CommandID, &l.ExecutedAt, &l.ExecutedTs,
		&success, &l.DurationMs, &l.Result, &l.Error, &l.Trigger)
	l.Success = success != 0
	return l, err
}

// AddPluginExecLog 写入一条执行日志，并按需裁剪到 maxPluginExecLogs 条
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
func (d *Database) ListPluginExecLogs(limit int) ([]PluginExecLog, error) {
	if limit <= 0 {
		limit = 100
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, plugin_id, command_id, executed_at, executed_ts, success, duration_ms, result, error, trigger
		 FROM plugin_exec_logs ORDER BY executed_ts DESC LIMIT ?`, limit)
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
