package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// newLinkTestDB 建一个干净的临时库。Open 会跑完整迁移，notes_fts 之类都就位。
func newLinkTestDB(t *testing.T) *Database {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func insertTestNote(t *testing.T, d *Database, id, keyword, name, content string, folder int) {
	t.Helper()
	_, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, name, is_folder, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, keyword, content, name, folder,
	)
	if err != nil {
		t.Fatalf("insert note %s: %v", id, err)
	}
}

// 候选里不能出现快捷笔记：它的 name 就是 "__quicknote__"（非空），
// 只靠 name <> ” 挡不住，会以「笔记 __quicknote__」的形式混进下拉和图谱。
func TestLinkTargetsExcludeQuickNote(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "普通笔记", "正文", 0)
	insertTestNote(t, d, "q1", NoteQuickKeyword, NoteQuickKeyword, "", 0)
	insertTestNote(t, d, "f1", "kf", "文件夹", "", 1)

	notes, _, _, err := d.ListLinkTargets()
	if err != nil {
		t.Fatalf("ListLinkTargets: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "n1" {
		t.Fatalf("候选应只剩普通笔记，实际 %+v", notes)
	}
}

// 候选的 hint 要能区分重名：笔记给父文件夹名。
func TestLinkTargetsHintFromParent(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "f1", "kf", "项目甲", "", 1)
	insertTestNote(t, d, "n1", "k1", "日报", "", 0)
	if _, err := d.conn.Exec(`UPDATE notes SET parent_id = 'f1' WHERE id = 'n1'`); err != nil {
		t.Fatalf("设置父级: %v", err)
	}

	notes, _, _, err := d.ListLinkTargets()
	if err != nil {
		t.Fatalf("ListLinkTargets: %v", err)
	}
	if len(notes) != 1 || notes[0].Hint != "项目甲" {
		t.Fatalf("hint 应为父文件夹名，实际 %+v", notes)
	}
}

func TestEntityLabels(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "笔记甲", "", 0)
	if _, err := d.conn.Exec(
		`INSERT INTO todos (id, title, created_at) VALUES ('t1', '待办乙', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert todo: %v", err)
	}

	m, err := d.EntityLabels("note", []string{"n1", "n1", "gone", ""})
	if err != nil {
		t.Fatalf("EntityLabels: %v", err)
	}
	if m["n1"] != "笔记甲" {
		t.Fatalf("笔记标题没查到: %+v", m)
	}
	if _, ok := m["gone"]; ok {
		t.Fatalf("已删除的 id 不该出现在结果里: %+v", m)
	}

	tm, err := d.EntityLabels("todo", []string{"t1"})
	if err != nil {
		t.Fatalf("EntityLabels todo: %v", err)
	}
	if tm["t1"] != "待办乙" {
		t.Fatalf("待办标题没查到: %+v", tm)
	}
}

// 服务端搜索要能搜到全量 LIMIT 之外的条目，且 typ 能限定类型。
func TestSearchLinkTargets(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "季度复盘 2026Q1", "", 0)
	insertTestNote(t, d, "n2", "k2", "别的东西", "", 0)
	insertTestNote(t, d, "q1", NoteQuickKeyword, NoteQuickKeyword, "", 0)

	notes, todos, items, err := d.SearchLinkTargets("", "复盘", 50)
	if err != nil {
		t.Fatalf("SearchLinkTargets: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "n1" {
		t.Fatalf("应只命中 n1，实际 %+v", notes)
	}
	if len(todos) != 0 || len(items) != 0 {
		t.Fatalf("不该命中其它类型: todos=%+v items=%+v", todos, items)
	}

	// typ 限定后不该返回别的类型
	notes, _, _, err = d.SearchLinkTargets("todo", "复盘", 50)
	if err != nil {
		t.Fatalf("SearchLinkTargets todo: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("限定了 todo 不该返回 notes: %+v", notes)
	}

	// 快捷笔记在任何搜索下都不出现
	notes, _, _, err = d.SearchLinkTargets("", NoteQuickKeyword, 50)
	if err != nil {
		t.Fatalf("SearchLinkTargets quick: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("快捷笔记不该被搜出来: %+v", notes)
	}
}

// 改名要把别人正文里的 [[旧标题]] 改成新标题，并重建受影响的 wiki 出边。
func TestRenameNoteRewritesWikiRefs(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "旧标题", "自己的正文", 0)
	insertTestNote(t, d, "n2", "k2", "引用方", "看 [[旧标题]] 和 [[note:旧标题]] 都指它", 0)
	if _, err := d.conn.Exec(
		`INSERT INTO notes (id, keyword, content, name, is_folder, created_at, updated_at)
		 VALUES ('n3','k3','[[旧标题加后缀]] 不该被改','旁观者',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert n3: %v", err)
	}

	if err := d.RenameNoteNode("n1", "新标题"); err != nil {
		t.Fatalf("RenameNoteNode: %v", err)
	}

	var content string
	if err := d.conn.QueryRow(`SELECT content FROM notes WHERE id = 'n2'`).Scan(&content); err != nil {
		t.Fatalf("读 n2: %v", err)
	}
	if !strings.Contains(content, "[[新标题]]") || !strings.Contains(content, "[[note:新标题]]") {
		t.Fatalf("引用没回写: %q", content)
	}
	if strings.Contains(content, "旧标题") {
		t.Fatalf("旧标题残留: %q", content)
	}

	var other string
	if err := d.conn.QueryRow(`SELECT content FROM notes WHERE id = 'n3'`).Scan(&other); err != nil {
		t.Fatalf("读 n3: %v", err)
	}
	if !strings.Contains(other, "[[旧标题加后缀]]") {
		t.Fatalf("更长标题被误伤: %q", other)
	}

	// n2 的 wiki 出边应重建为指向 n1
	rows, err := d.conn.Query(
		`SELECT dst_type, dst_id FROM entity_links WHERE src_type='note' AND src_id='n2' AND kind='wiki'`)
	if err != nil {
		t.Fatalf("查边: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if typ == "note" && id == "n1" {
			found = true
		}
	}
	if !found {
		t.Fatal("改名后 n2 的 wiki 边没有重新解析到 n1")
	}
}

// 当初没解析出目标的边（dst_id=”）靠 dst_label 兜底命中反链，改名要一起改。
func TestRenameNoteUpdatesDanglingLinkLabel(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "旧标题", "", 0)
	insertTestNote(t, d, "n2", "k2", "引用方", "", 0)
	if _, err := d.conn.Exec(
		`INSERT INTO entity_links (id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at)
		 VALUES ('e1','note','n2','','','旧标题','wiki','2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("插入悬空边: %v", err)
	}

	if err := d.RenameNoteNode("n1", "新标题"); err != nil {
		t.Fatalf("RenameNoteNode: %v", err)
	}

	var label string
	if err := d.conn.QueryRow(`SELECT dst_label FROM entity_links WHERE id = 'e1'`).Scan(&label); err != nil {
		t.Fatalf("读边: %v", err)
	}
	if label != "新标题" {
		t.Fatalf("悬空边标题没更新: %q", label)
	}
}

// 重构后 SyncNoteWikiLinks 仍只重建 wiki 边，绝不碰用户手动建的 manual 边。
func TestSyncWikiLinksKeepsManualEdges(t *testing.T) {
	d := newLinkTestDB(t)
	insertTestNote(t, d, "n1", "k1", "目标", "", 0)
	insertTestNote(t, d, "n2", "k2", "源", "正文里没有 wiki 链接", 0)
	if _, err := d.conn.Exec(
		`INSERT INTO entity_links (id, src_type, src_id, dst_type, dst_id, dst_label, kind, created_at)
		 VALUES ('m1','note','n2','note','n1','目标','manual','2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("插入 manual 边: %v", err)
	}

	if err := d.SyncNoteWikiLinks("n2", "正文里没有 wiki 链接"); err != nil {
		t.Fatalf("SyncNoteWikiLinks: %v", err)
	}

	var n int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM entity_links WHERE id='m1'`).Scan(&n); err != nil {
		t.Fatalf("查 manual 边: %v", err)
	}
	if n != 1 {
		t.Fatal("manual 边被 wiki 重建误删")
	}
}
