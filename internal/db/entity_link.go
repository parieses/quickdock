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
type LinkTarget struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

const linkCols = `id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at`
const maxLinkTargets = 2000

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

// resolveLinkTarget 按 hint 找目标，返回 (类型, id)，找不到返回 ("", "")。
// hint 明确就只查那一类；hint 为空则按 notes → todos → items 顺序，第一个命中的为准。
func resolveLinkTarget(q linkRowQuerier, hintType, label string) (string, string) {
	if label == "" {
		return "", ""
	}
	switch hintType {
	case "note":
		if id := lookupLabel(q, "notes", "name", label, " AND is_folder = 0"); id != "" {
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
		if id := lookupLabel(q, "notes", "name", label, " AND is_folder = 0"); id != "" {
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
	refs := linkutil.ExtractWikiLabels(content)
	return d.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM entity_links WHERE src_type = 'note' AND src_id = ? AND kind = 'wiki'`,
			noteID,
		); err != nil {
			return err
		}
		for _, ref := range refs {
			foundType, foundID := resolveLinkTarget(tx, ref.Type, ref.Label)
			dstType := foundType
			if foundID == "" {
				// 没找到目标：dst_id 留空，dst_label 存标签原文；带 hint 的把 hint
				// 留作 dst_type（用于兜底时的同类限定），无 hint 的自然留空。
				dstType = ref.Type
			}
			// 用 OR IGNORE：同一正文里 [[标题]] 与 [[note:标题]] 可能解析到同一目标，
			// 唯一索引会挡住重复边，忽略即可，不必在 Go 侧再判一次。
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO entity_links (id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				newID(), "note", noteID, dstType, foundID, ref.Label, "wiki", now(),
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// FindLinkTargets 供解析单个 wiki 标签 / ResolveWikiTarget 用：按 hint 精确找目标，
// 返回 (类型, id)，找不到时 id 为空串（调用方据此判「未找到」）。
func (d *Database) FindLinkTargets(hintType, label string) (dstType, dstID string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return resolveLinkTarget(d.conn, hintType, label)
}

// queryLinkTargets 收集一类可选主体。名称为空的排除（不可链接），各限 2000 条按名称排序。
func (d *Database) queryLinkTargets(q string) ([]LinkTarget, error) {
	rows, err := d.Query(q, maxLinkTargets)
	if err != nil {
		return nil, err
	}
	out := make([]LinkTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, LinkTarget{ID: str(r["id"]), Label: str(r["label"])})
	}
	return out, nil
}

// ListLinkTargets 给关联选择器 / 自动补全用：三类主体各自的候选项。
// notes 排除 is_folder=1（文件夹没有正文，不是合法链接目标）。
func (d *Database) ListLinkTargets() (notes, todos, items []LinkTarget, err error) {
	notes, err = d.queryLinkTargets(
		`SELECT id, name AS label FROM notes WHERE is_folder = 0 AND name <> '' ORDER BY name ASC LIMIT ?`)
	if err != nil {
		return nil, nil, nil, err
	}
	todos, err = d.queryLinkTargets(
		`SELECT id, title AS label FROM todos WHERE title <> '' ORDER BY title ASC LIMIT ?`)
	if err != nil {
		return nil, nil, nil, err
	}
	items, err = d.queryLinkTargets(
		`SELECT id, name AS label FROM items WHERE name <> '' ORDER BY name ASC LIMIT ?`)
	if err != nil {
		return nil, nil, nil, err
	}
	return notes, todos, items, nil
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
