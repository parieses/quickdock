package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---- 笔记树（notes 表：文件夹 + 文档）----

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
}

const noteCols = `id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at`

func scanNoteNode(rows interface {
	Scan(dest ...interface{}) error
}) (*Note, error) {
	var n Note
	var isFolder, isNote int
	// 必须扫到临时 int 再转换：直接扫 &n.IsNote（bool）后又被 isNote 覆盖，
	// 会让所有节点的 isNote 恒为 false（读模型与库里的 is_note 不一致）。
	err := rows.Scan(&n.ID, &n.Keyword, &n.Content, &n.Category, &n.Name, &n.ParentID,
		&isFolder, &n.Sort, &n.Tags, &isNote, &n.Format, &n.CreatedAt)
	n.IsFolder = isFolder != 0
	n.IsNote = isNote != 0
	if n.Format == "" {
		n.Format = "markdown"
	}
	return &n, err
}

// ListNotesTree 返回全量笔记树节点（含文件夹与文档），由前端拼树。
func (d *Database) ListNotesTree() ([]Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(`SELECT ` + noteCols + ` FROM notes ORDER BY is_folder DESC, sort ASC, name ASC`)
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

// CreateNoteFolder 新建文件夹节点（is_folder=1）。
func (d *Database) CreateNoteFolder(parentID, name string) (*Note, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("文件夹名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
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
	_, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Keyword, n.Content, "folder", n.Name, n.ParentID, b2i(n.IsFolder), n.Sort, "[]", 0, "markdown", n.CreatedAt)
	return n, err
}

// CreateNoteDoc 新建文档节点（is_folder=0），parentID 可为空（根）。
// format: markdown | text（决定前端是否显示预览分栏）
func (d *Database) CreateNoteDoc(parentID, name, content, format string) (*Note, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
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
	_, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, category, name, parent_id, is_folder, sort, tags, is_note, format, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Keyword, n.Content, "note", n.Name, n.ParentID, b2i(n.IsFolder), n.Sort, n.Tags, b2i(n.IsNote), n.Format, n.CreatedAt)
	return n, err
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

// RenameNoteNode 重命名节点（文件夹/文档共用）。
func (d *Database) RenameNoteNode(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("名称不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE notes SET name = ? WHERE id = ?`, name, id)
	return err
}

// UpdateNoteDoc 更新文档内容与标签。影响行数为 0 时报错，避免不存在的 id 静默成功。
func (d *Database) UpdateNoteDoc(id, content, tags string) error {
	if tags == "" {
		tags = "[]"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE notes SET content = ?, tags = ? WHERE id = ? AND is_folder = 0`, content, tags, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return nil
}

// UpdateNoteContent 只更新正文，保留标签（MCP note_update 未传 tags 时使用）。
func (d *Database) UpdateNoteContent(id, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE notes SET content = ? WHERE id = ? AND is_folder = 0`, content, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return nil
}

// UpdateNoteTags 只更新标签，保留正文（MCP note_update 只传 tags 时使用）。
func (d *Database) UpdateNoteTags(id, tags string) error {
	if tags == "" {
		tags = "[]"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE notes SET tags = ? WHERE id = ? AND is_folder = 0`, tags, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("笔记不存在或不是文档: %s", id)
	}
	return nil
}

// SetNoteFormat 设置笔记渲染格式（markdown | text）。
func (d *Database) SetNoteFormat(id, format string) error {
	if format != "markdown" && format != "text" {
		format = "markdown"
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE notes SET format = ? WHERE id = ? AND is_folder = 0`, format, id)
	return err
}

// MoveNoteNode 移动节点到新的父目录（并更新同级 sort）。
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
	if err := writeTombstone(d.conn, "notes", id, rowLabel(firstRow(noteRows)),
		map[string][]map[string]interface{}{"notes": noteRows}); err != nil {
		return err
	}
	qs := placeholders(len(ids))
	args := argsOf(ids)
	_, err = d.conn.Exec(`DELETE FROM notes WHERE id IN (`+qs+`)`, args...)
	return err
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

// SearchNotes 按名称/内容/标签搜索笔记（文档与文件夹名都搜；内容只搜文档）。
func (d *Database) SearchNotes(q string) ([]Note, error) {
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
		`SELECT `+noteCols+` FROM notes
		 WHERE name LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\'
		 ORDER BY is_folder DESC, sort ASC, name ASC`,
		like, like, like)
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
	if _, err := d.conn.Exec("INSERT INTO notes (id, keyword, content, category, created_at) VALUES (?, ?, ?, ?, ?)", n.ID, n.Keyword, n.Content, n.Category, n.CreatedAt); err != nil {
		return nil, err
	}
	return &n, nil
}

// UpdateNote 更新笔记内容（允许空内容）。
func (d *Database) UpdateNote(id, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("UPDATE notes SET content = ? WHERE id = ?", content, id)
	return err
}
