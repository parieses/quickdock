package db

import (
	"fmt"
	"strings"
	"time"
)

// ---- 笔记树（snippets 表升级为树形：文件夹 + 文档）----
// 兼容策略：
//   - 文件夹：is_folder=1，name 为标题，keyword 用唯一自动值 "f:<uuid>" 占位
//   - 文档/片段：is_folder=0，name 为标题，content 为 Markdown/文本
//   - 旧数据（快捷笔记、剪贴板存片段）continue 作为根级文档（parent_id=''，name 已回填）

// ListNotesTree 返回全量笔记树节点（含文件夹与文档），由前端拼树。
func (d *Database) ListNotesTree() ([]Snippet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + snippetCols + ` FROM snippets ORDER BY is_folder DESC, sort ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		s, err := scanSnippetNode(rows)
		if err != nil {
			return nil, err
		}
		if s.Name == "" {
			s.Name = s.Keyword
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// CreateNoteFolder 新建文件夹节点（is_folder=1）。
func (d *Database) CreateNoteFolder(parentID, name string) (*Snippet, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("文件夹名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := &Snippet{
		ID:        newID(),
		Keyword:   "f:" + newID(), // 占位，保证 UNIQUE
		Content:   "",
		Name:      name,
		ParentID:  parentID,
		IsFolder:  true,
		Sort:      nextSortLocked(d, "snippets", parentID),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	_, err := d.conn.Exec(
		`INSERT INTO snippets (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Keyword, s.Content, "folder", s.Name, s.ParentID, b2i(s.IsFolder), s.Sort, "[]", 0, "markdown", s.CreatedAt)
	return s, err
}

// CreateNoteDoc 新建文档节点（is_folder=0），parentID 可为空（根）。
// format: markdown | text（决定前端是否显示预览分栏）
func (d *Database) CreateNoteDoc(parentID, name, content, format string) (*Snippet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名笔记"
	}
	if format == "" {
		format = "markdown"
	}
	s := &Snippet{
		ID:        newID(),
		Keyword:   newID(),
		Content:   content,
		Name:      name,
		ParentID:  parentID,
		IsFolder:  false,
		Sort:      nextSortLocked(d, "snippets", parentID),
		Tags:      "[]",
		IsNote:    true,
		Format:    format,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	_, err := d.conn.Exec(
		`INSERT INTO snippets (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Keyword, s.Content, "note", s.Name, s.ParentID, b2i(s.IsFolder), s.Sort, s.Tags, b2i(s.IsNote), s.Format, s.CreatedAt)
	return s, err
}

// nextSortLocked 计算某父目录下最大的 sort+1（调用方需持 d.mu）
func nextSortLocked(d *Database, table, parentID string) int {
	var mx int
	err := d.conn.QueryRow("SELECT COALESCE(MAX(sort),0) FROM snippets WHERE parent_id = ?", parentID).Scan(&mx)
	if err != nil {
		return 0
	}
	return mx + 1
}

// RenameSnippetNode 重命名节点（文件夹/文档共用）。
func (d *Database) RenameSnippetNode(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE snippets SET name = ? WHERE id = ?`, name, id)
	return err
}

// UpdateNoteDoc 更新文档内容与标签。
func (d *Database) UpdateNoteDoc(id, content, tags string) error {
	if tags == "" {
		tags = "[]"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE snippets SET content = ?, tags = ? WHERE id = ? AND is_folder = 0`, content, tags, id)
	return err
}

// SetNoteFormat 设置笔记渲染格式（markdown | text）。
func (d *Database) SetNoteFormat(id, format string) error {
	if format != "markdown" && format != "text" {
		format = "markdown"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE snippets SET format = ? WHERE id = ? AND is_folder = 0`, format, id)
	return err
}

// MoveSnippetNode 移动节点到新的父目录（并更新同级 sort）。
func (d *Database) MoveSnippetNode(id, newParentID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 防环：不能把节点移进自己的子树
	if isSelfInSubtreeLocked(d, id, newParentID) {
		return fmt.Errorf("不能把节点移动到它自身的子目录下")
	}
	next := nextSortLocked(d, "snippets", newParentID)
	_, err := d.conn.Exec(`UPDATE snippets SET parent_id = ?, sort = ? WHERE id = ?`, newParentID, next, id)
	return err
}

// isSelfInSubtreeLocked 判断 id 是否在 ancestorID 的子树中（调用方持 d.mu）。
// 即：从 ancestorID 沿 parent 链向上，若碰到 id 则说明 ancestor 在 id 树下。
func isSelfInSubtreeLocked(d *Database, id, ancestorID string) bool {
	if id == "" || ancestorID == "" {
		return false
	}
	cur := ancestorID
	seen := map[string]bool{}
	for {
		if cur == id {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		var parent string
		err := d.conn.QueryRow(`SELECT parent_id FROM snippets WHERE id = ?`, cur).Scan(&parent)
		if err != nil || parent == "" {
			return false
		}
		cur = parent
	}
}

// DeleteSnippetNode 递归删除节点及其子树（文件夹含其下所有）。
func (d *Database) DeleteSnippetNode(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := collectSubtreeLocked(d, id)
	if len(ids) == 0 {
		return nil
	}
	qs := strings.Repeat("?,", len(ids))
	qs = qs[:len(qs)-1]
	args := make([]interface{}, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	_, err := d.conn.Exec(`DELETE FROM snippets WHERE id IN (`+qs+`)`, args...)
	return err
}

// collectSubtreeLocked 返回 id 及其所有子孙 id（调用方持 d.mu）。
func collectSubtreeLocked(d *Database, id string) []string {
	all, _ := d.conn.Query(`SELECT id, parent_id FROM snippets`)
	if all == nil {
		return []string{id}
	}
	defer all.Close()
	children := map[string][]string{}
	hasID := false
	for all.Next() {
		var nid, pid string
		if err := all.Scan(&nid, &pid); err != nil {
			continue
		}
		if nid == id {
			hasID = true
		}
		children[pid] = append(children[pid], nid)
	}
	if !hasID {
		return nil
	}
	out := []string{id}
	stack := []string{id}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[cur] {
			out = append(out, c)
			stack = append(stack, c)
		}
	}
	return out
}

// SearchNotes 按名称/内容/标签搜索笔记（文档与文件夹名都搜；内容只搜文档）。
func (d *Database) SearchNotes(q string) ([]Snippet, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	escaped := strings.ReplaceAll(q, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	like := "%" + escaped + "%"
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT `+snippetCols+` FROM snippets
		 WHERE name LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\'
		 ORDER BY is_folder DESC, sort ASC, name ASC`,
		like, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		s, err := scanSnippetNode(rows)
		if err != nil {
			return nil, err
		}
		if s.Name == "" {
			s.Name = s.Keyword
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}
