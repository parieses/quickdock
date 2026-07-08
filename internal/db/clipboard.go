package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ClipboardEntry 剪贴板历史条目
type ClipboardEntry struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	TextContent string `json:"textContent"`
	ImagePath   string `json:"imagePath"`
	ImageHash   string `json:"imageHash"`
	SourceApp   string `json:"sourceApp"`
	IsPinned    int    `json:"isPinned"`
	CopyCount   int    `json:"copyCount"`
	CreatedAt   int64  `json:"createdAt"`
}

// InsertClipboardEntry 插入或更新剪贴板记录（同文本合并：copy_count+1）
func (d *Database) InsertClipboardEntry(text string, sourceApp string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var existingID string
	var existingCount int
	err := d.conn.QueryRow("SELECT id, copy_count FROM clipboard_entries WHERE content_type = 'text' AND text_content = ?", text).Scan(&existingID, &existingCount)
	if err == nil {
		now := time.Now().UnixMilli()
		_, err = d.conn.Exec(
			"UPDATE clipboard_entries SET copy_count = ?, created_at = ? WHERE id = ?",
			existingCount+1, now, existingID,
		)
		if err != nil {
			return nil, err
		}
		return &ClipboardEntry{
			ID:          existingID,
			ContentType: "text",
			TextContent: text,
			SourceApp:   sourceApp,
			CopyCount:   existingCount + 1,
			CreatedAt:   now,
		}, nil
	}

	entry := &ClipboardEntry{
		ID:          uuid.New().String(),
		ContentType: "text",
		TextContent: text,
		SourceApp:   sourceApp,
		CopyCount:   1,
		CreatedAt:   time.Now().UnixMilli(),
	}
	_, err = d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, source_app, copy_count, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ContentType, entry.TextContent, entry.SourceApp, entry.CopyCount, entry.CreatedAt,
	)
	return entry, err
}

// InsertClipboardImageEntry 插入或更新图片剪贴板条目（按 image_hash 去重）
func (d *Database) InsertClipboardImageEntry(id, imagePath, imageHash, textContent, sourceApp string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UnixMilli()

	var existingID string
	var existingCount int
	err := d.conn.QueryRow("SELECT id, copy_count FROM clipboard_entries WHERE content_type = 'image' AND image_hash = ?", imageHash).Scan(&existingID, &existingCount)
	if err == nil {
		_, err = d.conn.Exec("UPDATE clipboard_entries SET copy_count = ?, created_at = ? WHERE id = ?", existingCount+1, now, existingID)
		if err != nil {
			return nil, err
		}
		return &ClipboardEntry{
			ID:          existingID,
			ContentType: "image",
			TextContent: textContent,
			ImagePath:   imagePath,
			ImageHash:   imageHash,
			SourceApp:   sourceApp,
			CopyCount:   existingCount + 1,
			CreatedAt:   now,
		}, nil
	}

	entry := &ClipboardEntry{
		ID:          id,
		ContentType: "image",
		TextContent: textContent,
		ImagePath:   imagePath,
		ImageHash:   imageHash,
		SourceApp:   sourceApp,
		CopyCount:   1,
		CreatedAt:   now,
	}
	_, err = d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, image_path, image_hash, source_app, copy_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ContentType, entry.TextContent, entry.ImagePath, entry.ImageHash, entry.SourceApp, entry.CopyCount, entry.CreatedAt,
	)
	return entry, err
}

// InsertClipboardFileEntry 插入文件路径剪贴板条目
func (d *Database) InsertClipboardFileEntry(filePaths, sourceApp string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var existingID string
	var existingCount int
	err := d.conn.QueryRow("SELECT id, copy_count FROM clipboard_entries WHERE content_type = 'file' AND text_content = ?", filePaths).Scan(&existingID, &existingCount)
	if err == nil {
		now := time.Now().UnixMilli()
		_, err = d.conn.Exec(
			"UPDATE clipboard_entries SET copy_count = ?, created_at = ? WHERE id = ?",
			existingCount+1, now, existingID,
		)
		if err != nil {
			return nil, err
		}
		return &ClipboardEntry{
			ID:          existingID,
			ContentType: "file",
			TextContent: filePaths,
			SourceApp:   sourceApp,
			CopyCount:   existingCount + 1,
			CreatedAt:   now,
		}, nil
	}

	entry := &ClipboardEntry{
		ID:          uuid.New().String(),
		ContentType: "file",
		TextContent: filePaths,
		SourceApp:   sourceApp,
		CopyCount:   1,
		CreatedAt:   time.Now().UnixMilli(),
	}
	_, err = d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, source_app, copy_count, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ContentType, entry.TextContent, entry.SourceApp, entry.CopyCount, entry.CreatedAt,
	)
	return entry, err
}

// ListClipboardEntries 列出剪贴板历史（按时间倒序）
func (d *Database) ListClipboardEntries(limit int) ([]ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if limit <= 0 {
		limit = 200
	}
	rows, err := d.conn.Query("SELECT id, content_type, text_content, image_path, image_hash, source_app, is_pinned, copy_count, created_at FROM clipboard_entries ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ClipboardEntry
	for rows.Next() {
		var e ClipboardEntry
		var imgPath sql.NullString
		var txtContent sql.NullString
		var imgHash sql.NullString
		if err := rows.Scan(&e.ID, &e.ContentType, &txtContent, &imgPath, &imgHash, &e.SourceApp, &e.IsPinned, &e.CopyCount, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.TextContent = txtContent.String
		e.ImagePath = imgPath.String
		e.ImageHash = imgHash.String
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetClipboardEntry 根据 ID 获取单条剪贴板记录
func (d *Database) GetClipboardEntry(id string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var e ClipboardEntry
	var imgPath sql.NullString
	var txtContent sql.NullString
	var imgHash sql.NullString
	err := d.conn.QueryRow(
		"SELECT id, content_type, text_content, image_path, image_hash, source_app, is_pinned, copy_count, created_at FROM clipboard_entries WHERE id = ?", id,
	).Scan(&e.ID, &e.ContentType, &txtContent, &imgPath, &imgHash, &e.SourceApp, &e.IsPinned, &e.CopyCount, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	e.TextContent = txtContent.String
	e.ImagePath = imgPath.String
	e.ImageHash = imgHash.String
	return &e, nil
}

// TogglePinClipboardEntry 切换剪贴板条目的收藏状态
func (d *Database) TogglePinClipboardEntry(id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var currentPinned int
	err := d.conn.QueryRow("SELECT is_pinned FROM clipboard_entries WHERE id = ?", id).Scan(&currentPinned)
	if err != nil {
		return false, fmt.Errorf("条目不存在: %w", err)
	}

	newVal := 0
	if currentPinned == 0 {
		newVal = 1
	}
	_, err = d.conn.Exec("UPDATE clipboard_entries SET is_pinned = ? WHERE id = ?", newVal, id)
	if err != nil {
		return false, err
	}
	return newVal == 1, nil
}

// DeleteClipboardEntry 删除单条剪贴板条目
func (d *Database) DeleteClipboardEntry(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("DELETE FROM clipboard_entries WHERE id = ?", id)
	return err
}

// IncrementClipboardCopyCount 增加指定条目的复制次数
func (d *Database) IncrementClipboardCopyCount(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("UPDATE clipboard_entries SET copy_count = copy_count + 1 WHERE id = ?", id)
	return err
}

// DeleteExpiredClipboardEntries 删除超过指定天数的剪贴板条目
func (d *Database) DeleteExpiredClipboardEntries(days int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	res, err := d.conn.Exec(
		"DELETE FROM clipboard_entries WHERE created_at < ? AND is_pinned = 0", cutoff,
	)
	if err != nil {
		return 0, err
	}
	count, _ := res.RowsAffected()
	return count, nil
}

// GetClipboardRetentionDays 获取剪贴板保留天数（默认 30 天）
func (d *Database) GetClipboardRetentionDays() (int, error) {
	val, err := d.GetSetting("clipboard_retention_days")
	if err != nil || val == "" {
		return 30, nil
	}
	var days int
	if _, err := fmt.Sscanf(val, "%d", &days); err != nil || days <= 0 {
		return 30, nil
	}
	return days, nil
}

// SetClipboardRetentionDays 设置剪贴板保留天数
func (d *Database) SetClipboardRetentionDays(days int) error {
	if days <= 0 {
		days = 30
	}
	return d.SetSetting("clipboard_retention_days", fmt.Sprintf("%d", days))
}
