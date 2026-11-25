package db

import "strings"

// 网页条目的存活状态持久层（item_health 表）。
//
// 表结构见 schema.go：一条条目最多一行，每次检查整行覆盖。内容摘要与「是否变化」
// 分开存，是因为变化判定要跟上一轮比，判定结果本身也要能在界面上单独筛。

// ItemHealth 一次检查的结果 + 关联条目的展示字段。
type ItemHealth struct {
	ItemID    string `json:"itemId"`
	Status    string `json:"status"`
	Code      int    `json:"code"`
	FinalURL  string `json:"finalUrl"`
	ErrMsg    string `json:"errMsg"`
	Changed   bool   `json:"changed"`
	CheckedAt string `json:"checkedAt"`

	// ContentHash 不出现在 JSON 里：前端只需要 changed 这个结论，
	// 摘要本身上百字节，回给前端没有任何用途。
	ContentHash string `json:"-"`

	// 以下来自 items / collections 的联表，方便书签页直接渲染
	Name           string `json:"name"`
	URL            string `json:"url"`
	CollectionName string `json:"collectionName"`
}

// WebItem 待检查的网页条目（只取探测需要的最小列）。
type WebItem struct {
	ID     string
	Name   string
	URL    string
	Remark string
}

// ListWebItems 取全部网页条目，供全量检查使用。
func (d *Database) ListWebItems(limit int) ([]WebItem, error) {
	if limit <= 0 || limit > 20000 {
		limit = 20000
	}
	rows, err := d.Query(
		"SELECT id, name, value, remark FROM items WHERE type = ? ORDER BY usage_count DESC LIMIT ?",
		"网页", limit)
	if err != nil {
		return nil, err
	}
	out := make([]WebItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, WebItem{
			ID:     str(r["id"]),
			Name:   str(r["name"]),
			URL:    str(r["value"]),
			Remark: str(r["remark"]),
		})
	}
	return out, nil
}

// GetWebItem 取单条网页条目（指定 id 检查时用）。
func (d *Database) GetWebItem(id string) (*WebItem, error) {
	r, err := d.QueryOne("SELECT id, name, value, remark FROM items WHERE id = ? AND type = ?", id, "网页")
	if err != nil || r == nil {
		return nil, err
	}
	return &WebItem{ID: str(r["id"]), Name: str(r["name"]), URL: str(r["value"]), Remark: str(r["remark"])}, nil
}

// PrevContentHash 取上一轮的内容摘要，用于判断「内容是否变了」。
func (d *Database) PrevContentHash(itemID string) string {
	r, err := d.QueryOne("SELECT content_hash FROM item_health WHERE item_id = ?", itemID)
	if err != nil || r == nil {
		return ""
	}
	return str(r["content_hash"])
}

// UpsertItemHealth 写入检查结果（覆盖旧行）。
func (d *Database) UpsertItemHealth(h ItemHealth) error {
	changed := 0
	if h.Changed {
		changed = 1
	}
	return d.ExecuteParams(`INSERT INTO item_health
		(item_id, status, http_code, final_url, err_msg, content_hash, changed, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			status = excluded.status,
			http_code = excluded.http_code,
			final_url = excluded.final_url,
			err_msg = excluded.err_msg,
			content_hash = CASE WHEN excluded.content_hash = '' THEN item_health.content_hash
			                    ELSE excluded.content_hash END,
			changed = excluded.changed,
			checked_at = excluded.checked_at`,
		[]interface{}{h.ItemID, h.Status, h.Code, h.FinalURL, h.ErrMsg, h.ContentHash, changed, h.CheckedAt})
}

// ListItemHealth 列出检查结果，status 为空表示全部。
//
// 只回「有检查记录」的条目：没查过的条目不该以「未知」状态混进死链筛选结果里。
func (d *Database) ListItemHealth(status string) ([]ItemHealth, error) {
	q := `SELECT h.item_id, h.status, h.http_code, h.final_url, h.err_msg, h.changed, h.checked_at,
	             i.name, i.value, COALESCE(c.name, '') AS collection_name
	      FROM item_health h
	      JOIN items i ON i.id = h.item_id
	      LEFT JOIN collections c ON c.id = i.collection_id`
	args := []interface{}{}
	if s := strings.TrimSpace(status); s != "" {
		q += " WHERE h.status = ?"
		args = append(args, s)
	}
	q += " ORDER BY CASE h.status WHEN 'broken' THEN 0 WHEN 'error' THEN 1 WHEN 'timeout' THEN 2 WHEN 'moved' THEN 3 WHEN 'blocked' THEN 4 ELSE 5 END, i.name"

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]ItemHealth, 0, len(rows))
	for _, r := range rows {
		out = append(out, ItemHealth{
			ItemID:         str(r["item_id"]),
			Status:         str(r["status"]),
			Code:           integer(r["http_code"]),
			FinalURL:       str(r["final_url"]),
			ErrMsg:         str(r["err_msg"]),
			Changed:        integer(r["changed"]) == 1,
			CheckedAt:      str(r["checked_at"]),
			Name:           str(r["name"]),
			URL:            str(r["value"]),
			CollectionName: str(r["collection_name"]),
		})
	}
	return out, nil
}

// ClearItemHealth 清空检查结果（用户想重新扫一遍时用）。
func (d *Database) ClearItemHealth() error {
	return d.ExecuteParams("DELETE FROM item_health", nil)
}
