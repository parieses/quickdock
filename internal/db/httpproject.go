package db

import (
	"database/sql"
	"errors"
	"strings"
)

// HttpProject 类似 Postman Collection：分组保存请求，并携带项目级共享请求头。
// Headers 以 JSON map 字符串存储；请求自身 Headers 可覆盖同名 key。
type HttpProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Headers   string `json:"headers"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// HttpEnvironment 项目下的环境（dev/prod…），变量在发送时替换进 URL/头/Body/认证。
// Variables 以 JSON 数组字符串存储：[{ "key", "value", "enabled" }]。
type HttpEnvironment struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Variables string `json:"variables"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const httpProjectColumns = `id, name, headers, sort, created_at, updated_at`
const httpEnvColumns = `id, project_id, name, variables, sort, created_at, updated_at`

func scanHttpProject(s interface {
	Scan(dest ...interface{}) error
}) (*HttpProject, error) {
	var r HttpProject
	if err := s.Scan(&r.ID, &r.Name, &r.Headers, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if r.Headers == "" {
		r.Headers = "{}"
	}
	return &r, nil
}

func scanHttpEnvironment(s interface {
	Scan(dest ...interface{}) error
}) (*HttpEnvironment, error) {
	var r HttpEnvironment
	if err := s.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Variables, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if r.Variables == "" {
		r.Variables = "[]"
	}
	return &r, nil
}

// ListHttpProjects 返回全部项目，按 sort、created_at 排序。
func (d *Database) ListHttpProjects() ([]HttpProject, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + httpProjectColumns + ` FROM http_projects ORDER BY sort ASC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpProject
	for rows.Next() {
		r, err := scanHttpProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetHttpProject 按 ID 取单个，未找到返回 (nil, nil)。
func (d *Database) GetHttpProject(id string) (*HttpProject, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+httpProjectColumns+` FROM http_projects WHERE id = ?`, id)
	r, err := scanHttpProject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// CreateHttpProject 新建项目（ID/时间戳缺失时自动补全）。
func (d *Database) CreateHttpProject(r *HttpProject) error {
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
	_, err := d.conn.Exec(`INSERT INTO http_projects (id, name, headers, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`,
		r.ID, r.Name, r.Headers, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

// UpdateHttpProject 按 ID 更新项目（updated_at 自动刷新）。
func (d *Database) UpdateHttpProject(r *HttpProject) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.UpdatedAt = now()
	if r.Headers == "" {
		r.Headers = "{}"
	}
	_, err := d.conn.Exec(`UPDATE http_projects SET name=?, headers=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Headers, r.Sort, r.UpdatedAt, r.ID)
	return err
}

// DeleteHttpProject 删除项目：将其下请求回退为未分类（project_id=''、folder_id=''，
// 保留数据不丢），级联删除该项目全部目录与环境（避免孤儿数据）。
func (d *Database) DeleteHttpProject(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(`UPDATE api_requests SET project_id='', folder_id='' WHERE project_id=?`, id); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM http_environments WHERE project_id=?`, id); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM http_folders WHERE project_id=?`, id); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM http_docs WHERE project_id=?`, id); err != nil {
		return err
	}
	_, err := d.conn.Exec(`DELETE FROM http_projects WHERE id=?`, id)
	return err
}

// ListHttpEnvironments 返回某项目下全部环境，按 sort、created_at 排序。
func (d *Database) ListHttpEnvironments(projectID string) ([]HttpEnvironment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT `+httpEnvColumns+` FROM http_environments WHERE project_id=? ORDER BY sort ASC, created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpEnvironment
	for rows.Next() {
		r, err := scanHttpEnvironment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetHttpEnvironment 按 ID 取单个，未找到返回 (nil, nil)。
func (d *Database) GetHttpEnvironment(id string) (*HttpEnvironment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+httpEnvColumns+` FROM http_environments WHERE id = ?`, id)
	r, err := scanHttpEnvironment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// CreateHttpEnvironment 新建环境（ID/时间戳缺失时自动补全）。
func (d *Database) CreateHttpEnvironment(r *HttpEnvironment) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	r.UpdatedAt = r.CreatedAt
	if r.Variables == "" {
		r.Variables = "[]"
	}
	_, err := d.conn.Exec(`INSERT INTO http_environments (id, project_id, name, variables, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.Name, r.Variables, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

// UpdateHttpEnvironment 按 ID 更新环境（updated_at 自动刷新）。
func (d *Database) UpdateHttpEnvironment(r *HttpEnvironment) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.UpdatedAt = now()
	if r.Variables == "" {
		r.Variables = "[]"
	}
	_, err := d.conn.Exec(`UPDATE http_environments SET name=?, variables=?, sort=?, updated_at=? WHERE id=?`,
		r.Name, r.Variables, r.Sort, r.UpdatedAt, r.ID)
	return err
}

// DeleteHttpEnvironment 删除环境。
func (d *Database) DeleteHttpEnvironment(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM http_environments WHERE id=?`, id)
	return err
}

// ---- 目录（项目下的功能模块，支持多级嵌套，parent_id 自引用） ----

// HttpFolder 项目下的目录，可多级嵌套（parent_id 指向同表其他目录）。
// project_id 冗余存储，便于按项目整体删除/列举。
type HttpFolder struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	ParentID  string `json:"parentId"`
	Name      string `json:"name"`
	Sort      int    `json:"sort"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

const httpFolderColumns = `id, project_id, parent_id, name, sort, created_at, updated_at`

func scanHttpFolder(s interface {
	Scan(dest ...interface{}) error
}) (*HttpFolder, error) {
	var r HttpFolder
	if err := s.Scan(&r.ID, &r.ProjectID, &r.ParentID, &r.Name, &r.Sort, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListHttpFolders 返回某项目下全部目录（含子目录），按 sort、created_at 排序。
func (d *Database) ListHttpFolders(projectID string) ([]HttpFolder, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT `+httpFolderColumns+` FROM http_folders WHERE project_id=? ORDER BY sort ASC, created_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HttpFolder
	for rows.Next() {
		r, err := scanHttpFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetHttpFolder 按 ID 取单个，未找到返回 (nil, nil)。
func (d *Database) GetHttpFolder(id string) (*HttpFolder, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow(`SELECT `+httpFolderColumns+` FROM http_folders WHERE id = ?`, id)
	r, err := scanHttpFolder(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// CreateHttpFolder 新建目录（ID/时间戳缺失时自动补全）。
func (d *Database) CreateHttpFolder(r *HttpFolder) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	r.UpdatedAt = r.CreatedAt
	_, err := d.conn.Exec(`INSERT INTO http_folders (id, project_id, parent_id, name, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.ProjectID, r.ParentID, r.Name, r.Sort, r.CreatedAt, r.UpdatedAt)
	return err
}

// UpdateHttpFolder 按 ID 更新目录（updated_at 自动刷新）。
func (d *Database) UpdateHttpFolder(r *HttpFolder) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r.UpdatedAt = now()
	_, err := d.conn.Exec(`UPDATE http_folders SET parent_id=?, name=?, sort=?, updated_at=? WHERE id=?`,
		r.ParentID, r.Name, r.Sort, r.UpdatedAt, r.ID)
	return err
}

// IsFolderAncestorOf 判断 folderID 是否为 candidateParentID 的祖先（沿 parent_id 链上溯）。
// 用于防环：把目录移动到自己的子孙下会形成环。最多上溯 64 层，防止异常数据导致死循环。
func (d *Database) IsFolderAncestorOf(folderID, candidateParentID string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := candidateParentID
	for i := 0; i < 64 && cur != "" && cur != folderID; i++ {
		var pid string
		err := d.conn.QueryRow(`SELECT parent_id FROM http_folders WHERE id = ?`, cur).Scan(&pid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if pid == folderID {
			return true, nil
		}
		cur = pid
	}
	return cur == folderID, nil
}

// folderSubtreeIDs 收集以 roots 为根的整棵目录子树 ID（含 roots 自身）。
// 调用方须已持锁（d.mu）。
func (d *Database) folderSubtreeIDs(roots []string) ([]string, error) {
	out := append([]string{}, roots...)
	queue := append([]string{}, roots...)
	// visited 去重 + 环检测：即使历史数据已成环，删除/移动也不会无限循环卡死 d.mu
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rows, err := d.conn.Query(`SELECT id FROM http_folders WHERE parent_id = ?`, cur)
		if err != nil {
			return nil, err
		}
		var children []string
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[cid] {
				seen[cid] = true
				children = append(children, cid)
			}
		}
		rows.Close()
		out = append(out, children...)
		queue = append(queue, children...)
	}
	return out, nil
}

// deleteFoldersAndRequests 删除给定目录及其下请求（调用方须已持锁）。
func (d *Database) deleteFoldersAndRequests(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := strings.Join(placeholders, ",")
	if _, err := d.conn.Exec(`DELETE FROM api_requests WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM http_docs WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM http_folders WHERE id IN (`+q+`)`, args...); err != nil {
		return err
	}
	return nil
}

// DeleteHttpFolder 删除目录：级联删除其下所有子目录与请求（用户选择「连带删除」）。
func (d *Database) DeleteHttpFolder(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids, err := d.folderSubtreeIDs([]string{id})
	if err != nil {
		return err
	}
	return d.deleteFoldersAndRequests(ids)
}

// ReorderHttpFolders 将某父目录（projectID + parentID）下的子目录按 ids 顺序重排，
// 并一并更新 project_id / parent_id（支持拖拽排序与跨目录、跨项目移动）。
func (d *Database) ReorderHttpFolders(projectID, parentID string, ids []string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.Exec(`UPDATE http_folders SET project_id=?, parent_id=?, sort=? WHERE id=?`,
				projectID, parentID, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateFolderSubtreeProject 将整棵目录子树（含自身）的 project_id 及子请求/子文档的
// project_id 统一改为新项目，避免跨项目移动目录后产生孤儿数据。调用方须已持锁。
func (d *Database) UpdateFolderSubtreeProject(rootID, projectID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids, err := d.folderSubtreeIDs([]string{rootID})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	// 注意顺序：第一个占位符对应 SET project_id=?，必须先放 projectID，再放 IN 列表
	args = append(args, projectID)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	q := strings.Join(ph, ",")
	if _, err := d.conn.Exec(`UPDATE http_folders SET project_id=? WHERE id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`UPDATE api_requests SET project_id=? WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`UPDATE http_docs SET project_id=? WHERE folder_id IN (`+q+`)`, args...); err != nil {
		return err
	}
	return nil
}
