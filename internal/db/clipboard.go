package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
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
	Note        string `json:"note"`
	CreatedAt   int64  `json:"createdAt"`
	// TextType 文本条目的智能分类（url/email/json/code/phone），入库时由正则判定并存储。
	// 空串表示普通文本。筛选阶段直接下推到 SQL（WHERE content_type='text' AND text_type=?），
	// 既快又不受「只载入前 N 条」限制——等价于 image/file 的 content_type 下推。
	// 判定口径必须与前端 utils/clipboardFilter.ts 的 classifyTextType 保持一致（靠单测锁两边）。
	TextType string `json:"textType"`
}

// 文本智能分类的正则（与前端 clipboardFilter.ts 的 classifyTextType 一一对应）。
// 顺序即优先级：email > url > phone > json > code，第一个命中即返回。
var (
	clipReEmail = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	clipReURL   = regexp.MustCompile(`^https?://[^\s]+$`)
	clipRePhone = regexp.MustCompile(`^1[3-9]\d{9}$`)
	clipReCode  = regexp.MustCompile(`^(function|const|let|var|import|export|class|def|#include|public|private)\b`)
)

// classifyTextType 判定一段文本的智能分类（url/email/json/code/phone）。
// 仅对文本条目有意义；图片/文件路径不属于任何分类，调用方不应传它们进来。
// 必须与前端 classifyTextType 保持同构（顺序、规则一致），否则「入库分类」与「前端展示」
// 会对同一条目给出不同结论。
func classifyTextType(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if clipReEmail.MatchString(trimmed) {
		return "email"
	}
	if clipReURL.MatchString(trimmed) {
		return "url"
	}
	if clipRePhone.MatchString(trimmed) {
		return "phone"
	}
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) &&
		(strings.HasSuffix(trimmed, "}") || strings.HasSuffix(trimmed, "]")) {
		return "json"
	}
	if strings.Contains(trimmed, "\n") || clipReCode.MatchString(trimmed) {
		return "code"
	}
	return ""
}

// findAndBumpLocked 在 content_type=ct AND col=val 条件下查找已有剪贴板条目，
// 若存在则递增 copy_count 并更新 created_at。
// 调用方必须已持有 d.mu。
// 返回 (existingID, newCount, createdAt, found, error)
func (d *Database) findAndBumpLocked(ct, col, val string) (string, int, int64, bool, error) {
	var existingID string
	var existingCount int
	err := d.conn.QueryRow(
		fmt.Sprintf("SELECT id, copy_count FROM clipboard_entries WHERE content_type = ? AND %s = ?", col),
		ct, val,
	).Scan(&existingID, &existingCount)
	if err == sql.ErrNoRows {
		return "", 0, 0, false, nil
	}
	if err != nil {
		return "", 0, 0, false, err
	}

	now := time.Now().UnixMilli()
	_, err = d.conn.Exec(
		"UPDATE clipboard_entries SET copy_count = ?, created_at = ? WHERE id = ?",
		existingCount+1, now, existingID,
	)
	return existingID, existingCount + 1, now, true, err
}

// InsertClipboardEntry 插入或更新剪贴板记录（同文本合并：copy_count+1）
func (d *Database) InsertClipboardEntry(text string, sourceApp string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	id, count, now, found, err := d.findAndBumpLocked("text", "text_content", text)
	if err != nil {
		return nil, err
	}
	if found {
		return &ClipboardEntry{ID: id, ContentType: "text", TextContent: text, SourceApp: sourceApp, CopyCount: count, CreatedAt: now}, nil
	}

	entry := &ClipboardEntry{
		ID: uuid.New().String(), ContentType: "text", TextContent: text, SourceApp: sourceApp,
		CopyCount: 1, CreatedAt: time.Now().UnixMilli(),
	}
	// 入库即分类：把智能类型写进 text_type，筛选阶段直接下推 SQL，避免前端逐条正则。
	entry.TextType = classifyTextType(text)
	_, err = d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, source_app, copy_count, created_at, text_type) VALUES (?, ?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ContentType, entry.TextContent, entry.SourceApp, entry.CopyCount, entry.CreatedAt, entry.TextType,
	)
	return entry, err
}

// InsertClipboardImageEntry 插入或更新图片剪贴板条目（按 image_hash 去重）
func (d *Database) InsertClipboardImageEntry(id, imagePath, imageHash, textContent, sourceApp string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	eid, count, now, found, err := d.findAndBumpLocked("image", "image_hash", imageHash)
	if err != nil {
		return nil, err
	}
	if found {
		return &ClipboardEntry{ID: eid, ContentType: "image", TextContent: textContent, ImagePath: imagePath, ImageHash: imageHash, SourceApp: sourceApp, CopyCount: count, CreatedAt: now}, nil
	}

	entry := &ClipboardEntry{
		ID: id, ContentType: "image", TextContent: textContent, ImagePath: imagePath, ImageHash: imageHash, SourceApp: sourceApp,
		CopyCount: 1, CreatedAt: time.Now().UnixMilli(),
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

	id, count, now, found, err := d.findAndBumpLocked("file", "text_content", filePaths)
	if err != nil {
		return nil, err
	}
	if found {
		return &ClipboardEntry{ID: id, ContentType: "file", TextContent: filePaths, SourceApp: sourceApp, CopyCount: count, CreatedAt: now}, nil
	}

	entry := &ClipboardEntry{
		ID: uuid.New().String(), ContentType: "file", TextContent: filePaths, SourceApp: sourceApp,
		CopyCount: 1, CreatedAt: time.Now().UnixMilli(),
	}
	_, err = d.conn.Exec(
		"INSERT INTO clipboard_entries (id, content_type, text_content, source_app, copy_count, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		entry.ID, entry.ContentType, entry.TextContent, entry.SourceApp, entry.CopyCount, entry.CreatedAt,
	)
	return entry, err
}

// MAX_CLIPBOARD_ROWS 是「不指定上限」时的硬兜底：避免 limit<=0 把整张表读进内存
// （历史教训：剪贴板历史可达数万条，一次性 SELECT 全表会拖垮主进程）。
// 导出等确需全量的场景也被这一上限保护。
const MAX_CLIPBOARD_ROWS = 5000

// clipboardSelectCols 所有列表/读取查询共用的列集合（顺序固定）。
// 末尾的 text_type 必须与 scanClipboardEntries 的 Scan 顺序一一对应。
const clipboardSelectCols = "id, content_type, text_content, image_path, image_hash, source_app, is_pinned, copy_count, note, created_at, text_type"

// scanClipboardEntries 把多行结果扫描为剪贴板条目切片（含 text_type）。
// 三个列表查询与单条读取共用，避免列集合/Scan 顺序在三处各自维护、改一处漏两处。
func scanClipboardEntries(rows *sql.Rows) ([]ClipboardEntry, error) {
	var entries []ClipboardEntry
	for rows.Next() {
		var e ClipboardEntry
		var imgPath, txtContent, imgHash, note sql.NullString
		if err := rows.Scan(&e.ID, &e.ContentType, &txtContent, &imgPath, &imgHash, &e.SourceApp, &e.IsPinned, &e.CopyCount, &note, &e.CreatedAt, &e.TextType); err != nil {
			return nil, err
		}
		e.TextContent = txtContent.String
		e.ImagePath = imgPath.String
		e.ImageHash = imgHash.String
		e.Note = note.String
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListClipboardEntries 列出剪贴板历史（置顶优先，其余按时间倒序）
//
// 为什么置顶项要排在最前：全部列表查询都带 LIMIT，而置顶项的语义是「永远不该丢」。
// 之前只按 created_at 排，一条三个月前置顶的内容根本进不了「最近 1000 条」窗口，
// 于是「收藏」标签里也看不到它 —— 置顶等于没生效。
func (d *Database) ListClipboardEntries(limit int) ([]ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if limit <= 0 {
		// 无上限分支改为硬上限兜底，杜绝全表加载雪崩
		limit = MAX_CLIPBOARD_ROWS
	} else if limit > MAX_CLIPBOARD_ROWS {
		limit = MAX_CLIPBOARD_ROWS
	}
	rows, err := d.conn.Query("SELECT "+clipboardSelectCols+" FROM clipboard_entries ORDER BY is_pinned DESC, created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClipboardEntries(rows)
}

// ListAllClipboardEntries 读取全部剪贴板条目（无 LIMIT），仅供导出使用。
//
// 为什么单独开一个方法而不是把 MAX_CLIPBOARD_ROWS 调大：那个常量是「误用 limit=0
// 时不要整表读内存」的护栏，导出是唯一确实需要全量的场景，把它做成显式命名的方法，
// 就不会有人顺手在列表页调它。
// CountClipboardEntries 返回库内总条数。
// 列表接口带 limit（前端只拉最近 1000 条），标题上直接写 entries.length 会让
// 用户以为历史只有这么多；配上真总数才说得清「已载入 / 共多少」。
func (d *Database) CountClipboardEntries() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM clipboard_entries`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *Database) ListAllClipboardEntries() ([]ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		"SELECT " + clipboardSelectCols + " FROM clipboard_entries ORDER BY is_pinned DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClipboardEntries(rows)
}

// ListClipboardEntriesByType 按 content_type 过滤列出剪贴板历史（按时间倒序）。
// 直接在 SQL 层过滤，避免前端把整段历史拉回再 JS 筛选（历史量大时内存/卡顿）。
// contentType 为空串时等价于 ListClipboardEntries(limit)。
func (d *Database) ListClipboardEntriesByType(limit int, contentType string) ([]ClipboardEntry, error) {
	if limit <= 0 || limit > MAX_CLIPBOARD_ROWS {
		limit = MAX_CLIPBOARD_ROWS
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var rows *sql.Rows
	var err error
	if contentType == "" {
		rows, err = d.conn.Query("SELECT "+clipboardSelectCols+" FROM clipboard_entries ORDER BY is_pinned DESC, created_at DESC LIMIT ?", limit)
	} else {
		rows, err = d.conn.Query("SELECT "+clipboardSelectCols+" FROM clipboard_entries WHERE content_type = ? ORDER BY is_pinned DESC, created_at DESC LIMIT ?", contentType, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClipboardEntries(rows)
}

// ListClipboardEntriesBySmartType 按文本智能分类（url/email/json/code/phone）过滤列出
// 剪贴板历史（按时间倒序）。逻辑同 ListClipboardEntriesByType，只是把过滤下推到 text_type
// 列（content_type 固定为 text）——这样智能分类标签也和 image/file 一样走 SQL 下推，
// 既是 O(索引) 查询，又不受「只载入前 N 条」限制，旧的历史条目也能被筛出来。
func (d *Database) ListClipboardEntriesBySmartType(limit int, smartType string) ([]ClipboardEntry, error) {
	if limit <= 0 || limit > MAX_CLIPBOARD_ROWS {
		limit = MAX_CLIPBOARD_ROWS
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.conn.Query(
		"SELECT "+clipboardSelectCols+" FROM clipboard_entries WHERE content_type = 'text' AND text_type = ? ORDER BY is_pinned DESC, created_at DESC LIMIT ?",
		smartType, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanClipboardEntries(rows)
}

// GetClipboardEntry 根据 ID 获取单条剪贴板记录
func (d *Database) GetClipboardEntry(id string) (*ClipboardEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var e ClipboardEntry
	var imgPath, txtContent, imgHash, note sql.NullString
	err := d.conn.QueryRow(
		"SELECT "+clipboardSelectCols+" FROM clipboard_entries WHERE id = ?", id,
	).Scan(&e.ID, &e.ContentType, &txtContent, &imgPath, &imgHash, &e.SourceApp, &e.IsPinned, &e.CopyCount, &note, &e.CreatedAt, &e.TextType)
	if err != nil {
		return nil, err
	}
	e.TextContent = txtContent.String
	e.ImagePath = imgPath.String
	e.ImageHash = imgHash.String
	e.Note = note.String
	return &e, nil
}

// backfillClipboardTextType 为存量文本条目补齐 text_type（智能分类 url/email/json/code/phone）。
//
// 调用方（migrate）已持有 d.mu 写锁，故本函数直接走 d.conn，不要再调用会自行加锁的
// 列表/更新方法，否则会重入死锁。仅在 text_type 为空时计算并回写，幂等：首次运行后
// 存量全部带类型，后续启动走到这里时 SELECT 命中 0 行，直接返回，无额外开销。
func (d *Database) backfillClipboardTextType() error {
	rows, err := d.conn.Query("SELECT id, text_content FROM clipboard_entries WHERE content_type = 'text' AND (text_type IS NULL OR text_type = '')")
	if err != nil {
		return err
	}
	type pair struct{ id, t string }
	batch := make([]pair, 0, 256)
	for rows.Next() {
		var id, tc string
		if err := rows.Scan(&id, &tc); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, pair{id, classifyTextType(tc)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(batch) == 0 {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("UPDATE clipboard_entries SET text_type = ? WHERE id = ?")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, p := range batch {
		if _, err := stmt.Exec(p.t, p.id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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

// DeleteClipboardEntry 删除单条剪贴板条目，返回它引用的图片路径（非图片条目为空串）。
//
// 为什么不在这里顺手删文件：DB 层只负责数据。图片落在 platform.GetImageDir() 下，
// 由服务层统一按目录白名单清理（见 services.RemoveClipboardImages），
// 这样「只删自己目录里的文件」这条安全规则只有一处实现。
func (d *Database) DeleteClipboardEntry(id string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var img sql.NullString
	// 查不到行不是错误：下面 DELETE 同样影响 0 行，调用方拿到的语义一致。
	_ = d.conn.QueryRow("SELECT image_path FROM clipboard_entries WHERE id = ?", id).Scan(&img)
	_, err := d.conn.Exec("DELETE FROM clipboard_entries WHERE id = ?", id)
	return img.String, err
}

// collectClipboardImagePaths 取出即将被删的条目的图片路径（调用方已持锁）。
func (d *Database) collectClipboardImagePaths(where string, args ...interface{}) []string {
	rows, err := d.conn.Query(
		"SELECT image_path FROM clipboard_entries WHERE "+where+" AND image_path IS NOT NULL AND image_path <> ''",
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ClearClipboardHistory 清空剪贴板历史（保留固定的条目），返回删除条数与待清理的图片路径
func (d *Database) ClearClipboardHistory() (int64, []string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	imgs := d.collectClipboardImagePaths("is_pinned = 0")
	res, err := d.conn.Exec("DELETE FROM clipboard_entries WHERE is_pinned = 0")
	if err != nil {
		return 0, nil, err
	}
	n, _ := res.RowsAffected()
	return n, imgs, nil
}

// IncrementClipboardCopyCount 增加指定条目的复制次数
func (d *Database) IncrementClipboardCopyCount(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("UPDATE clipboard_entries SET copy_count = copy_count + 1, created_at = ? WHERE id = ?", time.Now().UnixMilli(), id)
	return err
}

// UpdateClipboardNote 更新剪贴板条目的备注
func (d *Database) UpdateClipboardNote(id, note string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.conn.Exec("UPDATE clipboard_entries SET note = ? WHERE id = ?", note, id)
	return err
}

// DeleteExpiredClipboardEntries 删除超过指定天数的剪贴板条目，返回删除条数与待清理的图片路径。
//
// 返回图片路径是必须的：只删数据库行会让 GetImageDir() 下的 PNG 永远留着，
// 一个月几十张截图不显眼，一年就是几百 MB 的纯垃圾（且用户完全看不到）。
func (d *Database) DeleteExpiredClipboardEntries(days int) (int64, []string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	imgs := d.collectClipboardImagePaths("created_at < ? AND is_pinned = 0", cutoff)
	res, err := d.conn.Exec(
		"DELETE FROM clipboard_entries WHERE created_at < ? AND is_pinned = 0", cutoff,
	)
	if err != nil {
		return 0, nil, err
	}
	count, _ := res.RowsAffected()
	return count, imgs, nil
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
