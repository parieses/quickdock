package db

// 笔记修订历史：保存前把「旧内容」拍下来，供误覆盖 / 误粘贴后回滚。
//
// 为什么不在 services 层做：写侧有前端防抖保存、MCP 直写、导入、剪藏四五个
// 入口，任何一个漏调就少一份历史。挂在 db 的 UPDATE 事务里是唯一能保证
// 「所有覆盖式写入都留痕」的位置，且留痕失败只记日志、绝不阻断正文保存。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// 修订留痕原因。落库存稳定枚举（不做本地化）：节流判断与历史数据都按字面值比对，
// 展示层的中英文案由前端 revisionReasonLabel 映射，别把中文写进这里。
const (
	noteRevisionSave     = "save"
	noteRevisionImport   = "import"
	noteRevisionRollback = "rollback"
	noteRevisionClip     = "clip"
)

const (
	// 前端 800ms 防抖保存，一篇写十分钟的笔记会产生几百次 UPDATE。
	// 一分钟一个恢复点足够覆盖「粘贴覆盖 / 删掉一段」，又不会把库撑爆。
	noteRevisionMinGap = 60 * time.Second
	// 每篇最多保留的修订条数，超出删最旧的。
	noteRevisionKeep = 40
)

// NoteRevision 一条历史版本。
type NoteRevision struct {
	ID        string `json:"id"`
	NoteID    string `json:"noteId"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Tags      string `json:"tags"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"createdAt"`
}

// noteRevisionConn 只用到 Query/Exec，与 rowQuerier 同形；单独声明是为了
// 不把 QueryRow 塞进那个公共接口（回收站那条链路上还有别的实现方）。
type noteRevisionConn interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// captureNoteRevisionLocked 在一次写操作前记录旧版本（调用方持 d.mu 写锁）。
// 非文档节点（文件夹 / 快捷笔记）直接跳过，不留垃圾行。
func captureNoteRevisionLocked(q noteRevisionConn, noteID, reason string) error {
	if noteID == "" {
		return nil
	}
	rows, err := q.Query(`SELECT name, content, tags FROM notes
		WHERE id = ? AND is_folder = 0 AND keyword <> ? LIMIT 1`, noteID, NoteQuickKeyword)
	if err != nil {
		return err
	}
	var name, content, tags string
	found := false
	if rows.Next() {
		if err := rows.Scan(&name, &content, &tags); err != nil {
			rows.Close()
			return err
		}
		found = true
	}
	rows.Close()
	if !found {
		// 节点不存在或不是文档：留给后面的 UPDATE 去报「笔记不存在」，
		// 这里只是没有可拍的旧版本。
		return nil
	}

	// 节流只对自动保存生效。导入 / 回滚 / 剪藏是明确的离散动作，必须每次留痕。
	if reason == noteRevisionSave {
		last, err := latestNoteRevisionLocked(q, noteID)
		if err != nil {
			return err
		}
		if last != nil {
			if last.Content == content {
				return nil // 内容没变（例如只点了一下格式切换的连锁保存）
			}
			if t, e := time.Parse(time.RFC3339, last.CreatedAt); e == nil && time.Since(t) < noteRevisionMinGap {
				return nil
			}
		}
	}

	return insertNoteRevisionLocked(q, NoteRevision{
		ID:        newID(),
		NoteID:    noteID,
		Name:      name,
		Content:   content,
		Tags:      tags,
		Reason:    reason,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
}

func latestNoteRevisionLocked(q noteRevisionConn, noteID string) (*NoteRevision, error) {
	rows, err := q.Query(`SELECT id, note_id, name, content, tags, reason, created_at
		FROM note_revisions WHERE note_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var r NoteRevision
	if err := rows.Scan(&r.ID, &r.NoteID, &r.Name, &r.Content, &r.Tags, &r.Reason, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func insertNoteRevisionLocked(q noteRevisionConn, r NoteRevision) error {
	if _, err := q.Exec(`INSERT INTO note_revisions (id, note_id, name, content, tags, reason, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.NoteID, r.Name, r.Content, r.Tags, r.Reason, r.CreatedAt); err != nil {
		return err
	}
	_, err := q.Exec(`DELETE FROM note_revisions WHERE note_id = ? AND id NOT IN (
		SELECT id FROM note_revisions WHERE note_id = ?
		ORDER BY created_at DESC, rowid DESC LIMIT ?)`, r.NoteID, r.NoteID, noteRevisionKeep)
	return err
}

// ListNoteRevisions 按时间倒序列出某篇笔记的修订（不含正文以外的大字段裁剪，前端自行截断）。
func (d *Database) ListNoteRevisions(noteID string, limit int) ([]NoteRevision, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT id, note_id, name, content, tags, reason, created_at
		FROM note_revisions WHERE note_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`, noteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NoteRevision{}
	for rows.Next() {
		var r NoteRevision
		if err := rows.Scan(&r.ID, &r.NoteID, &r.Name, &r.Content, &r.Tags, &r.Reason, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetNoteRevision 读单条修订（前端做 diff / 预览用）。
func (d *Database) GetNoteRevision(id string) (*NoteRevision, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var r NoteRevision
	err := d.conn.QueryRow(`SELECT id, note_id, name, content, tags, reason, created_at
		FROM note_revisions WHERE id = ?`, id).
		Scan(&r.ID, &r.NoteID, &r.Name, &r.Content, &r.Tags, &r.Reason, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ApplyNoteRevision 把某条修订写回笔记。
// 回滚本身也是一次覆盖，所以先把「当前内容」强制留一条 rollback 修订 ——
// 否则用户发现滚错了版本就没法再滚回来。
func (d *Database) ApplyNoteRevision(id string) (*Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var rev NoteRevision
	err := d.conn.QueryRow(`SELECT id, note_id, name, content, tags, reason, created_at
		FROM note_revisions WHERE id = ?`, id).
		Scan(&rev.ID, &rev.NoteID, &rev.Name, &rev.Content, &rev.Tags, &rev.Reason, &rev.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("修订不存在: %s", id)
		}
		return nil, err
	}
	if err := captureNoteRevisionForcedLocked(d.conn, rev.NoteID, noteRevisionRollback); err != nil {
		logger.W("[note] 回滚前留痕失败（继续回滚）id=%s: %v", rev.NoteID, err)
	}
	now := time.Now().Format(time.RFC3339)
	res, err := d.conn.Exec(`UPDATE notes SET content = ?, tags = ?, name = ?, updated_at = ?
		WHERE id = ? AND is_folder = 0`, rev.Content, rev.Tags, rev.Name, now, rev.NoteID)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return nil, fmt.Errorf("笔记不存在或不是文档: %s", rev.NoteID)
	}
	// 回滚改了 content/tags/name（都是 notes_fts 的索引列），必须同步索引，
	// 否则搜到的还是回滚前的旧内容。调用方已持 d.mu，直接调不加锁的增量同步。
	if err := d.syncNoteIndexRow(rev.NoteID); err != nil {
		return nil, err
	}
	return d.getNoteNodeLocked(rev.NoteID)
}

// captureNoteRevisionForcedLocked 绕过节流强制留痕（回滚、导入、剪藏入口）。
func captureNoteRevisionForcedLocked(q noteRevisionConn, noteID, reason string) error {
	if noteID == "" {
		return nil
	}
	rows, err := q.Query(`SELECT name, content, tags FROM notes
		WHERE id = ? AND is_folder = 0 AND keyword <> ? LIMIT 1`, noteID, NoteQuickKeyword)
	if err != nil {
		return err
	}
	var name, content, tags string
	found := false
	if rows.Next() {
		if err := rows.Scan(&name, &content, &tags); err != nil {
			rows.Close()
			return err
		}
		found = true
	}
	rows.Close()
	if !found {
		return nil
	}
	return insertNoteRevisionLocked(q, NoteRevision{
		ID:        newID(),
		NoteID:    noteID,
		Name:      name,
		Content:   content,
		Tags:      tags,
		Reason:    reason,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
}

// getNoteNodeLocked 读单个树节点（调用方持锁，直接用 conn 避免 RWMutex 重入死锁）。
func (d *Database) getNoteNodeLocked(id string) (*Note, error) {
	rows, err := d.conn.Query(`SELECT `+noteCols+` FROM notes WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("笔记不存在: %s", id)
	}
	return scanNoteNode(rows)
}

// GetNoteNode 读单个树节点（对外只读入口）。
func (d *Database) GetNoteNode(id string) (*Note, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.getNoteNodeLocked(id)
}

// ListNoteTags 后端聚合全部标签，免得前端每次拉整棵树再解析 JSON。
func (d *Database) ListNoteTags() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT tags FROM notes WHERE is_folder = 0 AND keyword <> ? AND tags <> ''`, NoteQuickKeyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		for _, t := range parseNoteTags(raw) {
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, rows.Err()
}

// parseNoteTags 解析 tags 字段。历史上有两种落库口径：JSON 数组字符串，
// 以及早期手输的逗号分隔串。两种都认，避免升级后老笔记的标签突然消失。
func parseNoteTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	if strings.HasPrefix(raw, "[") {
		// 不引 encoding/json 也行，但 tag 里可能有转义引号，交给 json 更稳。
		var arr []string
		if err := json.Unmarshal([]byte(raw), &arr); err == nil {
			for _, t := range arr {
				if t = strings.TrimSpace(t); t != "" {
					out = append(out, t)
				}
			}
			return out
		}
	}
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(strings.Trim(t, `"`)); t != "" {
			out = append(out, t)
		}
	}
	return out
}
