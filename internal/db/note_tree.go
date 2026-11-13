package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// ---- 笔记树（notes 表：文件夹 + 文档）----

// NoteQuickKeyword 快捷笔记的固定 keyword。
// 浮窗快捷笔记和树共用 notes 表，但它不是树的一部分：树列表、搜索、图谱、
// MCP 返回都得在这里统一挡掉，只靠前端 filter 会让搜索/AI 侧漏出一条
// 名为 __quicknote__ 的幽灵笔记。放在 db 包是为了让所有读路径口径一致。
const NoteQuickKeyword = "__quicknote__"

// Note 笔记节点（文件夹 = is_folder=1，文档 = is_folder=0）
type Note struct {
	ID        string `json:"id"`
	Keyword   string `json:"keyword"`
	Content   string `json:"content"`
	Category  string `json:"category"`
	Name      string `json:"name"`     // 树形节点的标题（文件夹/文档名）
	ParentID  string `json:"parentId"` // 父文件夹 ID；空=根
	IsFolder  bool   `json:"isFolder"`
	Sort      int    `json:"sort"`
	Tags      string `json:"tags"`   // 标签 JSON 数组字符串
	IsNote    bool   `json:"isNote"` // 是否笔记/文档（源自快捷笔记等）
	Format    string `json:"format"` // markdown | text（渲染方式）
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"` // 最后一次内容/标签/改名时间；树结构操作不改
}

const noteCols = `id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at, updated_at`

// noteColsQualified 供 JOIN notes_fts 的查询使用：FTS 虚表自己也有
// name/content/tags 三列，裸列名会直接报 ambiguous column name。
// items_fts 那条链路上是同样的处理，别把它简化回去。
var noteColsQualified = "notes." + strings.ReplaceAll(noteCols, ", ", ", notes.")

func scanNoteNode(rows interface {
	Scan(dest ...interface{}) error
}) (*Note, error) {
	var n Note
	var isFolder, isNote int
	// 必须扫到临时 int 再转换：直接扫 &n.IsNote（bool）后又被 isNote 覆盖，
	// 会让所有节点的 isNote 恒为 false（读模型与库里的 is_note 不一致）。
	err := rows.Scan(&n.ID, &n.Keyword, &n.Content, &n.Category, &n.Name, &n.ParentID,
		&isFolder, &n.Sort, &n.Tags, &isNote, &n.Format, &n.CreatedAt, &n.UpdatedAt)
	n.IsFolder = isFolder != 0
	n.IsNote = isNote != 0
	if n.Format == "" {
		n.Format = "markdown"
	}
	if n.UpdatedAt == "" {
		n.UpdatedAt = n.CreatedAt
	}
	return &n, err
}

// rebuildNoteIndex 重建笔记全文索引 notes_fts（trigram 普通内容表）。
//
// 为什么不用触发器增量维护：本构建下 trigram FTS5 表两种增量写法都踩坑——
//
//	① 命令式 'delete' 触发器（external content 写法）：写入即报 SQL logic error / 267；
//	② 普通 SQL DELETE 触发器（普通内容表写法）：在 AFTER 触发器里回删同一虚拟表会死锁
//	   （实测 go test 直接 hang 住）。
//
// 故改为「每次写入后整表重建」：先 DELETE 清空、再整表 INSERT 重 tokenize，绝不会残留
// 过期 gram（普通内容表整表清空后重扫，无部分删除的脏索引问题）。
// 若 notes_fts 已损坏（267，存量旧库常见），DELETE 也可能失败，此时 DROP 重建再来一次，
// 让损坏库在下次启动自愈。
//
// 调用方必须已持有 d.mu（所有写入函数与 migrate 均持锁）。函数本身不加锁，避免重入死锁。
func (d *Database) rebuildNoteIndex() error {
	if _, err := d.conn.Exec(`DELETE FROM notes_fts`); err != nil {
		// 索引可能已损坏（267），DROP 后照 notesFtsDDL 重建，再灌。
		logger.W("[db] 清空笔记全文索引失败，尝试 DROP 重建: %v", err)
		if _, derr := d.conn.Exec(`DROP TABLE IF EXISTS notes_fts`); derr != nil {
			return fmt.Errorf("删除损坏的笔记全文索引失败: %w（清空失败: %v）", derr, err)
		}
		if _, derr := d.conn.Exec(notesFtsDDL); derr != nil {
			return fmt.Errorf("重建笔记全文索引失败: %w（清空失败: %v）", derr, err)
		}
	}
	if _, err := d.conn.Exec(
		`INSERT INTO notes_fts(rowid, name, content, tags) SELECT rowid, name, content, tags FROM notes`); err != nil {
		return fmt.Errorf("灌入笔记全文索引失败: %w", err)
	}
	return nil
}

// rebuildNoteIndexWithLock 给「调用方没持 d.mu」的入口用（快照恢复等整库覆盖场景）。
// 其余写入方法都在自己的 d.mu.Lock() 内直接调不加锁的 rebuildNoteIndex。
func (d *Database) rebuildNoteIndexWithLock() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rebuildNoteIndex()
}

// ---- 写入函数：每个函数在落库后调用 rebuildNoteIndex 同步全文索引 ----
// 约定：*Locked 后缀的内部函数在「调用方已持 d.mu」的前提下工作，不自行加锁、不重建索引；
// 公开方法负责加锁 + 调 rebuildNoteIndex。ImportNoteBatch 走 *Locked 变体，
// 整批结束后只重建一次（避免每篇都重建 → O(N²)）。

// ListNotesTree 返回全量笔记树节点（含文件夹与文档），由前端拼树。
func (d *Database) ListNotesTree() ([]Note, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT `+noteCols+` FROM notes
		WHERE keyword <> ? ORDER BY is_folder DESC, sort ASC, name ASC`, NoteQuickKeyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNoteNode(rows)
		if err != nil {
			return nil, err
		}
		if n.Name == "" {
			n.Name = n.Keyword
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ensureParentFolderLocked 校验父节点存在且是文件夹（parentID 为空表示根目录）。
// 不做校验会插入「孤儿」节点：parentId 指向不存在的 id 或某个文档时，
// 前端树只在文件夹下渲染子节点，节点就会建了却看不见。调用方需持 d.mu。
func ensureParentFolderLocked(d *Database, parentID string) error {
	if parentID == "" {
		return nil
	}
	var isFolder int
	err := d.conn.QueryRow(`SELECT is_folder FROM notes WHERE id = ?`, parentID).Scan(&isFolder)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("父目录不存在: %s（先用 note_search 查文件夹 id，或留空建到根目录）", parentID)
	}
	if err != nil {
		return err
	}
	if isFolder == 0 {
		return fmt.Errorf("父节点不是文件夹: %s（只能挂到 isFolder=true 的节点下）", parentID)
	}
	return nil
}

// createNoteFolderLocked 新建文件夹节点（调用方需持 d.mu；不自行加锁、不重建索引）。
func (d *Database) createNoteFolderLocked(parentID, name string) (*Note, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("文件夹名称不能为空")
	}
	if err := ensureParentFolderLocked(d, parentID); err != nil {
		return nil, err
	}
	n := &Note{
		ID:        newID(),
		Keyword:   "f:" + newID(), // 占位，保证 UNIQUE
		Content:   "",
		Name:      name,
		ParentID:  parentID,
		IsFolder:  true,
		Sort:      nextSortLocked(d, "notes", parentID),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	n.UpdatedAt = n.CreatedAt
	if _, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Keyword, n.Content, "folder", n.Name, n.ParentID, b2i(n.IsFolder), n.Sort, "[]", 0, "markdown", n.CreatedAt, n.UpdatedAt); err != nil {
		return nil, err
	}
	return n, nil
}

// CreateNoteFolder 新建文件夹节点（is_folder=1），落库后重建全文索引。
func (d *Database) CreateNoteFolder(parentID, name string) (*Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, err := d.createNoteFolderLocked(parentID, name)
	if err != nil {
		return nil, err
	}
	if err := d.rebuildNoteIndex(); err != nil {
		return nil, err
	}
	return n, nil
}

// createNoteDocLocked 新建文档节点（调用方需持 d.mu；不自行加锁、不重建索引）。
// format: markdown | text（决定前端是否显示预览分栏）
func (d *Database) createNoteDocLocked(parentID, name, content, format string) (*Note, error) {
	if err := ensureParentFolderLocked(d, parentID); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "未命名笔记"
	}
	if format == "" {
		format = "markdown"
	}
	n := &Note{
		ID:        newID(),
		Keyword:   newID(),
		Content:   content,
		Name:      name,
		ParentID:  parentID,
		IsFolder:  false,
		Sort:      nextSortLocked(d, "notes", parentID),
		Tags:      "[]",
		IsNote:    true,
		Format:    format,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	n.UpdatedAt = n.CreatedAt
	if _, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Keyword, n.Content, "note", n.Name, n.ParentID, b2i(n.IsFolder), n.Sort, n.Tags, b2i(n.IsNote), n.Format, n.CreatedAt, n.UpdatedAt); err != nil {
		return nil, err
	}
	return n, nil
}

// CreateNoteDoc 新建文档节点（is_folder=0），parentID 可为空（根）。落库后重建全文索引。
func (d *Database) CreateNoteDoc(parentID, name, content, format string) (*Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n, err := d.createNoteDocLocked(parentID, name, content, format)
	if err != nil {
		return nil, err
	}
	if err := d.rebuildNoteIndex(); err != nil {
		return nil, err
	}
	return n, nil
}

// nextSortLocked 计算某父目录下最大的 sort+1（调用方需持 d.mu）
//
// table 参数此前被忽略、始终查 notes（笔记是唯一调用方，问题一直没暴露）。
// 现在 SSH 会话树也用它，改成按传入表名查询，并走白名单校验防注入。
func nextSortLocked(d *Database, table, parentID string) int {
	if err := validateTable(table); err != nil {
		return 0
	}
	var mx int
	err := d.conn.QueryRow(
		fmt.Sprintf("SELECT COALESCE(MAX(sort),0) FROM %s WHERE parent_id = ?", table),
		parentID,
	).Scan(&mx)
	if err != nil {
		return 0
	}
	return mx + 1
}

// RenameNoteNode 重命名节点（文件夹/文档共用）。落库后重建全文索引（name 在索引列）。
func (d *Database) RenameNoteNode(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(`UPDATE notes SET name = ?, updated_at = ? WHERE id = ?`,
		name, time.Now().Format(time.RFC3339), id); err != nil {
		return err
	}
	return d.rebuildNoteIndex()
}

// UpdateNoteDoc 更新文档内容与标签。影响行数为 0 时报错，避免不存在的 id 静默成功。
func (d *Database) UpdateNoteDoc(id, content, tags string) error {
	if tags == "" {
		tags = "[]"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// 先拍旧版本再覆盖。修订是派生数据，留痕失败只记日志、不回滚正文写入：
	// 一次「保存」绝不能因为历史表出问题而变成红色报错。
	if err := captureNoteRevisionLocked(d.conn, id, noteRevisionSave); err != nil {
		logger.W("[note] 留修订失败（正文照常保存）id=%s: %v", id, err)
	}
	res, err := d.conn.Exec(`UPDATE notes SET content = ?, tags = ?, updated_at = ? WHERE id = ? AND is_folder = 0`,
		content, tags, time.Now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return d.rebuildNoteIndex()
}

// UpdateNoteContent 只更新正文，保留标签（MCP note_update 未传 tags 时使用）。
func (d *Database) UpdateNoteContent(id, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := captureNoteRevisionLocked(d.conn, id, noteRevisionSave); err != nil {
		logger.W("[note] 留修订失败（正文照常保存）id=%s: %v", id, err)
	}
	res, err := d.conn.Exec(`UPDATE notes SET content = ?, updated_at = ? WHERE id = ? AND is_folder = 0`,
		content, time.Now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return d.rebuildNoteIndex()
}

// UpdateNoteTags 只更新标签，保留正文（MCP note_update 只传 tags 时使用）。
func (d *Database) UpdateNoteTags(id, tags string) error {
	if tags == "" {
		tags = "[]"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := captureNoteRevisionLocked(d.conn, id, noteRevisionSave); err != nil {
		logger.W("[note] 留修订失败（标签照常保存）id=%s: %v", id, err)
	}
	res, err := d.conn.Exec(`UPDATE notes SET tags = ?, updated_at = ? WHERE id = ? AND is_folder = 0`,
		tags, time.Now().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return d.rebuildNoteIndex()
}

// SetNoteFormat 设置笔记渲染格式（markdown | text）。
// 只改 format，不是 notes_fts 的索引列（name/content/tags），无需重建索引。
func (d *Database) SetNoteFormat(id, format string) error {
	if format != "markdown" && format != "text" {
		format = "markdown"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE notes SET format = ?, updated_at = ? WHERE id = ? AND is_folder = 0`,
		format, time.Now().Format(time.RFC3339), id)
	return err
}

// MoveNoteNode 移动节点到新的父目录（并更新同级 sort）。
// 只改 parent_id/sort，不是 notes_fts 的索引列，无需重建索引。
func (d *Database) MoveNoteNode(id, newParentID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 防环：不能把节点移进自己的子树
	if isSelfInSubtreeLocked(d, id, newParentID) {
		return fmt.Errorf("不能把节点移动到它自身的子目录下")
	}
	next := nextSortLocked(d, "notes", newParentID)
	_, err := d.conn.Exec(`UPDATE notes SET parent_id = ?, sort = ? WHERE id = ?`, newParentID, next, id)
	return err
}

// SetNoteSort 只改同级排序值（拖拽排序用）。
//
// 不塞进 MoveNoteNode：那个会改 parent_id 并重算 sort 到末尾，
// 拖一下排序却顺手换了父目录（或把节点甩到列表最后）不是用户要的结果。
// 只改 sort，不是 notes_fts 的索引列，无需重建索引。
func (d *Database) SetNoteSort(id string, sort int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE notes SET sort = ? WHERE id = ?`, sort, id)
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
		err := d.conn.QueryRow(`SELECT parent_id FROM notes WHERE id = ?`, cur).Scan(&parent)
		if err != nil || parent == "" {
			return false
		}
		cur = parent
	}
}

// DeleteNoteNode 递归删除节点及其子树（文件夹含其下所有）。
// 删除前把整棵子树拍成墓碑：误删一个文件夹时，恢复它即可连内容一起回来。
func (d *Database) DeleteNoteNode(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := collectSubtreeLocked(d, id)
	if len(ids) == 0 {
		return nil
	}
	// 捕获子树全部行（调用方已持 d.mu，直接用 conn，不走带锁的 AddTombstone）
	noteRows, err := captureSubtreeRows(d.conn, "notes", ids)
	if err != nil {
		return err
	}
	qs := placeholders(len(ids))
	args := argsOf(ids)
	payload := map[string][]map[string]interface{}{"notes": noteRows}
	// 修订跟着主体一起进墓碑：恢复一篇误删的笔记却把它的历史全丢了，
	// 用户会以为「恢复 = 回到删之前的样子」。captureRows 的 table 走白名单，
	// note_revisions 已登记；where 只拼占位符，值全部参数化。
	revRows, err := captureRows(d.conn, "note_revisions", "note_id IN ("+qs+")", args...)
	if err != nil {
		return err
	}
	if len(revRows) > 0 {
		payload["note_revisions"] = revRows
	}
	if err := writeTombstone(d.conn, "notes", id, rowLabel(firstRow(noteRows)), payload); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`DELETE FROM note_revisions WHERE note_id IN (`+qs+`)`, args...); err != nil {
		return err
	}
	if _, err = d.conn.Exec(`DELETE FROM notes WHERE id IN (`+qs+`)`, args...); err != nil {
		return err
	}
	return d.rebuildNoteIndex()
}

// firstRow 取首行（无行时返回空 map，rowLabel 会退回空串）。
func firstRow(rows []map[string]interface{}) map[string]interface{} {
	if len(rows) == 0 {
		return map[string]interface{}{}
	}
	return rows[0]
}

// collectSubtreeLocked 返回 id 及其所有子孙 id（调用方持 d.mu）。
func collectSubtreeLocked(d *Database, id string) []string {
	all, _ := d.conn.Query(`SELECT id, parent_id FROM notes`)
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

// scanNoteRows 把结果集扫成节点列表，统一处理 name 回退。
func scanNoteRows(rows *sql.Rows) ([]Note, error) {
	var out []Note
	for rows.Next() {
		n, err := scanNoteNode(rows)
		if err != nil {
			return nil, err
		}
		if n.Name == "" {
			n.Name = n.Keyword
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// SearchNotes 按名称/内容/标签搜索笔记（文档与文件夹名都搜；内容只搜文档）。
//
// 两条路径：
//  1. 查询里含 >=3 字的词 → 走 notes_fts（trigram 全文索引，按相关度排序）；
//  2. 全是很短的词、FTS 零命中、或 FTS 语句异常 → 退回原来的三列 LIKE。
//
// 不能只留 FTS：trigram 的索引单位就是 3 字，用户搜「DB」「v2」「日志」
// 这类 1–2 字词会恒定 0 结果，而中文输入法是逐字上屏的，短查询恰恰最高频。
// 也不能只留 LIKE：正文一大，每敲一个键全表扫三列会肉眼可见地卡。
//
// 纯读用读锁，且必须直接用 d.conn：d.Query 内部还会取 RLock，
// 在外层持锁时再调就是 RWMutex 重入死锁。
func (d *Database) SearchNotes(q string) ([]Note, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	if match := noteFTSMatch(q); match != "" {
		// MATCH 必须写真表名，不能给 FTS 表起别名（起别名后连 bm25() 都会报
		// no such column，知识库那边踩过）。rank 是 FTS5 的隐藏列，可直接排序。
		rows, err := d.conn.Query(`SELECT `+noteColsQualified+` FROM notes_fts
			JOIN notes ON notes.rowid = notes_fts.rowid
			WHERE notes_fts MATCH ? AND notes.keyword <> ?
			ORDER BY rank LIMIT 300`, match, NoteQuickKeyword)
		if err != nil {
			// 索引偶发不可用不该让搜索整个失败，下面还有 LIKE 兜底。
			logger.W("[note] 全文检索失败，回退 LIKE: %v", err)
		} else {
			out, serr := scanNoteRows(rows)
			rows.Close()
			if serr == nil && len(out) > 0 {
				return out, nil
			}
		}
	}

	escaped := strings.ReplaceAll(q, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	like := "%" + escaped + "%"
	rows, err := d.conn.Query(
		`SELECT `+noteColsQualified+` FROM notes
		 WHERE notes.keyword <> ? AND (
		   notes.name LIKE ? ESCAPE '\' OR notes.content LIKE ? ESCAPE '\' OR notes.tags LIKE ? ESCAPE '\')
		 ORDER BY notes.is_folder DESC, notes.sort ASC, notes.name ASC`,
		NoteQuickKeyword, like, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNoteRows(rows)
}

// ---- 快捷笔记（固定关键词 find-or-create 的笔记文档）----

// GetOrCreateNote 获取或创建固定关键词的笔记（允许空内容）。
func (d *Database) GetOrCreateNote(keyword string) (*Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	row := d.conn.QueryRow("SELECT id, keyword, content, category, created_at FROM notes WHERE keyword = ?", keyword)
	var n Note
	if err := row.Scan(&n.ID, &n.Keyword, &n.Content, &n.Category, &n.CreatedAt); err == nil {
		return &n, nil
	}
	n = Note{ID: newID(), Keyword: keyword, Content: "", Category: "note", CreatedAt: time.Now().Format(time.RFC3339)}
	n.UpdatedAt = n.CreatedAt
	if _, err := d.conn.Exec(
		"INSERT INTO notes (id, keyword, content, category, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		n.ID, n.Keyword, n.Content, n.Category, n.CreatedAt, n.UpdatedAt); err != nil {
		return nil, err
	}
	// 只有「新建」这一支改变了 notes 的行集合，必须重建索引让行数对得上；
	// 命中已存在的分支上面直接 return，不重建。
	if err := d.rebuildNoteIndex(); err != nil {
		return nil, err
	}
	return &n, nil
}

// UpdateNote 更新笔记内容（允许空内容）。改的是 content（索引列），落库后重建索引。
func (d *Database) UpdateNote(id, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec("UPDATE notes SET content = ?, updated_at = ? WHERE id = ?",
		content, time.Now().Format(time.RFC3339), id); err != nil {
		return err
	}
	return d.rebuildNoteIndex()
}
