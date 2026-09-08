package db

import (
	"fmt"
	"strings"
	"time"
)

// ScheduledTask 定时任务
// 动作(action)：app=打开软件/文件  dir=打开目录  url=打开网页  command=执行命令  http=HTTP 请求
// 调度(scheduleKind)：once=一次性  interval=固定间隔  daily=每天  weekly=每周
type ScheduledTask struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Action       string `json:"action"`      // app | dir | url | command | http
	Target       string `json:"target"`      // 路径 / 网址 / 命令 / http url
	WorkingDir   string `json:"workingDir"`  // app/command 的工作目录
	HTTPMethod   string `json:"httpMethod"`  // GET/POST/...
	HTTPHeaders  string `json:"httpHeaders"` // 每行 "Key: Value"
	HTTPBody     string `json:"httpBody"`
	ScheduleKind string `json:"scheduleKind"` // once | interval | daily | weekly
	RunAt        string `json:"runAt"`        // once：YYYY-MM-DD HH:MM:SS
	IntervalSec  int    `json:"intervalSec"`  // interval：间隔秒数
	TimeOfDay    string `json:"timeOfDay"`    // daily/weekly：HH:MM:SS
	Weekdays     string `json:"weekdays"`     // weekly：CSV，0=周日..6=周六
	Enabled      bool   `json:"enabled"`
	Notify       bool   `json:"notify"`     // 执行后是否发系统通知
	NextRun      string `json:"nextRun"`    // 下次运行 YYYY-MM-DD HH:MM:SS（'' 表示不再运行）
	LastRun      string `json:"lastRun"`    // 上次运行 YYYY-MM-DD HH:MM:SS
	LastStatus   string `json:"lastStatus"` // ok | fail | ''
	LastResult   string `json:"lastResult"` // 上次运行简要结果
	Sort         int    `json:"sort"`
	CreatedAt    string `json:"createdAt"`
}

const schedCols = `id, name, action, target, working_dir, http_method, http_headers, http_body,
	schedule_kind, run_at, interval_sec, time_of_day, weekdays, enabled, notify,
	next_run, last_run, last_status, last_result, sort, created_at`

func scanScheduledTask(rows interface{ Scan(...interface{}) error }) (ScheduledTask, error) {
	var t ScheduledTask
	var enabled, notify int
	err := rows.Scan(&t.ID, &t.Name, &t.Action, &t.Target, &t.WorkingDir,
		&t.HTTPMethod, &t.HTTPHeaders, &t.HTTPBody,
		&t.ScheduleKind, &t.RunAt, &t.IntervalSec, &t.TimeOfDay, &t.Weekdays,
		&enabled, &notify, &t.NextRun, &t.LastRun, &t.LastStatus, &t.LastResult,
		&t.Sort, &t.CreatedAt)
	t.Enabled = enabled != 0
	t.Notify = notify != 0
	return t, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateScheduledTask 新建定时任务（next_run 由调用方预先算好传入 t.NextRun）
func (d *Database) CreateScheduledTask(t *ScheduledTask) (*ScheduledTask, error) {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return nil, fmt.Errorf("任务名称不能为空")
	}
	if strings.TrimSpace(t.Target) == "" {
		return nil, fmt.Errorf("执行目标不能为空")
	}
	if t.ID == "" {
		t.ID = newID()
	}
	t.CreatedAt = time.Now().Format(time.RFC3339)

	var maxSort int
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.conn.QueryRow("SELECT COALESCE(MAX(sort), 0) FROM scheduled_tasks").Scan(&maxSort)
	t.Sort = maxSort + 1

	_, err := d.conn.Exec(
		`INSERT INTO scheduled_tasks
			(id, name, action, target, working_dir, http_method, http_headers, http_body,
			 schedule_kind, run_at, interval_sec, time_of_day, weekdays, enabled, notify,
			 next_run, last_run, last_status, last_result, sort, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?)`,
		t.ID, t.Name, t.Action, t.Target, t.WorkingDir, t.HTTPMethod, t.HTTPHeaders, t.HTTPBody,
		t.ScheduleKind, t.RunAt, t.IntervalSec, t.TimeOfDay, t.Weekdays, b2i(t.Enabled), b2i(t.Notify),
		t.NextRun, t.Sort, t.CreatedAt,
	)
	return t, err
}

// ListScheduledTasks 返回全部定时任务（按 sort、创建时间）
func (d *Database) ListScheduledTasks() ([]ScheduledTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + schedCols + ` FROM scheduled_tasks ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetScheduledTask 按 ID 查询
func (d *Database) GetScheduledTask(id string) (*ScheduledTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+schedCols+` FROM scheduled_tasks WHERE id = ?`, id)
	t, err := scanScheduledTask(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateScheduledTask 更新可编辑字段与 next_run（调用方预先算好 t.NextRun）
func (d *Database) UpdateScheduledTask(t *ScheduledTask) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("任务名称不能为空")
	}
	if strings.TrimSpace(t.Target) == "" {
		return fmt.Errorf("执行目标不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE scheduled_tasks SET
			name = ?, action = ?, target = ?, working_dir = ?, http_method = ?, http_headers = ?, http_body = ?,
			schedule_kind = ?, run_at = ?, interval_sec = ?, time_of_day = ?, weekdays = ?, enabled = ?, notify = ?,
			next_run = ?
		 WHERE id = ?`,
		t.Name, t.Action, t.Target, t.WorkingDir, t.HTTPMethod, t.HTTPHeaders, t.HTTPBody,
		t.ScheduleKind, t.RunAt, t.IntervalSec, t.TimeOfDay, t.Weekdays, b2i(t.Enabled), b2i(t.Notify),
		t.NextRun, t.ID,
	)
	return err
}

// DeleteScheduledTask 删除
func (d *Database) DeleteScheduledTask(id string) error {
	return d.DeleteWhere("scheduled_tasks", "id = ?", id)
}

// SetTaskEnabled 启用/停用，并更新 next_run（重新排期或清空）
func (d *Database) SetTaskEnabled(id string, enabled bool, nextRun string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE scheduled_tasks SET enabled = ?, next_run = ? WHERE id = ?",
		b2i(enabled), nextRun, id)
	return err
}

// ListDueTasks 返回「已启用、有下次运行时间、已到点」的任务
func (d *Database) ListDueTasks(now string) ([]ScheduledTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT `+schedCols+` FROM scheduled_tasks
		 WHERE enabled = 1 AND next_run <> '' AND next_run <= ?
		 ORDER BY next_run ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListEnabledWithNextRun 返回「已启用且有下次运行时间」的任务（供调度器计算最近等待时长）
func (d *Database) ListEnabledWithNextRun() ([]ScheduledTask, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT `+schedCols+` FROM scheduled_tasks
		 WHERE enabled = 1 AND next_run <> ''
		 ORDER BY next_run ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTaskRunResult 记录一次运行结果，并写入下次运行时间与启用状态
func (d *Database) SetTaskRunResult(id, lastRun, status, result, nextRun string, enabled bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(result) > 500 {
		result = result[:500]
	}
	_, err := d.conn.Exec(
		`UPDATE scheduled_tasks SET last_run = ?, last_status = ?, last_result = ?, next_run = ?, enabled = ? WHERE id = ?`,
		lastRun, status, result, nextRun, b2i(enabled), id,
	)
	return err
}
