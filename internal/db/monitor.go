package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// 监控探测类型。同一张表同时承载六类探针，而不是另建「服务监控」表：
// 它们共用 interval/timeout/告警/静默/日志/在线率这一整套运行时，拆表等于把
// 检查循环、日志裁剪、统计聚合全部复制一份；差异只在「怎么判 up」这一处。
const (
	MonitorKindHTTP    = "http"    // 起 HTTP 请求，按状态码/内容判定（默认，兼容旧数据）
	MonitorKindTCP     = "tcp"     // 连一次 host:port，连上即 up（数据库/Redis/MQ 端口探活）
	MonitorKindProcess = "process" // 本机进程是否存在（守护进程、容器外的原生服务）
	MonitorKindCmd     = "cmd"     // 执行一条命令，退出码 0 即 up（自定义健康脚本）
	MonitorKindDNS     = "dns"     // 解析一次域名，拿到结果即 up（复用 target 字段）
	MonitorKindPing    = "ping"    // 调系统 ping 一次，收到回包即 up（复用 target 字段）
)

// RecycleMonitor 是全局回收站里「监控」这一类主体的 collection 值。
// 它不写 tombstones：监控靠 deleted_at 软删并保留日志，恢复只清标记。
// 常量放这里而不是 tombstone.go，是为了让「这类主体不走墓碑」一眼可见。
const RecycleMonitor = "monitors"

// Monitor 运行状态监控（仿 UptimeRobot）
// expected_status 支持 "2xx"/"3xx"/"4xx"/"5xx"、精确码如 "200"，
// 或 "any"（任意非 0 状态码即视为正常，用于需要鉴权的健康检查等）。
// 注意 "any" 必须以字面量落库；空串会被下面的 normalize 归一成默认的 "2xx"。
type Monitor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind 探测类型，见 MonitorKind*。空值按 http 处理：老数据的列默认值就是 'http'，
	// 但 structToMap / 手写 SQL 之外还有一条「回收站恢复整行转插入」的路径会带空串过来。
	Kind string `json:"kind"`
	// Target 非 http 类型的目标：
	//   tcp     → host:port（如 127.0.0.1:3306）
	//   process → 进程名（不区分大小写，可省略 .exe，如 mysqld）
	//   cmd     → 要执行的命令（退出码 0 视为正常）
	//   dns     → [A:|AAAA:|MX:|CNAME:]主机名（如 A:example.com）
	//   ping    → 主机名或 IP（如 192.168.1.1 或 ::1）
	// http 不用它，仍读 URL —— URL 还牵着证书检查、地址校验与 MCP 描述，
	// 把 http 也搬过来只会让「旧的 url 列」变成需要兼容的空壳。
	Target              string `json:"target"`
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
	// 静默窗口：unix 秒，>now 期间照常探测、照常写日志，只是不推送告警。
	// 与 MaintenanceUntil 是两件事：维护窗口是「别探测」（会留下数据缺口），
	// 静默是「探测但别吵我」——凌晨备份导致的间歇抖动正属于后者。
	MuteUntil int64 `json:"muteUntil"`
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
	// p95/p99 只统计近 24h 内 status='up' 且 latency_ms>0 的样本（nearest-rank）。
	// 无成功样本的监控保持 0，调用方可用它区分「没数据」和「真的 0ms」。
	P95LatencyMs int    `json:"p95LatencyMs"`
	P99LatencyMs int    `json:"p99LatencyMs"`
	LastDownAt   string `json:"lastDownAt"` // 最近一次 down 的检测时间
}

const monCols = `id, name, kind, target, url, method, interval_sec, timeout_sec, expected_status,
	follow_redirects, enabled, notify_down, notify_up,
	last_status, last_checked_at, last_checked_ts, last_latency_ms, last_status_code, last_error,
	skip_tls_verify,
	cert_warn_days, cert_expires_at, last_cert_warned,
	content_match_type, content_match_pattern,
	down_alert_threshold, consecutive_down,
	group_name, latency_alert_ms,
	http_headers, http_body,
	sort, created_at, deleted_at, maintenance_until, mute_until`

const logWindowSec = int64(24 * 3600) // 统计窗口：近 24 小时
// maxLogsPerMonitor 每个监控保留的检测日志上限。
//
// 原来是 1000 条：60 秒间隔只能存 16.7 小时，而前端却提供「7d / 全部」两个范围——
// 选项名跟数据完全对不上。20000 条在 60s 间隔下约 14 天、5min 间隔约 69 天，
// 单条 ~120B，最坏也就 2.4MB/监控。裁剪是索引上的 O(1) DELETE（见 AddMonitorLog），
// 提高上限不会拖慢写入。
const maxLogsPerMonitor = 20000

// monitorLogTrimInterval 每写多少条日志才做一次「COUNT + 裁剪」。
//
// 原先每条日志都先 SELECT COUNT(*) 再决定要不要删，等于每次监控探测都要为一次
// 写付一次全表计数（带 monitor_id 索引，但日志涨到两万条后仍是几万行扫描）。
// 裁剪本来就是「别让表无限长」的兜底，晚 16 条再执行完全无所谓：单 monitor
// 最多临时超出上限 16 条，且每个 monitor 自己的写入都会周期性触发裁剪。
// 与 activity.go 的 activityTrimInterval 是同一套做法。
const monitorLogTrimInterval = 16

var monitorLogTrimTick atomic.Int64

// normalizeMonitorKind 把空串与未知值收敛到 http。
//
// 探针、界面、统计都只认这六个值；脏数据（老库恢复、手改 DB、插件写入）落到
// 「未知 kind」时若原样返回，会表现为「这条监控永远不检测」——收敛到 http
// 至少让它按升级前的行为跑，用户能看见它红了/绿了，而不是一片空白。
// 合法性校验在 services/monitor 保存前做，那一层要能对用户报错，这里只兜底。
func normalizeMonitorKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case MonitorKindTCP:
		return MonitorKindTCP
	case MonitorKindProcess:
		return MonitorKindProcess
	case MonitorKindCmd:
		return MonitorKindCmd
	case MonitorKindDNS:
		return MonitorKindDNS
	case MonitorKindPing:
		return MonitorKindPing
	case MonitorKindHTTP, "":
		return MonitorKindHTTP
	default:
		return MonitorKindHTTP
	}
}

func scanMonitor(rows interface{ Scan(...interface{}) error }) (Monitor, error) {
	var m Monitor
	var follow, enabled, notifyDown, notifyUp, skipTLS int
	var certWarn, alertThresh, latencyAlert int
	var contentMatchType, contentMatchPattern, groupName string
	err := rows.Scan(&m.ID, &m.Name, &m.Kind, &m.Target, &m.URL, &m.Method, &m.IntervalSec, &m.TimeoutSec,
		&m.ExpectedStatus, &follow, &enabled, &notifyDown, &notifyUp,
		&m.LastStatus, &m.LastCheckedAt, &m.LastCheckedTs, &m.LastLatencyMs, &m.LastStatusCode,
		&m.LastError, &skipTLS,
		&certWarn, &m.CertExpiresAt, &m.LastCertWarned,
		&contentMatchType, &contentMatchPattern,
		&alertThresh, &m.ConsecutiveDown,
		&groupName, &latencyAlert,
		&m.HTTPHeaders, &m.HTTPBody,
		&m.Sort, &m.CreatedAt, &m.DeletedAt, &m.MaintenanceUntil, &m.MuteUntil)
	m.Kind = normalizeMonitorKind(m.Kind)
	m.Target = strings.TrimSpace(m.Target)
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
	m.Kind = normalizeMonitorKind(m.Kind)
	m.Target = strings.TrimSpace(m.Target)
	if m.Kind == MonitorKindHTTP {
		if strings.TrimSpace(m.URL) == "" {
			return nil, fmt.Errorf("监控地址不能为空")
		}
	} else if m.Target == "" {
		// 非 http 类型没有 target 就是一条「永远失败」的监控，且失败原因会是
		// 「连接空地址」这种看不懂的话，故在入口挡掉。
		return nil, fmt.Errorf("探测目标不能为空")
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
			(id, name, kind, target, url, method, interval_sec, timeout_sec, expected_status,
			 follow_redirects, enabled, notify_down, notify_up,
			 last_status, last_checked_at, last_checked_ts, last_latency_ms, last_status_code, last_error,
			 skip_tls_verify,
			 cert_warn_days, cert_expires_at, last_cert_warned,
			 content_match_type, content_match_pattern,
			 down_alert_threshold, consecutive_down,
			 group_name, latency_alert_ms,
			 http_headers, http_body,
			 sort, created_at, deleted_at, maintenance_until, mute_until)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 0, 0, 0, '', ?,
			 ?, 0, 0, ?, ?,
			 ?, 0, ?, ?,
			 ?, ?,
			 ?, ?, '', 0, 0)`,
		m.ID, m.Name, m.Kind, m.Target, m.URL, m.Method, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus,
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	// 只有 HTTP 探测读 url 列；tcp/process/cmd/dns/ping 都读 target。
	// 无条件要求 url 会让这些类型在「编辑保存」时直接失败，而界面上根本没让它填 URL。
	if normalizeMonitorKind(m.Kind) == MonitorKindHTTP && strings.TrimSpace(m.URL) == "" {
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
	m.Kind = normalizeMonitorKind(m.Kind)
	m.Target = strings.TrimSpace(m.Target)
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		`UPDATE monitors SET
			name = ?, kind = ?, target = ?, url = ?, method = ?, interval_sec = ?, timeout_sec = ?, expected_status = ?,
			follow_redirects = ?, enabled = ?, notify_down = ?, notify_up = ?,
			skip_tls_verify = ?,
			cert_warn_days = ?, content_match_type = ?, content_match_pattern = ?,
			down_alert_threshold = ?, group_name = ?, latency_alert_ms = ?,
			http_headers = ?, http_body = ?
		 WHERE id = ?`,
		m.Name, m.Kind, m.Target, m.URL, m.Method, m.IntervalSec, m.TimeoutSec, m.ExpectedStatus,
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

// PurgeDeletedMonitors 清空监控回收站：硬删所有软删行及其日志。
// 与 ClearTombstones 分开做，是因为监控从没写过墓碑 —— 只清墓碑会让
// 「清空回收站」悄悄留下一批看得见却清不掉的监控。
func (d *Database) PurgeDeletedMonitors() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM monitor_logs WHERE monitor_id IN (SELECT id FROM monitors WHERE deleted_at <> '')"); err != nil {
		return 0, err
	}
	res, err := tx.Exec("DELETE FROM monitors WHERE deleted_at <> ''")
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PurgeExpiredDeletedMonitors 按与墓碑一致的保留天数清理过期软删监控。
// deleted_at 是带时区偏移的 RFC3339，字符串比较会误判，必须走 julianday()。
func (d *Database) PurgeExpiredDeletedMonitors(maxAgeDays int) (int, error) {
	if maxAgeDays <= 0 {
		maxAgeDays = TombstoneMaxAgeDays
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339)
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	where := "deleted_at <> '' AND julianday(deleted_at) < julianday(?)"
	if _, err = tx.Exec("DELETE FROM monitor_logs WHERE monitor_id IN (SELECT id FROM monitors WHERE "+where+")", cutoff); err != nil {
		return 0, err
	}
	res, err := tx.Exec("DELETE FROM monitors WHERE "+where, cutoff)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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

// SetMonitorMute 告警静默：until=0 取消。探测与日志照常，只是不推送通知。
func (d *Database) SetMonitorMute(id string, until int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE monitors SET mute_until = ? WHERE id = ?", until, id)
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
	// 裁剪攒批：INSERT 已经落库，绝大多数写入到这里就可以直接返回。
	if monitorLogTrimTick.Add(1)%monitorLogTrimInterval != 0 {
		return nil
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	d.mu.RLock()
	defer d.mu.RUnlock()
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

// ListMonitorStats 一次性汇总所有监控近 24h 的在线率/平均延迟/最近故障时间，
// 并补算成功样本的 p95/p99 延迟分位。
func (d *Database) ListMonitorStats() ([]MonitorStat, error) {
	since := time.Now().Unix() - logWindowSec
	d.mu.RLock()
	defer d.mu.RUnlock()

	// 第一条：原有聚合（在线率/平均延迟/最近故障）。
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
	defer rows.Close() // 幂等：出错路径也能关掉
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 显式关闭，让第二条查询复用连接（连接池虽为 4，但别依赖这个假设）。
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// 第二条：一次取回全量成功延迟样本，Go 侧算分位。
	// SQLite 没有 percentile_cont，逐条查又是 N+1；按 (monitor_id, latency_ms)
	// 排好序后单遍扫描即可按 id 分组，组内天然升序。单监控日志上限 2 万条，可接受。
	// 只统计 status='up' 且 latency_ms>0：失败探测的延迟是超时值，不该混进分位。
	latRows, err := d.conn.Query(
		`SELECT monitor_id, latency_ms FROM monitor_logs
		 WHERE checked_ts >= ? AND status = 'up' AND latency_ms > 0
		 ORDER BY monitor_id, latency_ms`, since)
	if err != nil {
		return nil, err
	}
	defer latRows.Close()

	// 回填目标按 id 索引；out 已定型不再 append，指针稳定。
	byID := make(map[string]*MonitorStat, len(out))
	for i := range out {
		byID[out[i].MonitorID] = &out[i]
	}

	var curID string
	var samples []int
	flush := func() {
		if len(samples) == 0 {
			return
		}
		if st, ok := byID[curID]; ok {
			st.P95LatencyMs = percentileNearestRank(samples, 95)
			st.P99LatencyMs = percentileNearestRank(samples, 99)
		}
	}
	for latRows.Next() {
		var id string
		var ms int
		if err := latRows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		if id != curID {
			flush()
			curID = id
			samples = samples[:0]
		}
		samples = append(samples, ms)
	}
	flush()
	if err := latRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// percentileNearestRank 返回升序样本里第 p 百分位（nearest-rank）对应的延迟值。
// 索引 = ceil(p/100 * n) - 1；用整数 (p*n + 99)/100 精确表达 ceil，避免浮点边界
// 把 20 个样本的 95 分位算成第 20 个（应为第 19 个）。调用方保证 sorted 非空且升序。
func percentileNearestRank(sorted []int, p int) int {
	n := len(sorted)
	idx := (p*n+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
