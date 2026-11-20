package db

import (
	"database/sql"
	"fmt"
	"strings"

	"quickdock/internal/linkutil"
)

// entity_links 是「条目 ↔ 笔记 ↔ 待办」的互链边表：三类主体都没有互指字段，
// 关联只能外挂。每行一条有向边，kind 区分 wiki（正文 [[...]] 解析出来的派生数据）
// 与 manual（用户在界面上显式建的关联）。
//
// ── 关于「删除主体时为什么不级联删关联行」──────────────────────────────
// 刻意不做删除级联，改为查询侧容错（直接返回存下的行，靠 dst_label 标题快照
// 兜底展示），原因有两条：
//  1. 回收站是「整行 JSON 快照 + 硬删 + BulkInsert 恢复」。主体从回收站恢复后
//     id 不变，若当初删主体时把关联一并删了，恢复主体也不会把关联带回来 ——
//     用户会看到「笔记找回来了，链接却全没了」。留着边，恢复即自动复原。
//  2. todos 是纯硬删、不进回收站，本来就不可恢复；为它单独写一套级联只会让
//     三类主体的删除路径分叉，收益却为零。
// 代价是库里会留下指向已删主体的悬空边，但它们只是展示数据：ListEntityLinks /
// ListEntityBacklinks 不 join 主体表，不会因目标不存在而报错或漏行，前端用
// dst_label 显示即可。关联是可重建的派生数据，正文与用户的显式关联才是真源。
// ────────────────────────────────────────────────────────────────────

// EntityLink 一条互链边。json tag 用 camelCase：Wails 序列化 ApiResult.Data 时
// 直接按 tag 输出，前端拿到的就是 srcType/srcId/... 而非 snake_case。
type EntityLink struct {
	ID        string `json:"id"`
	SrcType   string `json:"srcType"`
	SrcID     string `json:"srcId"`
	DstType   string `json:"dstType"`
	DstID     string `json:"dstId"`
	DstLabel  string `json:"dstLabel"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
}

// LinkTarget 关联选择器里的一类候选项。
// Hint 是给重名项做区分的次要信息（笔记给父文件夹、待办给到期日、条目给所属集合），
// 允许为空，前端在空时不渲染。
type LinkTarget struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

const linkCols = `id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at`
const maxLinkTargets = 2000

// 服务端候选搜索的单类上限。搜索结果只用来补「本地已加载集合里没有」的漏网项，
// 给太多反而把下拉撑长，200 足够翻。
const maxSearchTargets = 200

// 三类候选的公用 SELECT，统一输出 id / label / hint 三列。
// notes 用父文件夹名、todos 用到期日、items 用所属集合名当 hint ——
// 同名日报、跨集合的同名快捷方式光看标题分不清。
//
// notes 必须显式排除快捷笔记：快捷笔记与树共用 notes 表，且它的 name 会被写成
// "__quicknote__"（非空），只靠 name <> ” 挡不住，会在候选与图谱里变成一个
// 谁点都点不到、且新人不知为何物的幽灵条目。
const (
	linkTargetNotesQ = `SELECT n.id AS id, n.name AS label, COALESCE(p.name, '') AS hint
		FROM notes n LEFT JOIN notes p ON p.id = n.parent_id
		WHERE n.is_folder = 0 AND n.name <> '' AND n.keyword <> ?
		ORDER BY n.name ASC LIMIT ?`
	linkTargetTodosQ = `SELECT id, title AS label, COALESCE(due_date, '') AS hint
		FROM todos WHERE title <> '' ORDER BY title ASC LIMIT ?`
	linkTargetItemsQ = `SELECT i.id AS id, i.name AS label, COALESCE(c.name, '') AS hint
		FROM items i LEFT JOIN collections c ON c.id = i.collection_id
		WHERE i.name <> '' ORDER BY i.name ASC LIMIT ?`

	searchTargetNotesQ = `SELECT n.id AS id, n.name AS label, COALESCE(p.name, '') AS hint
		FROM notes n LEFT JOIN notes p ON p.id = n.parent_id
		WHERE n.is_folder = 0 AND n.name <> '' AND n.keyword <> ? AND n.name LIKE ? ESCAPE '\'
		ORDER BY n.name ASC LIMIT ?`
	searchTargetTodosQ = `SELECT id, title AS label, COALESCE(due_date, '') AS hint
		FROM todos WHERE title <> '' AND title LIKE ? ESCAPE '\'
		ORDER BY title ASC LIMIT ?`
	searchTargetItemsQ = `SELECT i.id AS id, i.name AS label, COALESCE(c.name, '') AS hint
		FROM items i LEFT JOIN collections c ON c.id = i.collection_id
		WHERE i.name <> '' AND i.name LIKE ? ESCAPE '\'
		ORDER BY i.name ASC LIMIT ?`
)

// ListAllEntityLinks 未传 / 传非正 limit 时的兜底上限：图谱一次构图不该把
// 超大边表整表读进内存；正常调用方（GraphData）总会带上自己的上限。
const maxAllEntityLinks = 5000

func validEntityType(t string) bool {
	return t == "note" || t == "todo" || t == "item"
}

func validLinkKind(k string) bool {
	return k == "manual" || k == "wiki"
}

// linkRowQuerier 让同一套目标解析逻辑既能跑在普通连接（FindLinkTargets）上，
// 也能跑在 SyncNoteWikiLinks 的事务上 —— 后者持写锁期间不能再调带锁的 d.* 方法，
// 否则 RWMutex 重入死锁。
type linkRowQuerier interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

// lookupLabel 在指定表按标题精确命中第一条，返回其 id；找不到返回空串。
// table/col/extra 全是代码内置常量（非用户输入），直接拼 SQL 安全。
func lookupLabel(q linkRowQuerier, table, col, label, extra string) string {
	var id string
	err := q.QueryRow(
		"SELECT id FROM "+table+" WHERE "+col+" = ?"+extra+" ORDER BY created_at ASC LIMIT 1",
		label,
	).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// noteTargetExtra notes 作为链接目标时的附加条件：文件夹没有正文，快捷笔记
// 不是树的一部分（它的 name 就是 "__quicknote__"），两者都不该被 [[标题]] 解析命中。
// 常量拼接安全：两个片段都是代码内置常量，不含用户输入。
const noteTargetExtra = " AND is_folder = 0 AND keyword <> '" + NoteQuickKeyword + "'"

// resolveLinkTarget 按 hint 找目标，返回 (类型, id)，找不到返回 ("", "")。
// hint 明确就只查那一类；hint 为空则按 notes → todos → items 顺序，第一个命中的为准。
func resolveLinkTarget(q linkRowQuerier, hintType, label string) (string, string) {
	if label == "" {
		return "", ""
	}
	switch hintType {
	case "note":
		if id := lookupLabel(q, "notes", "name", label, noteTargetExtra); id != "" {
			return "note", id
		}
	case "todo":
		if id := lookupLabel(q, "todos", "title", label, ""); id != "" {
			return "todo", id
		}
	case "item":
		if id := lookupLabel(q, "items", "name", label, ""); id != "" {
			return "item", id
		}
	default:
		if id := lookupLabel(q, "notes", "name", label, noteTargetExtra); id != "" {
			return "note", id
		}
		if id := lookupLabel(q, "todos", "title", label, ""); id != "" {
			return "todo", id
		}
		if id := lookupLabel(q, "items", "name", label, ""); id != "" {
			return "item", id
		}
	}
	return "", ""
}

// AddEntityLink 新增一条边。用 INSERT OR IGNORE 让唯一索引挡住重复边，
// 再按唯一键把「真正落库的那一行」查回来返回（重复时被忽略的是新 id，库里留的是旧行）。
func (d *Database) AddEntityLink(srcType, srcID, dstType, dstID, dstLabel, kind string) (EntityLink, error) {
	if !validEntityType(srcType) || !validEntityType(dstType) {
		return EntityLink{}, fmt.Errorf("非法实体类型")
	}
	if srcID == "" {
		return EntityLink{}, fmt.Errorf("源 id 不能为空")
	}
	if kind == "" {
		kind = "manual"
	}
	if !validLinkKind(kind) {
		return EntityLink{}, fmt.Errorf("非法关联类型: %s", kind)
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	l := EntityLink{
		ID:        newID(),
		SrcType:   srcType,
		SrcID:     srcID,
		DstType:   dstType,
		DstID:     dstID,
		DstLabel:  dstLabel,
		Kind:      kind,
		CreatedAt: now(),
	}
	if _, err := d.conn.Exec(
		`INSERT OR IGNORE INTO entity_links (id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.SrcType, l.SrcID, l.DstType, l.DstID, l.DstLabel, l.Kind, l.CreatedAt,
	); err != nil {
		return EntityLink{}, err
	}
	// 返回落库行而不是刚构造的 l：唯一键撞车时 OR IGNORE 什么都没插，
	// 库里那条的 id/created_at 与我们生成的不同。
	row := d.conn.QueryRow(
		`SELECT `+linkCols+` FROM entity_links
		 WHERE src_type = ? AND src_id = ? AND dst_type = ? AND dst_id = ? AND dst_label = ? AND kind = ?`,
		srcType, srcID, dstType, dstID, dstLabel, kind,
	)
	var out EntityLink
	if err := row.Scan(&out.ID, &out.SrcType, &out.SrcID, &out.DstType, &out.DstID,
		&out.DstLabel, &out.Kind, &out.CreatedAt); err != nil {
		return EntityLink{}, err
	}
	return out, nil
}

// RemoveEntityLink 按主键删一条边。
func (d *Database) RemoveEntityLink(id string) error {
	if id == "" {
		return fmt.Errorf("关联 id 不能为空")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM entity_links WHERE id = ?`, id)
	return err
}

func linkFromMap(m map[string]interface{}) EntityLink {
	return EntityLink{
		ID:        str(m["id"]),
		SrcType:   str(m["src_type"]),
		SrcID:     str(m["src_id"]),
		DstType:   str(m["dst_type"]),
		DstID:     str(m["dst_id"]),
		DstLabel:  str(m["dst_label"]),
		Kind:      str(m["kind"]),
		CreatedAt: str(m["created_at"]),
	}
}

// ListEntityLinks 列出某主体的全部出边，按 created_at 排序。
// 不 join 主体表：悬空的 dst 只是展示数据，查询侧容错即可（见文件头注释）。
func (d *Database) ListEntityLinks(srcType, srcID string) ([]EntityLink, error) {
	if !validEntityType(srcType) {
		return nil, fmt.Errorf("非法实体类型: %s", srcType)
	}
	if srcID == "" {
		return []EntityLink{}, nil
	}
	rows, err := d.Query(
		`SELECT `+linkCols+` FROM entity_links WHERE src_type = ? AND src_id = ? ORDER BY created_at ASC, id ASC`,
		srcType, srcID,
	)
	if err != nil {
		return nil, err
	}
	out := make([]EntityLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, linkFromMap(r))
	}
	return out, nil
}

// ListEntityBacklinks 反链核心：既按 dst_id 精确命中（已解析出目标 id 的边），
// 也在「目标当时不存在」——边上的 dst_id 为空串——时按 dst_label 标题兜底命中。
// 这样 wiki 写 [[标题]] 当时目标还没建、后来创建了，新目标也能看到这条反链。
//
// 兜底分支额外加「dst_type 为空串或与目标同类」：无前缀、未解析的边记的
// dst_type 为空，对任意类型目标都应命中；带前缀（如 [[todo:X]]）的边只在同类
// 目标下命中，避免它跑到同名笔记的反链里。
func (d *Database) ListEntityBacklinks(dstType, dstID, dstLabel string) ([]EntityLink, error) {
	if !validEntityType(dstType) {
		return nil, fmt.Errorf("非法实体类型: %s", dstType)
	}
	var clauses []string
	var args []interface{}
	if dstID != "" {
		clauses = append(clauses, `(dst_type = ? AND dst_id = ?)`)
		args = append(args, dstType, dstID)
	}
	if dstLabel != "" {
		clauses = append(clauses, `(dst_id = '' AND dst_label = ? AND (dst_type = '' OR dst_type = ?))`)
		args = append(args, dstLabel, dstType)
	}
	if len(clauses) == 0 {
		return []EntityLink{}, nil
	}
	q := `SELECT ` + linkCols + ` FROM entity_links WHERE ` + strings.Join(clauses, " OR ") +
		` ORDER BY created_at ASC, id ASC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	// 两个分支互斥（dst_id 是否为空），理论上不会重复；仍按 id 去重，
	// 保证将来条件调整后同一条边不会被前端渲染两次。
	out := make([]EntityLink, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		l := linkFromMap(r)
		if seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		out = append(out, l)
	}
	return out, nil
}

// linkExecQuerier 让「重建 wiki 出边」既能跑在普通连接的写锁内（改名回写），
// 也能跑在事务上（保存正文）。两者都满足 QueryRow + Exec。
type linkExecQuerier interface {
	linkRowQuerier
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// rebuildNoteWikiLinks 用正文重建某篇笔记的 wiki 出边：先删旧边（只删 kind='wiki'，
// 用户显式建的 manual 边绝不碰），再按 [[...]] 逐条解析目标后插入。
// 重复边交给唯一索引 OR IGNORE 挡住 —— 同一正文里 [[标题]] 与 [[note:标题]]
// 可能解析到同一目标，不必在 Go 侧再判一次。
func rebuildNoteWikiLinks(q linkExecQuerier, noteID, content string) error {
	if _, err := q.Exec(
		`DELETE FROM entity_links WHERE src_type = 'note' AND src_id = ? AND kind = 'wiki'`,
		noteID,
	); err != nil {
		return err
	}
	for _, ref := range linkutil.ExtractWikiLabels(content) {
		foundType, foundID := resolveLinkTarget(q, ref.Type, ref.Label)
		dstType := foundType
		if foundID == "" {
			// 没找到目标：dst_id 留空，dst_label 存标签原文；带 hint 的把 hint
			// 留作 dst_type（用于兜底时的同类限定），无 hint 的自然留空。
			dstType = ref.Type
		}
		if _, err := q.Exec(
			`INSERT OR IGNORE INTO entity_links (id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID(), "note", noteID, dstType, foundID, ref.Label, "wiki", now(),
		); err != nil {
			return err
		}
	}
	return nil
}

// rewriteWikiRefsLocked 在笔记改名后回写引用它的地方。
//
// 两类数据要一起修：
//  1. 正文里的 [[旧标题]] / [[note:旧标题]] —— 不改的话，这条链接立刻变成悬空链接，
//     点进去提示「未找到对应条目」，而用户只是改了个名字。
//  2. 当初没解析出目标的边（dst_id=”）的 dst_label —— 这类边靠标题兜底命中反链，
//     标题不跟着改，反链就会从新标题那篇笔记里消失。
//
// 已解析出 dst_id 的边不动：它们按 id 匹配，标题改了也不影响。
// 调用方必须已持有写锁（本函数直接走 d.conn，不再加锁，避免 RWMutex 重入死锁）。
func (d *Database) rewriteWikiRefsLocked(oldName, newName string) error {
	if oldName == "" || oldName == newName {
		return nil
	}
	// 1. 未解析的边改标题。UPDATE OR IGNORE：若新标题那条边已经存在，
	// 唯一索引会挡住这次更新，留下旧行由正文回写后的重建流程统一收口。
	if _, err := d.conn.Exec(
		`UPDATE OR IGNORE entity_links SET dst_label = ?
		 WHERE kind = 'wiki' AND dst_id = '' AND dst_label = ? AND (dst_type = '' OR dst_type = 'note')`,
		newName, oldName,
	); err != nil {
		return err
	}

	// 2. 正文回写。LIKE 只做粗筛（转义后精确匹配字面 [[标题]]），
	// 真正的替换用 ReplaceAll：整串匹配，不会误伤 [[旧标题加后缀]]。
	oldPlain := "[[" + oldName + "]]"
	oldTyped := "[[note:" + oldName + "]]"
	newPlain := "[[" + newName + "]]"
	newTyped := "[[note:" + newName + "]]"
	rows, err := d.conn.Query(
		`SELECT id, content FROM notes
		 WHERE keyword <> ? AND (content LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\')`,
		NoteQuickKeyword, "%"+escapeLike(oldPlain)+"%", "%"+escapeLike(oldTyped)+"%",
	)
	if err != nil {
		return err
	}
	type pendingRewrite struct{ id, content string }
	var pending []pendingRewrite
	for rows.Next() {
		var id, content string
		if err := rows.Scan(&id, &content); err != nil {
			continue
		}
		next := strings.ReplaceAll(content, oldTyped, newTyped)
		next = strings.ReplaceAll(next, oldPlain, newPlain)
		if next != content {
			pending = append(pending, pendingRewrite{id: id, content: next})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range pending {
		// 不回写 updated_at：改名回写是系统行为，不该把陈年笔记顶到「最近修改」。
		if _, err := d.conn.Exec(`UPDATE notes SET content = ? WHERE id = ?`, p.content, p.id); err != nil {
			return err
		}
		// 正文变了，这篇的出边要跟着重建：目标可能从悬空变已解析，也可能反过来。
		if err := rebuildNoteWikiLinks(d.conn, p.id, p.content); err != nil {
			return err
		}
	}
	return nil
}

// SyncNoteWikiLinks 在笔记正文保存后重建它的 wiki 出边。
//
// 事务边界：删旧 wiki 边 + 逐条解析目标 + 插新边整组放进一个 d.Transaction，
// 持写锁直到提交，中途不会出现「边已清空、新的还没插上」的中间态被别的读看到。
// 事务内用 tx 直接解析目标，不调 d.FindLinkTargets（那会再取 RLock 造成重入死锁）。
//
// 手动边保护：DELETE 只带 kind = 'wiki'，用户显式建的 kind = 'manual' 边绝不碰。
func (d *Database) SyncNoteWikiLinks(noteID, content string) error {
	if noteID == "" {
		return fmt.Errorf("笔记 id 不能为空")
	}
	// 快捷笔记与树共用 notes 表，但它不进树、不进搜索，也就不该在图谱里
	// 变成一个谁也点不到的悬空节点。统一在这里挡掉，上层所有「写了正文」
	// 的入口都能无脑调本函数，不必各自记「这个要跳过」。
	var kw string
	if err := d.conn.QueryRow(`SELECT keyword FROM notes WHERE id = ?`, noteID).Scan(&kw); err == nil &&
		kw == NoteQuickKeyword {
		return nil
	}
	return d.Transaction(func(tx *sql.Tx) error {
		return rebuildNoteWikiLinks(tx, noteID, content)
	})
}

// FindLinkTargets 供解析单个 wiki 标签 / ResolveWikiTarget 用：按 hint 精确找目标，
// 返回 (类型, id)，找不到时 id 为空串（调用方据此判「未找到」）。
func (d *Database) FindLinkTargets(hintType, label string) (dstType, dstID string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return resolveLinkTarget(d.conn, hintType, label)
}

// queryLinkTargets 执行一条返回 (id, label, hint) 三列的查询，把行映射成候选。
// args 原样透传，SQL 由调用方用常量拼接，不接受外部输入拼串。
func (d *Database) queryLinkTargets(q string, args ...interface{}) ([]LinkTarget, error) {
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]LinkTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, LinkTarget{ID: str(r["id"]), Label: str(r["label"]), Hint: str(r["hint"])})
	}
	return out, nil
}

// ListLinkTargets 给关联选择器 / 自动补全用：三类主体各自的候选项。
// notes 排除 is_folder=1（文件夹没有正文）与快捷笔记（见 linkTargetNotesQ 注释）。
func (d *Database) ListLinkTargets() (notes, todos, items []LinkTarget, err error) {
	notes, err = d.queryLinkTargets(linkTargetNotesQ, NoteQuickKeyword, maxLinkTargets)
	if err != nil {
		return nil, nil, nil, err
	}
	todos, err = d.queryLinkTargets(linkTargetTodosQ, maxLinkTargets)
	if err != nil {
		return nil, nil, nil, err
	}
	items, err = d.queryLinkTargets(linkTargetItemsQ, maxLinkTargets)
	if err != nil {
		return nil, nil, nil, err
	}
	return notes, todos, items, nil
}

// SearchLinkTargets 服务端候选搜索：按标题 LIKE 命中，补上「候选全量被 LIMIT 截断」
// 后本地再筛也筛不到的那部分。typ 非空只查该类，空串查三类。
//
// 为什么不把全量候选一次性给前端就算了：三类各 2000 条上限，第 2001 条起
// 永远进不了下拉，而且用户完全无感 —— 表现为「明明有这篇笔记，就是搜不到」。
// 拼音匹配仍在前端本地集合内做，服务端这里只承担兜底补齐。
func (d *Database) SearchLinkTargets(typ, keyword string, limit int) (notes, todos, items []LinkTarget, err error) {
	if limit <= 0 || limit > maxSearchTargets {
		limit = maxSearchTargets
	}
	pat := "%" + escapeLike(strings.TrimSpace(keyword)) + "%"
	switch typ {
	case "note":
		notes, err = d.queryLinkTargets(searchTargetNotesQ, NoteQuickKeyword, pat, limit)
	case "todo":
		todos, err = d.queryLinkTargets(searchTargetTodosQ, pat, limit)
	case "item":
		items, err = d.queryLinkTargets(searchTargetItemsQ, pat, limit)
	case "":
		if notes, err = d.queryLinkTargets(searchTargetNotesQ, NoteQuickKeyword, pat, limit); err != nil {
			return nil, nil, nil, err
		}
		if todos, err = d.queryLinkTargets(searchTargetTodosQ, pat, limit); err != nil {
			return nil, nil, nil, err
		}
		if items, err = d.queryLinkTargets(searchTargetItemsQ, pat, limit); err != nil {
			return nil, nil, nil, err
		}
	default:
		return nil, nil, nil, fmt.Errorf("非法实体类型: %s", typ)
	}
	return notes, todos, items, err
}

// EntityLabels 按类型批量查标题，给反链补来源名用。
// 返回 map[id]label；已删除的 id 不会出现在结果里，由调用方降级成「已删除」。
func (d *Database) EntityLabels(typ string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var table, col string
	switch typ {
	case "note":
		table, col = "notes", "name"
	case "todo":
		table, col = "todos", "title"
	case "item":
		table, col = "items", "name"
	default:
		return out, nil
	}
	// 去重后再拼占位符：同一条来源被多篇笔记引用时 id 会重复出现。
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	args := make([]interface{}, 0, len(uniq))
	for _, id := range uniq {
		args = append(args, id)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(uniq)), ",")
	rows, err := d.Query(`SELECT id, `+col+` AS label FROM `+table+` WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out[str(r["id"])] = str(r["label"])
	}
	return out, nil
}

// ListAllEntityLinks 一次性拉出全量互链边（带 LIMIT），供关系图谱构图。
//
// 与 ListEntityLinks / ListEntityBacklinks 同一取舍：刻意不 join 主体表，
// 悬空的 src / dst 都只是展示数据，原样返回、靠 dst_label 标题快照兜底即可
// （见文件头注释）。所以这里只 SELECT entity_links 本身，不做任何 EXISTS
// 过滤，也不会因主体已删而漏行。limit <= 0 时回落到 maxAllEntityLinks。
func (d *Database) ListAllEntityLinks(limit int) ([]EntityLink, error) {
	if limit <= 0 {
		limit = maxAllEntityLinks
	}
	rows, err := d.Query(
		`SELECT `+linkCols+` FROM entity_links ORDER BY created_at ASC, id ASC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	out := make([]EntityLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, linkFromMap(r))
	}
	return out, nil
}
