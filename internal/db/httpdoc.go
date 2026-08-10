package db

import (
	"database/sql"
	"errors"
)

// HttpDoc 目录下的 Markdown 文档（轻量笔记），folder_id 指向所属目录。
type HttpDoc struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	FolderID  string `json:"folderId"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const httpDocColumns = `id, project_id, folder_id, name, content, sort, created_at, updated_at`

func scanHttpDoc(s interface {
	Scan(dest ...interface{}) error
}) (*HttpDoc, error) {
	var r HttpDoc
	if err := s.Scan(&r.ID, &r.ProjectID, &r.FolderID, &r.Name, &r.Content, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListHttpDocs 返回某项目下全部文档（含子目录），按 sort、created_at 排序。
func (d *Database) ListHttpDocs(projectID string) ([]HttpDoc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT `+httpDocColumns+` FROM http_docs WHERE project_id=? ORDER BY sort ASC, created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpDoc
	for rows.Next() {
		r, err := scanHttpDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetHttpDoc 按 ID 取单个，未找到返回 (nil, nil)。
func (d *Database) GetHttpDoc(id string) (*HttpDoc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+httpDocColumns+` FROM http_docs WHERE id = ?`, id)
	r, err := scanHttpDoc(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// CreateHttpDoc 新建文档（ID/时间戳缺失时自动补全）。
func (d *Database) CreateHttpDoc(r *HttpDoc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	r.UpdatedAt = r.CreatedAt
	_, err := d.conn.Exec(`INSERT INTO http_docs (id, project_id, folder_id, name, content, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.FolderID, r.Name, r.Content, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

// UpdateHttpDoc 按 ID 更新文档（updated_at 自动刷新）。
func (d *Database) UpdateHttpDoc(r *HttpDoc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.UpdatedAt = now()
	_, err := d.conn.Exec(`UPDATE http_docs SET name=?, content=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Content, r.Sort, r.UpdatedAt, r.ID)
	return err
}

// DeleteHttpDoc 删除文档。
func (d *Database) DeleteHttpDoc(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM http_docs WHERE id=?`, id)
	return err
}
