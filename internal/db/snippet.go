package db

import (
	"fmt"
	"strings"
	"time"
)

// Snippet 文本片段
type Snippet struct {
	ID        string `json:"id"`
	Keyword   string `json:"keyword"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	CreatedAt string `json:"createdAt"`
}

// CreateSnippet 创建文本片段
func (d *Database) CreateSnippet(keyword, content, category string) (*Snippet, error) {
	if keyword == "" || content == "" {
		return nil, fmt.Errorf("关键词和内容不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	s := &Snippet{
		ID:        newID(),
		Keyword:   keyword,
		Content:   content,
		Category:  category,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	_, err := d.conn.Exec(
		"INSERT INTO snippets (id, keyword, content, category, created_at) VALUES (?, ?, ?, ?, ?)",
		s.ID, s.Keyword, s.Content, s.Category, s.CreatedAt,
	)
	return s, err
}

// ListSnippets 列出所有文本片段（按关键词升序）
func (d *Database) ListSnippets() ([]Snippet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT id, keyword, content, category, created_at FROM snippets ORDER BY keyword ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snippets []Snippet
	for rows.Next() {
		var s Snippet
		if err := rows.Scan(&s.ID, &s.Keyword, &s.Content, &s.Category, &s.CreatedAt); err != nil {
			return nil, err
		}
		snippets = append(snippets, s)
	}
	return snippets, rows.Err()
}

// GetSnippetByKeyword 按唯一关键词查询片段（用于快捷笔记 find-or-create）
func (d *Database) GetSnippetByKeyword(keyword string) (*Snippet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow("SELECT id, keyword, content, category, created_at FROM snippets WHERE keyword = ?", keyword)
	var s Snippet
	if err := row.Scan(&s.ID, &s.Keyword, &s.Content, &s.Category, &s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetOrCreateNoteSnippet 获取或创建快捷笔记片段（关键词固定，允许空内容）
func (d *Database) GetOrCreateNoteSnippet(keyword string) (*Snippet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow("SELECT id, keyword, content, category, created_at FROM snippets WHERE keyword = ?", keyword)
	var s Snippet
	if err := row.Scan(&s.ID, &s.Keyword, &s.Content, &s.Category, &s.CreatedAt); err == nil {
		return &s, nil
	}
	s = Snippet{ID: newID(), Keyword: keyword, Content: "", Category: "note", CreatedAt: time.Now().Format(time.RFC3339)}
	if _, err := d.conn.Exec("INSERT INTO snippets (id, keyword, content, category, created_at) VALUES (?, ?, ?, ?, ?)", s.ID, s.Keyword, s.Content, s.Category, s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateNoteSnippet 更新快捷笔记内容（允许空内容）
func (d *Database) UpdateNoteSnippet(id, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE snippets SET content = ? WHERE id = ?", content, id)
	return err
}

// SearchSnippets 按关键词搜索文本片段
func (d *Database) SearchSnippets(query string) ([]Snippet, error) {
	// LIKE 中 % 和 _ 是通配符，需要转义
	escaped := strings.ReplaceAll(query, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	like := "%" + escaped + "%"
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT id, keyword, content, category, created_at FROM snippets WHERE keyword LIKE ? ESCAPE '\\' OR content LIKE ? ESCAPE '\\' ORDER BY keyword ASC", like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snippets []Snippet
	for rows.Next() {
		var s Snippet
		if err := rows.Scan(&s.ID, &s.Keyword, &s.Content, &s.Category, &s.CreatedAt); err != nil {
			return nil, err
		}
		snippets = append(snippets, s)
	}
	return snippets, rows.Err()
}

// DeleteSnippet 删除文本片段
func (d *Database) DeleteSnippet(id string) error {
	return d.DeleteWhere("snippets", "id = ?", id)
}

// UpdateSnippet 更新文本片段
func (d *Database) UpdateSnippet(id, keyword, content, category string) error {
	if keyword == "" || content == "" {
		return fmt.Errorf("关键词和内容不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(
		"UPDATE snippets SET keyword = ?, content = ?, category = ? WHERE id = ?",
		keyword, content, category, id,
	)
	return err
}
