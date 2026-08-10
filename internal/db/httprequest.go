package db

import (
	"database/sql"
	"errors"
)

// ApiRequest 保存的 HTTP 请求（类似 Postman 收藏）。
// Headers 以 JSON map 字符串存储；auth_user/auth_pass 仅本地保存，不作网络传输（明文，单机使用）。
type ApiRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProjectID string `json:"projectId"`
	FolderID  string `json:"folderId"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Headers   string `json:"headers"`
	Body      string `json:"body"`
	BodyType  string `json:"bodyType"`
	AuthType  string `json:"authType"`
	AuthToken string `json:"authToken"`
	AuthUser  string `json:"authUser"`
	AuthPass  string `json:"authPass"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const apiRequestColumns = `id, name, project_id, folder_id, method, url, headers, body, body_type, auth_type, auth_token, auth_user, auth_pass, sort, created_at, updated_at`

func scanApiRequest(s interface {
	Scan(dest ...interface{}) error
}) (*ApiRequest, error) {
	var r ApiRequest
	err := s.Scan(&r.ID, &r.Name, &r.ProjectID, &r.FolderID, &r.Method, &r.URL, &r.Headers, &r.Body,
		&r.BodyType, &r.AuthType, &r.AuthToken, &r.AuthUser, &r.AuthPass,
		&r.Sort, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if r.Headers == "" {
		r.Headers = "{}"
	}
	return &r, nil
}

// ListApiRequests 返回全部保存的请求，按 sort、created_at 排序。
func (d *Database) ListApiRequests() ([]ApiRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + apiRequestColumns + ` FROM api_requests ORDER BY sort ASC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApiRequest
	for rows.Next() {
		r, err := scanApiRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetApiRequest 按 ID 取单个，未找到返回 (nil, nil)。
func (d *Database) GetApiRequest(id string) (*ApiRequest, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+apiRequestColumns+` FROM api_requests WHERE id = ?`, id)
	r, err := scanApiRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// CreateApiRequest 新建保存的请求（ID/时间戳缺失时自动补全）。
func (d *Database) CreateApiRequest(r *ApiRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	r.UpdatedAt = r.CreatedAt
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.conn.Exec(`INSERT INTO api_requests
		(id, name, project_id, folder_id, method, url, headers, body, body_type, auth_type, auth_token, auth_user, auth_pass, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.ProjectID, r.FolderID, r.Method, r.URL, r.Headers, r.Body, r.BodyType,
		r.AuthType, r.AuthToken, r.AuthUser, r.AuthPass, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

// UpdateApiRequest 按 ID 更新已有请求（updated_at 自动刷新）。
func (d *Database) UpdateApiRequest(r *ApiRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.UpdatedAt = now()
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.conn.Exec(`UPDATE api_requests SET
		name=?, project_id=?, folder_id=?, method=?, url=?, headers=?, body=?, body_type=?, auth_type=?,
		auth_token=?, auth_user=?, auth_pass=?, sort=?, updated_at=?
		WHERE id=?`,
		r.Name, r.ProjectID, r.FolderID, r.Method, r.URL, r.Headers, r.Body, r.BodyType, r.AuthType,
		r.AuthToken, r.AuthUser, r.AuthPass, r.Sort, r.UpdatedAt, r.ID)
	return err
}

// DeleteApiRequest 删除保存的请求。
func (d *Database) DeleteApiRequest(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM api_requests WHERE id = ?`, id)
	return err
}

// ReorderApiRequests 将某容器（projectID + folderID）下的请求按 ids 顺序重排，
// 并一并更新 project_id / folder_id（支持拖拽排序与跨目录、跨项目移动）。
func (d *Database) ReorderApiRequests(projectID, folderID string, ids []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, id := range ids {
		if _, err := d.conn.Exec(`UPDATE api_requests SET project_id=?, folder_id=?, sort=? WHERE id=?`,
			projectID, folderID, i, id); err != nil {
			return err
		}
	}
	return nil
}
