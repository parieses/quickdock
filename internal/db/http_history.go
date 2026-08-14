package db

import (
	"database/sql"
	"errors"
	"time"
)

// HttpRequestHistory 一次 HTTP 请求的执行历史（最近 N 条）。
// 记录发送前的完整请求快照 + 发送后的状态码/耗时/体积，供"最近使用/重放"。
type HttpRequestHistory struct {
	ID         string `json:"id"`
	ProjectID  string `json:"projectId"`
	Name       string `json:"name"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Headers    string `json:"headers"`
	Body       string `json:"body"`
	BodyType   string `json:"bodyType"`
	AuthType   string `json:"authType"`
	AuthToken  string `json:"authToken"`
	AuthUser   string `json:"authUser"`
	AuthPass   string `json:"authPass"`
	StatusCode int    `json:"statusCode"`
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"durationMs"`
	Size       int    `json:"size"`
	CreatedTs  int64  `json:"createdTs"`

	// 记录到日志的时间（供前端展示排序）
	CreatedAt string `json:"createdAt"`
}

const httpHistoryColumns = `id, project_id, name, method, url, headers, body, body_type,
	auth_type, auth_token, auth_user, auth_pass,
	status_code, ok, duration_ms, size, created_ts`

func scanHttpHistory(s interface {
	Scan(dest ...interface{}) error
}) (*HttpRequestHistory, error) {
	var h HttpRequestHistory
	var ok int
	err := s.Scan(&h.ID, &h.ProjectID, &h.Name, &h.Method, &h.URL, &h.Headers, &h.Body, &h.BodyType,
		&h.AuthType, &h.AuthToken, &h.AuthUser, &h.AuthPass,
		&h.StatusCode, &ok, &h.DurationMs, &h.Size, &h.CreatedTs)
	if err != nil {
		return nil, err
	}
	h.OK = ok != 0
	if h.Headers == "" {
		h.Headers = "{}"
	}
	return &h, nil
}

// maxHttpHistoryPerProject 每个项目保留的历史条数上限（超出删除最旧的）。
const maxHttpHistoryPerProject = 200

// RecordHttpHistory 写入一条请求历史，并按 project 裁剪到 maxHttpHistoryPerProject 条。
// 返回写入后的记录。
func (d *Database) RecordHttpHistory(h *HttpRequestHistory) (*HttpRequestHistory, error) {
	if h.ID == "" {
		h.ID = newID()
	}
	if h.CreatedTs <= 0 {
		h.CreatedTs = time.Now().UnixMilli()
	}
	if h.Headers == "" {
		h.Headers = "{}"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`INSERT INTO http_request_history
		(id, project_id, name, method, url, headers, body, body_type,
		 auth_type, auth_token, auth_user, auth_pass, status_code, ok, duration_ms, size, created_ts)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.ID, h.ProjectID, h.Name, h.Method, h.URL, h.Headers, h.Body, h.BodyType,
		h.AuthType, h.AuthToken, h.AuthUser, h.AuthPass, h.StatusCode, b2i(h.OK), h.DurationMs, h.Size, h.CreatedTs)
	if err != nil {
		return nil, err
	}
	// 裁剪：仅对同一 project 保留最近 maxHttpHistoryPerProject 条（用 created_ts 阈值）
	if _, err := d.conn.Exec(
		`DELETE FROM http_request_history WHERE project_id = ? AND created_ts < (
			SELECT created_ts FROM http_request_history WHERE project_id = ? ORDER BY created_ts DESC LIMIT 1 OFFSET ?)`,
		h.ProjectID, h.ProjectID, maxHttpHistoryPerProject); err != nil {
		return nil, err
	}
	return h, nil
}

// ListHttpHistory 返回某项目最近 limit 条历史（最新在前）。
// projectID 为空时返回全部项目的历史。
func (d *Database) ListHttpHistory(projectID string, limit int) ([]HttpRequestHistory, error) {
	if limit <= 0 {
		limit = maxHttpHistoryPerProject
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var rows *sql.Rows
	var err error
	if projectID == "" {
		rows, err = d.conn.Query(`SELECT `+httpHistoryColumns+` FROM http_request_history ORDER BY created_ts DESC LIMIT ?`, limit)
	} else {
		rows, err = d.conn.Query(`SELECT `+httpHistoryColumns+` FROM http_request_history WHERE project_id = ? ORDER BY created_ts DESC LIMIT ?`, projectID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpRequestHistory
	for rows.Next() {
		h, err := scanHttpHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// GetHttpHistory 按 ID 取单条，未找到返回 (nil, nil)。
func (d *Database) GetHttpHistory(id string) (*HttpRequestHistory, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+httpHistoryColumns+` FROM http_request_history WHERE id = ?`, id)
	h, err := scanHttpHistory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return h, nil
}

// DeleteHttpHistory 删除一条历史。
func (d *Database) DeleteHttpHistory(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM http_request_history WHERE id = ?`, id)
	return err
}

// ClearHttpHistory 清空某项目（projectID 为空则全部）的历史。
func (d *Database) ClearHttpHistory(projectID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	if projectID == "" {
		_, err = d.conn.Exec(`DELETE FROM http_request_history`)
	} else {
		_, err = d.conn.Exec(`DELETE FROM http_request_history WHERE project_id = ?`, projectID)
	}
	return err
}
