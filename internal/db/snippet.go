package db

import "time"

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
		return nil, nil
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

// ListSnippets 列出所有文本片段
func (d *Database) ListSnippets() ([]Snippet, error) {
	rows, err := d.ListTableWhere("snippets", "1=1 ORDER BY keyword ASC")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToSnippet), nil
}

// SearchSnippets 按关键词搜索文本片段
func (d *Database) SearchSnippets(query string) ([]Snippet, error) {
	like := "%" + query + "%"
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query("SELECT id, keyword, content, category, created_at FROM snippets WHERE keyword LIKE ? OR content LIKE ? ORDER BY keyword ASC", like, like)
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

func mapToSnippet(m map[string]interface{}) Snippet {
	return Snippet{
		ID:        str(m["id"]),
		Keyword:   str(m["keyword"]),
		Content:   str(m["content"]),
		Category:  str(m["category"]),
		CreatedAt: str(m["created_at"]),
	}
}
