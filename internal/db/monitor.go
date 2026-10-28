package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Monitor 网站运行状态监控（仿 UptimeRobot）
// expected_status 支持 "2xx"/"3xx"/"4xx"/"5xx" 或精确码如 "200"
type Monitor struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	URL                 string `json:"url"`
	Method              string `json:"method"`         // GET/POST/HEAD...
	IntervalSec         int    `json:"intervalSec"`    // 检测间隔（秒）
	TimeoutSec          int    `json:"timeoutSec"`     // 单次请求超时（秒）
	ExpectedStatus      string `json:"expectedStatus"` // 2xx | 3xx | 4xx | 5xx | 200 ...
	FollowRedirects     bool   `json:"followRedirects"`
	Enabled             bool   `json:"enabled"`
	NotifyDown          bool   `json:"notifyDown"`
	NotifyUp            bool   `json:"notifyUp"`
	LastStatus          string `json:"lastStatus"`    // up | down | ''
	LastCheckedAt       string `json:"lastCheckedAt"` // YYYY-MM-DD HH:MM:SS
	LastCheckedTs       int64  `json:"lastCheckedTs"` // unix 秒，供检查器精确计算间隔
	LastLatencyMs       int    `json:"lastLatencyMs"`
	LastStatusCode      int    `json:"lastStatusCode"`
	LastError           string `json:"lastError"`
	SkipTLSVerify       bool   `json:"skipTLSVerify"`    // 忽略证书错误（自签名/过期等），仅单条生效，默认关闭
	CertWarnDays        int    `json:"certWarnDays"`     // SSL 证书提前 N 天告警
	CertExpiresAt       int64  `json:"certExpiresAt"`    // 证书过期时间（unix 秒，0=未知/非HTTPS）
	LastCertWarned      int64  `json:"lastCertWarned"`   // 上次证书告警时间（unix 秒，去抖）
	ContentMatchType    string `json:"contentMatchType"` // none | contains | not_contains | regex
	ContentMatchPattern string `json:"contentMatchPattern"`
	DownAlertThreshold  int    `json:"downAlertThreshold"` // 误报抑制：连续 N 次失败才发宕机告警（>=1）
	ConsecutiveDown     int    `json:"consecutiveDown"`    // 当前连续失败次数（运行态，>=0）
	GroupName           string `json:"groupName"`          // 分组名（空=未分组；列表按它分区展示）
	LatencyAlertMs      int    `json:"latencyAlertMs"`     // 延迟告警阈值 ms（0=关闭；开启则宕机告警还要求当次延迟超过它）
	HTTPHeaders         string `json:"httpHeaders"`        // 自定义请求头："Name: Value" 每行一条（空=不附加）
	HTTPBody            string `json:"httpBody"`           // 请求体（仅 POST/PUT/PATCH 等带 body 的方法发送）
	Sort                int    `json:"sort"`
	CreatedAt           string `json:"createdAt"`
	DeletedAt           string `json:"deletedAt"`        // 软删标记：非空即进回收站，列表默认不显示
	MaintenanceUntil    int64  `json:"maintenanceUntil"` // 维护窗口：unix 秒，>now 期间暂停检测
}

// MonitorLog 单次检测记录
type MonitorLog struct {
	ID         string `json:"id"`
	MonitorID  string `json:"monitorId"`
	CheckedAt  string `json:"checkedAt"`
	CheckedTs  int64  `json:"checkedTs"`
	Status     string `json:"status"` // up | down
	StatusCode int    `json:"statusCode"`
	LatencyMs  int    `json:"latencyMs"`
	Error      string `json:"error"`
}

// MonitorStat 统计指标（近 24 小时）
type MonitorStat struct {
	MonitorID    string  `json:"monitorId"`
	TotalChecks  int     `json:"totalChecks"`
	UpChecks     int     `json:"upChecks"`
	UptimeRatio  float64 `json:"uptimeRatio"` // 0..100，近 24h
	AvgLatencyMs int     `json:"avgLatencyMs"`
	LastDownAt   string  `json:"lastDownAt"` // 最近一次 down 的检测时间
}

const monCols = `id, name, url, method, interval_sec, timeout_sec, expected_status,
	follow_redirects, enabled, notify_down, notify_up,
	last_status, last_checked_at, last_checked_ts, last_latency_ms, last_status_code, last_error,
	skip_tls_verify,
	cert_warn_days, cert_expires_at, last_cert_warned,
	content_match_type, content_match_pattern,
	down_alert_threshold, consecutive_down,
	group_name, latency_alert_ms,
	http_headers, http_body,
	sort, created_at, deleted_at, maintenance_until`

const logWindowSec = int64(24 * 3600) // 统计窗口：近 24 小时
// maxLogsPerMonitor 每个监控保留的检测日志上限。
//
// 原来是 1000 条：60 秒间隔只能存 16.7 小时，而前端却提供「7d / 全部」两个范围——
// 选项名跟数据完全对不上。20000 条在 60s 间隔下约 14 天、5min 间隔约 69 天，
// 单条 ~120B，最坏也就 2.4MB/监控。裁剪是索引上的 O(1) DELETE（见 AddMonitorLog），
// 提高上限不会拖慢写入。
const maxLogsPerMonitor = 20000

func scanMonitor(rows interface{ Scan(...interface{}) error }) (Monitor, error) {
	var m Monitor
	var follow, enabled, notifyDown, notifyUp, skipTLS int
	var certWarn, alertThresh, latencyAlert int
	var contentMatchType, contentMatchPattern, groupName string
	err := rows.Scan(&m.ID, &m.Name, &m.URL, &m.Method, &m.IntervalSec, &m.TimeoutSec,
		&m.ExpectedStatus, &follow, &enabled, &notifyDown, &notifyUp,
		&m.LastStatus, &m.LastCheckedAt, &m.LastCheckedTs, &m.LastLatencyMs, &m.LastStatusCode,
		&m.LastError, &skipTLS,
		&certWarn, &m.CertExpiresAt, &m.LastCertWarned,
		&contentMatchType, &contentMatchPattern,
		&alertThresh, &m.ConsecutiveDown,
		&groupName, &latencyAlert,
		&m.HTTPHeaders, &m.HTTPBody,
		&m.Sort, &m.CreatedAt, &m.DeletedAt, &m.MaintenanceUntil)
	m.FollowRedirects = follow != 0
	m.Enabled = enabled != 0
	m.NotifyDown = notifyDown != 0
	m.NotifyUp = notifyUp != 0
	m.SkipTLSVerify = skipTLS != 0
	m.CertWarnDays = certWarn
	m.DownAlertThreshold = alertThresh
	if m.DownAlertThreshold < 1 {
		m.DownAlertThreshold = 1
	}
	m.ContentMatchType = contentMatchType
	m.ContentMatchPattern = contentMatchPattern
	m.GroupName = strings.TrimSpace(groupName)
	m.LatencyAlertMs = latencyAlert
	if m.LatencyAlertMs < 0 {
		m.LatencyAlertMs = 0
	}
	return m, err
}

// CreateMonitor 新建监控（last_* 留空）
func (d *Database) CreateMonitor(m *Monitor) (*Monitor, error) {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return nil, fmt.Errorf("监控名称不能为空")
	}
	if strings.TrimSpace(m.URL) == "" {
		return nil, fmt.Errorf("监控地址不能为空")
	}
	if m.IntervalSec < 5 {
		m.IntervalSec = 60
	}
	if m.TimeoutSec < 1 {
		m.TimeoutSec = 10
	}
	if m.Method == "" {
		m.Method = "GET"
	}
	if m.ExpectedStatus == "" {
		m.ExpectedStatus = "2xx"
	}
	if m.CertWarnDays <= 0 {
		m.CertWarnDays = 14
	}
	if m.ContentMatchType == "" {
		m.ContentMatchType = "none"
	}
	if m.DownAlertThreshold < 1 {
		m.DownAlertThreshold = 1
	}
	m.GroupName = strings.TrimSpace(m.GroupName)
	if m.LatencyAlertMs < 0 {
		m.LatencyAlertMs = 0
	}
	m.ID = newID()
	m.CreatedAt = time.Now().Format(time.RFC3339)

	var maxSort int
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.conn.QueryRow("SELECT COALESCE(MAX(sort), 0) FROM monitors").Scan(&maxSort)
	m.Sort = maxSort + 1

	_, err := d.conn.Exec(
		`INSERT INTO monitors
			(id, name, url, method, interval_sec, timeout_sec, expected_status,
			 follow_redirects, enabled, notify_down, notify_up,
			 last_status, last_checked_at, last_checked_ts, last_latency_ms, last_status_code, last_error,
			 skip_tls_verify,
			 cert_warn_days, cert_expires_at, last_cert_warned,
			 content_match_type, content_match_pattern,
			 down_alert_threshold, consecutive_down,
			 group_name, latency_alert_ms,
			 http_headers, http_body,
			 sort, created_at, deleted_at, maintenance_until)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 0, 0, 0, '', ?,
			 ?, 0, 0, ?, ?,
			 ?, 0, ?, ?,
			 ?, ?,
			 ?, ?, '', 0)`,
		m.ID, m.Name, m.URL, m.Method, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus,
		b2i(m.FollowRedirects), b2i(m.Enabled), b2i(m.NotifyDown), b2i(m.NotifyUp),
		b2i(m.SkipTLSVerify),
		m.CertWarnDays, m.ContentMatchType, m.ContentMatchPattern,
		m.DownAlertThreshold,
		m.GroupName, m.LatencyAlertMs,
		m.HTTPHeaders, m.HTTPBody,
		m.Sort, m.CreatedAt,
	)
	return m, err
}

// ListMonitors 全部监控（按 sort），默认排除已软删的
func (d *Database) ListMonitors() ([]Monitor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + monCols + ` FROM monitors WHERE deleted_at = '' ORDER BY sort ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMonitor 按 ID 查询
func (d *Database) GetMonitor(id string) (*Monitor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+monCols+` FROM monitors WHERE id = ?`, id)
	m, err := scanMonitor(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMonitor 更新可编辑字段（不改动 last_* 运行数据）
func (d *Database) UpdateMonitor(m *Monitor) error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		return fmt.Errorf("监控名称不能为空")
	}
	if strings.TrimSpace(m.URL) == "" {
		return fmt.Errorf("监控地址不能为空")
	}
	if m.IntervalSec < 5 {
		m.IntervalSec = 60
	}
	if m.TimeoutSec < 1 {
		m.TimeoutSec = 10
	}
	if m.Method == "" {
		m.Method = "GET"
	}
	if m.ExpectedStatus == "" {
		m.ExpectedStatus = "2xx"
	}
	if m.CertWarnDays <= 0 {
		m.CertWarnDays = 14
	}
	if m.ContentMatchType == "" {
		m.ContentMatchType = "none"
	}
	if m.DownAlertThreshold < 1 {
		m.DownAlertThreshold = 1
	}
	m.GroupName = strings.TrimSpace(m.GroupName)
	if m.LatencyAlertMs < 0 {
		m.LatencyAlertMs = 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE monitors SET
			name = ?, url = ?, method = ?, interval_sec = ?, timeout_sec = ?, expected_status = ?,
			follow_redirects = ?, enabled = ?, notify_down = ?, notify_up = ?,
			skip_tls_verify = ?,
			cert_warn_days = ?, content_match_type = ?, content_match_pattern = ?,
			down_alert_threshold = ?, group_name = ?, latency_alert_ms = ?,
			http_headers = ?, http_body = ?
		 WHERE id = ?`,
		m.Name, m.URL, m.Method, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus,
		b2i(m.FollowRedirects), b2i(m.Enabled), b2i(m.NotifyDown), b2i(m.NotifyUp),
		b2i(m.SkipTLSVerify),
		m.CertWarnDays, m.ContentMatchType, m.ContentMatchPattern,
		m.DownAlertThreshold, m.GroupName, m.LatencyAlertMs,
		m.HTTPHeaders, m.HTTPBody,
		m.ID,
	)
	return err
}

// DeleteMonitor 硬删：彻底移除监控及其全部日志（仅用于回收站「彻底删除」）。
// 普通删除请走 SoftDeleteMonitor 进回收站。
func (d *Database) DeleteMonitor(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec("DELETE FROM monitor_logs WHERE monitor_id = ?", id); err != nil {
		return err
	}
	if _, err := d.conn.Exec("DELETE FROM monitors WHERE id = ?", id); err != nil {
		return err
	}
	return nil
}

// SoftDeleteMonitor 软删：打 deleted_at 标记进回收站，保留日志与运行数据，可恢复。
func (d *Database) SoftDeleteMonitor(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET deleted_at = ? WHERE id = ?", time.Now().Format(time.RFC3339), id)
	return err
}

// RestoreMonitor 从回收站恢复：清掉 deleted_at 标记。
func (d *Database) RestoreMonitor(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET deleted_at = '' WHERE id = ?", id)
	return err
}

// ListDeletedMonitors 回收站里的监控（按软删时间倒序）。
func (d *Database) ListDeletedMonitors() ([]Monitor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + monCols + ` FROM monitors WHERE deleted_at <> '' ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMonitorSort 仅改排序（拖拽用），不走 UpdateMonitor 以免覆盖编辑字段或误触发归一化。
func (d *Database) SetMonitorSort(id string, sort int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET sort = ? WHERE id = ?", sort, id)
	return err
}

// SetMonitorMaintenance 设置维护窗口：until=0 表示取消（恢复检测）。
func (d *Database) SetMonitorMaintenance(id string, until int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET maintenance_until = ? WHERE id = ?", until, id)
	return err
}

// SetMonitorEnabled 启用/停用
func (d *Database) SetMonitorEnabled(id string, enabled bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET enabled = ? WHERE id = ?", b2i(enabled), id)
	return err
}

// UpdateMonitorStatus 写入一次检测结果（不写日志）
func (d *Database) UpdateMonitorStatus(id, status, checkedAt string, checkedTs int64, latencyMs, statusCode int, errMsg string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, e := d.conn.Exec(
		`UPDATE monitors SET
			last_status = ?, last_checked_at = ?, last_checked_ts = ?,
			last_latency_ms = ?, last_status_code = ?, last_error = ?
		 WHERE id = ?`,
		status, checkedAt, checkedTs, latencyMs, statusCode, errMsg, id,
	)
	return e
}

// UpdateMonitorConsecutiveDown 更新连续失败计数（运行态，检查器维护）。
// 失败时递增，成功时清零。
func (d *Database) UpdateMonitorConsecutiveDown(id string, n int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, e := d.conn.Exec(`UPDATE monitors SET consecutive_down = ? WHERE id = ?`, n, id)
	return e
}

// UpdateMonitorCert 写入证书过期时间与上次告警时间（由检查器维护，非用户编辑）
func (d *Database) UpdateMonitorCert(id string, expiresAt, lastWarned int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, e := d.conn.Exec(
		`UPDATE monitors SET cert_expires_at = ?, last_cert_warned = ? WHERE id = ?`,
		expiresAt, lastWarned, id,
	)
	return e
}

// AddMonitorLog 写入一条检测日志，并按需裁剪到 maxLogsPerMonitor 条
func (d *Database) AddMonitorLog(l *MonitorLog) error {
	l.ID = newID()
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(
		`INSERT INTO monitor_logs (id, monitor_id, checked_at, checked_ts, status, status_code, latency_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.MonitorID, l.CheckedAt, l.CheckedTs, l.Status, l.StatusCode, l.LatencyMs, l.Error,
	); err != nil {
		return err
	}
	// 裁剪：超出上限时删除最早的一批。
	// 用「保留第 maxLogsPerMonitor 新的那条 checked_ts」作为阈值，删除所有比它更旧的行，
	// 复用 idx_monitor_logs_mid_ts 索引，避免原先 `id NOT IN (SELECT ... LIMIT 1000)` 每次
	// 都物化一整份 id 列表的 O(n) 开销（该索引 ORDER BY checked_ts DESC 可直接命中）。
	var cnt int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM monitor_logs WHERE monitor_id = ?", l.MonitorID).Scan(&cnt); err != nil {
		return err
	}
	if cnt > maxLogsPerMonitor {
		if _, err := d.conn.Exec(
			`DELETE FROM monitor_logs WHERE monitor_id = ? AND checked_ts < (
				SELECT checked_ts FROM monitor_logs WHERE monitor_id = ? ORDER BY checked_ts DESC LIMIT 1 OFFSET ?)`,
			l.MonitorID, l.MonitorID, maxLogsPerMonitor); err != nil {
			return err
		}
	}
	return nil
}

// ClearMonitorLogs 清空某监控的检测日志
func (d *Database) ClearMonitorLogs(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM monitor_logs WHERE monitor_id = ?", id)
	return err
}

// GetMonitorLogs 返回最近 limit 条检测日志（按时间倒序）
func (d *Database) GetMonitorLogs(id string, limit int) ([]MonitorLog, error) {
	if limit <= 0 {
		limit = 50
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, monitor_id, checked_at, checked_ts, status, status_code, latency_ms, error
		 FROM monitor_logs WHERE monitor_id = ? ORDER BY checked_ts DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonitorLog
	for rows.Next() {
		var l MonitorLog
		if err := rows.Scan(&l.ID, &l.MonitorID, &l.CheckedAt, &l.CheckedTs, &l.Status, &l.StatusCode, &l.LatencyMs, &l.Error); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetMonitorLogsSince 返回 checked_ts >= sinceTs 的检测日志（按时间倒序，最多 limit 条）。
// 用于趋势图的时间范围切换（24h / 7d / 全部）。sinceTs=0 表示不限。
func (d *Database) GetMonitorLogsSince(id string, sinceTs, limit int64) ([]MonitorLog, error) {
	if limit <= 0 {
		limit = 2000
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var rows *sql.Rows
	var err error
	if sinceTs > 0 {
		rows, err = d.conn.Query(
			`SELECT id, monitor_id, checked_at, checked_ts, status, status_code, latency_ms, error
			 FROM monitor_logs WHERE monitor_id = ? AND checked_ts >= ? ORDER BY checked_ts DESC LIMIT ?`,
			id, sinceTs, limit)
	} else {
		rows, err = d.conn.Query(
			`SELECT id, monitor_id, checked_at, checked_ts, status, status_code, latency_ms, error
			 FROM monitor_logs WHERE monitor_id = ? ORDER BY checked_ts DESC LIMIT ?`, id, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonitorLog
	for rows.Next() {
		var l MonitorLog
		if err := rows.Scan(&l.ID, &l.MonitorID, &l.CheckedAt, &l.CheckedTs, &l.Status, &l.StatusCode, &l.LatencyMs, &l.Error); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListEnabledMonitors 返回全部已启用监控（供检查器计算最近等待时长）
func (d *Database) ListEnabledMonitors() ([]Monitor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + monCols + ` FROM monitors WHERE enabled = 1 AND deleted_at = '' ORDER BY sort ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMonitorsDue 返回「已启用且到点需检测」的监控（nowTs 为 unix 秒）
func (d *Database) ListMonitorsDue(nowTs int64) ([]Monitor, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT `+monCols+` FROM monitors
		 WHERE enabled = 1 AND deleted_at = '' AND (last_checked_ts = 0 OR last_checked_ts + interval_sec <= ?)
		 ORDER BY last_checked_ts ASC`, nowTs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMonitorStats 一次性汇总所有监控近 24h 的在线率/平均延迟/最近故障时间
func (d *Database) ListMonitorStats() ([]MonitorStat, error) {
	since := time.Now().Unix() - logWindowSec
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT monitor_id,
			COUNT(*),
			SUM(CASE WHEN status = 'up' THEN 1 ELSE 0 END),
			AVG(CASE WHEN status = 'up' THEN latency_ms ELSE NULL END),
			MAX(CASE WHEN status = 'down' THEN checked_at ELSE '' END)
		 FROM monitor_logs WHERE checked_ts >= ? GROUP BY monitor_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MonitorStat
	for rows.Next() {
		var s MonitorStat
		var avgLatency sql.NullFloat64
		if err := rows.Scan(&s.MonitorID, &s.TotalChecks, &s.UpChecks, &avgLatency, &s.LastDownAt); err != nil {
			return nil, err
		}
		if s.TotalChecks > 0 {
			s.UptimeRatio = float64(s.UpChecks) / float64(s.TotalChecks) * 100
		} else {
			s.UptimeRatio = 100
		}
		if avgLatency.Valid {
			s.AvgLatencyMs = int(avgLatency.Float64)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
