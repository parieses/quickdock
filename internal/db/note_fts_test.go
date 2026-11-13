package db

import (
	"database/sql"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func reproDSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
}

// hits 直接查 notes_fts（普通内容表），返回 MATCH 命中行数。267 会在这里炸。
func noteFTSHits(t *testing.T, d *Database, kw string) int {
	t.Helper()
	rows, err := d.conn.Query(`SELECT rowid FROM notes_fts WHERE notes_fts MATCH ?`, kw)
	if err != nil {
		t.Fatalf("MATCH %q err: %v", kw, err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("MATCH %q rows err: %v", kw, err)
	}
	return n
}

// counts 断言「索引行数 == notes 行数」这条不变式（删掉触发器后靠主动重建保证）。
func noteFTSCounts(t *testing.T, d *Database) (notes, fts int) {
	t.Helper()
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&notes); err != nil {
		t.Fatalf("count notes: %v", err)
	}
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM notes_fts`).Scan(&fts); err != nil {
		t.Fatalf("count notes_fts: %v", err)
	}
	if notes != fts {
		t.Errorf("索引行数与正文不一致：notes=%d fts=%d", notes, fts)
	}
	return notes, fts
}

func assertIntegrity(t *testing.T, d *Database) {
	t.Helper()
	var integ string
	if err := d.conn.QueryRow(`PRAGMA integrity_check`).Scan(&integ); err != nil {
		t.Fatalf("integrity_check err: %v", err)
	}
	if integ != "ok" {
		t.Errorf("integrity_check = %q（应为 ok）", integ)
	}
}

// 造一个「旧库」：notes 表没有 updated_at、没有 notes_fts，塞入边界数据
// （NULL/空 name、空 tags、emoji、5000 字长文、文件夹），再让 Open 走迁移。
func makeLegacyNotesDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", reproDSN(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE notes (
		id TEXT PRIMARY KEY,
		keyword TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		category TEXT DEFAULT '',
		name TEXT DEFAULT '',
		parent_id TEXT DEFAULT '',
		is_folder INTEGER DEFAULT 0,
		sort INTEGER DEFAULT 0,
		tags TEXT DEFAULT '',
		is_note INTEGER DEFAULT 0,
		format TEXT DEFAULT 'markdown',
		created_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create old notes: %v", err)
	}
	rows := []struct {
		id, kw, content, name, tags string
		folder                      int
	}{
		{id: "n1", kw: "k1", content: "这是一篇关于数据库索引与查询优化的排查记录，讲慢查询怎么定位", name: "数据库索引", tags: `["db"]`},
		{id: "n2", kw: "k2", content: "quickdock release notes and changelog about the new note module", name: "Release Notes", tags: `["release"]`},
		{id: "n3", kw: "k3", content: "emoji 测试 🚀🔥 与中文混合数据库索引慢查询", name: "", tags: `[]`}, // name 空，旧数据缺省
		{id: "n4", kw: "k4", content: strings.Repeat("x", 5000), name: "长文", tags: ""},   // tags 空
		{id: "f1", kw: "fk", content: "folder", name: "我的文件夹", tags: `[]`, folder: 1},
	}
	for _, r := range rows {
		q := "INSERT INTO notes(id,keyword,content,category,name,parent_id,is_folder,sort,tags,is_note,format,created_at) VALUES(?,?,?,'note',?,?,?,?,?,0,'markdown','2026-01-01T00:00:00Z')"
		if _, err := raw.Exec(q, r.id, r.kw, r.content, r.name, "", r.folder, 0, r.tags); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}
	return path
}

// TestNoteFTSMigration 覆盖用户报的启动 267：旧库迁移建索引 + 数据回填必须无损坏。
func TestNoteFTSMigration(t *testing.T) {
	d, err := Open(makeLegacyNotesDB(t))
	if err != nil {
		t.Fatalf("db.Open (migrate): %v", err)
	}
	defer d.Close()

	assertIntegrity(t, d)
	noteFTSCounts(t, d) // 5 行全部进索引
	if got := noteFTSHits(t, d, "数据库"); got != 2 {
		t.Errorf("迁移后 MATCH '数据库' = %d（应为 2：n1 + n3）", got)
	}
	if got, err := d.SearchNotes("数据库索引"); err != nil {
		t.Errorf("SearchNotes err: %v", err)
	} else if len(got) == 0 {
		t.Error("SearchNotes('数据库索引') 零命中")
	} else {
		t.Logf("迁移后 SearchNotes 命中 %d 条", len(got))
	}
}

// TestNoteFTSWritePaths 验证删掉触发器后，每条写入路径都靠主动重建把索引同步好，
// 且 UPDATE 不残留过期 gram（这是 trigram 触发器方案踩过的坑）。
func TestNoteFTSWritePaths(t *testing.T) {
	d, err := Open(makeLegacyNotesDB(t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()

	// 新增：新内容立即可搜，行数对齐。
	if _, err := d.CreateNoteDoc("f1", "新建笔记", "另一段关于数据库索引的排查正文", "markdown"); err != nil {
		t.Fatalf("CreateNoteDoc: %v", err)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "另一段关于"); got != 1 {
		t.Errorf("新增后 MATCH 新内容 = %d（应为 1）", got)
	}

	// 改正文：旧词必须从索引消失（无过期 gram），新词可搜。
	if err := d.UpdateNoteDoc("n1", "改成了完全不同的一段正文，讲 triglyceride 与 quicksort", `["db"]`); err != nil {
		t.Fatalf("UpdateNoteDoc: %v", err)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "查询优化"); got != 0 {
		t.Errorf("过期 gram：改正文后仍搜到旧词『查询优化』= %d（应为 0）", got)
	}
	if got := noteFTSHits(t, d, "triglyceride"); got != 1 {
		t.Errorf("改后 MATCH 新词 'triglyceride' = %d（应为 1）", got)
	}

	// 改名：新名可搜。
	if err := d.RenameNoteNode("n1", "全新的标题麒麟"); err != nil {
		t.Fatalf("RenameNoteNode: %v", err)
	}
	if got := noteFTSHits(t, d, "全新的标题"); got != 1 {
		t.Errorf("改名后 MATCH 新标题 = %d（应为 1）", got)
	}

	// 改标签：新标签可搜，旧标签消失。
	if err := d.UpdateNoteTags("n1", `["麒麟标签"]`); err != nil {
		t.Fatalf("UpdateNoteTags: %v", err)
	}
	if got := noteFTSHits(t, d, "麒麟标签"); got != 1 {
		t.Errorf("改标签后 MATCH = %d（应为 1）", got)
	}

	// 删除：被删笔记内容从索引消失，行数对齐。
	if err := d.DeleteNoteNode("n2"); err != nil {
		t.Fatalf("DeleteNoteNode: %v", err)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "changelog"); got != 0 {
		t.Errorf("删除后仍搜到 'changelog' = %d（应为 0）", got)
	}

	assertIntegrity(t, d)
}

// TestNoteFTSBatchAndRestore 覆盖绕过公开方法的批量 / 恢复路径。
func TestNoteFTSBatchAndRestore(t *testing.T) {
	d, err := Open(makeLegacyNotesDB(t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()

	// 批量导入：整批只重建一次，结果仍正确（O(N) 而非 O(N²)）。
	payload := `[
		{"path":"批量/甲.md","content":"批量导入第一篇关于分布式事务的笔记","format":"markdown","tags":"[\"批量\"]"},
		{"path":"批量/乙.md","content":"批量导入第二篇消息队列幂等笔记","format":"markdown","tags":"[]"}
	]`
	n, err := d.ImportNoteBatch(payload, "", "")
	if err != nil {
		t.Fatalf("ImportNoteBatch: %v", err)
	}
	if n != 2 {
		t.Errorf("ImportNoteBatch created = %d（应为 2）", n)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "分布式事务"); got != 1 {
		t.Errorf("批量导入后 MATCH '分布式事务' = %d（应为 1）", got)
	}

	// 墓碑恢复：DeleteNoteNode 写墓碑，RestoreTombstone 用 BulkInsert 覆盖回 notes。
	if err := d.DeleteNoteNode("n1"); err != nil {
		t.Fatalf("DeleteNoteNode(n1): %v", err)
	}
	if got := noteFTSHits(t, d, "triglyceride"); got != 0 { // n1 此时还是旧正文
		t.Errorf("删除 n1 后仍搜到 = %d（应为 0）", got)
	}
	if err := d.RestoreTombstone("notes", "n1"); err != nil {
		t.Fatalf("RestoreTombstone: %v", err)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "查询优化"); got != 1 {
		t.Errorf("墓碑恢复后 MATCH 原正文『查询优化』= %d（应为 1）", got)
	}

	// 快照恢复：导出 → 新建一条 → 用旧 JSON 覆盖，索引必须跟着回到导出时状态。
	jsonStr, err := d.ExportFullDataAsJSON()
	if err != nil {
		t.Fatalf("ExportFullDataAsJSON: %v", err)
	}
	if _, err := d.CreateNoteDoc("", "恢复前临时笔记", "快照恢复专用临时标记麒麟龟", "markdown"); err != nil {
		t.Fatalf("CreateNoteDoc(temp): %v", err)
	}
	if got := noteFTSHits(t, d, "快照恢复专用"); got != 1 {
		t.Errorf("临时笔记未进索引 = %d（应为 1）", got)
	}
	if err := d.RestoreFromJSON(jsonStr); err != nil {
		t.Fatalf("RestoreFromJSON: %v", err)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "快照恢复专用"); got != 0 {
		t.Errorf("快照恢复后临时笔记仍在索引 = %d（应为 0）", got)
	}

	assertIntegrity(t, d)
}

// TestNoteFTSLegacyStructureUpgrade 复现用户真实场景：存量库里 notes_fts 是
// external content + trigram，且残留会写坏虚表的旧触发器。Open 必须清理旧触发器、
// 把索引结构升级为普通内容表、并让被 267 卡住的迁移正常完成。
func TestNoteFTSLegacyStructureUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old_external.db")
	raw, err := sql.Open("sqlite", reproDSN(path))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	// 用当前完整表结构，只把 notes_fts 换成旧式 external content 表 + 旧触发器，
	// 模拟上一版本留下的库。
	stmts := []string{
		`CREATE TABLE notes (
			id TEXT PRIMARY KEY, keyword TEXT NOT NULL UNIQUE, content TEXT NOT NULL,
			category TEXT DEFAULT '', name TEXT DEFAULT '', parent_id TEXT DEFAULT '',
			is_folder INTEGER DEFAULT 0, sort INTEGER DEFAULT 0, tags TEXT DEFAULT '',
			is_note INTEGER DEFAULT 0, format TEXT DEFAULT 'markdown',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO notes(id,keyword,content,name,tags,created_at,updated_at)
		 VALUES('n1','k1','外部内容表的旧正文，讲数据库索引','旧标题','[]','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`CREATE VIRTUAL TABLE notes_fts USING fts5(name, content, tags,
			content='notes', content_rowid='rowid', tokenize='trigram')`,
		`CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN
			INSERT INTO notes_fts(rowid, name, content, tags) VALUES(new.rowid,new.name,new.content,new.tags);
		 END`,
		`CREATE TRIGGER notes_ad AFTER DELETE ON notes BEGIN
			INSERT INTO notes_fts(notes_fts,rowid,name,content,tags) VALUES('delete',old.rowid,old.name,old.content,old.tags);
		 END`,
		`CREATE TRIGGER notes_au AFTER UPDATE ON notes BEGIN
			INSERT INTO notes_fts(notes_fts,rowid,name,content,tags) VALUES('delete',old.rowid,old.name,old.content,old.tags);
			INSERT INTO notes_fts(rowid,name,content,tags) VALUES(new.rowid,new.name,new.content,new.tags);
		 END`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("造旧库: %v\nSQL: %s", err, s)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	// Open 必须不报 267，且迁移正常完成。
	d, err := Open(path)
	if err != nil {
		t.Fatalf("db.Open 修复后仍失败: %v", err)
	}
	defer d.Close()

	assertIntegrity(t, d)
	// 旧触发器必须被清掉。
	var trigs int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'notes_%'`).Scan(&trigs); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if trigs != 0 {
		t.Errorf("残留旧 notes 触发器 %d 个（应为 0）", trigs)
	}
	// 索引 DDL 必须是新的普通内容表（不含 content=）。
	var ddl string
	if err := d.conn.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='notes_fts'`).Scan(&ddl); err != nil {
		t.Fatalf("read fts ddl: %v", err)
	}
	if regexp.MustCompile(`(?i)content\s*=`).MatchString(ddl) {
		t.Errorf("notes_fts 仍是 external content 表: %s", ddl)
	}
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "数据库索引"); got != 1 {
		t.Errorf("结构升级后 MATCH '数据库索引' = %d（应为 1）", got)
	}
	// 之后再正常写入，也不能重新把表写坏。
	if err := d.UpdateNoteContent("n1", "升级后写入的新正文，讲消息队列"); err != nil {
		t.Fatalf("UpdateNoteContent: %v", err)
	}
	if got := noteFTSHits(t, d, "消息队列"); got != 1 {
		t.Errorf("升级后写入未进索引 = %d（应为 1）", got)
	}
	assertIntegrity(t, d)
}

// TestNoteFTSRebuildRepairsBrokenIndex 直接验证 rebuildNoteIndex 的自愈分支：
// 索引表被 DROP（模拟损坏到不可用的极端情况）后，DELETE 会失败，
// 必须走 DROP + 按 notesFtsDDL 重建 + 灌入，而不是把错误抛给用户。
func TestNoteFTSRebuildRepairsBrokenIndex(t *testing.T) {
	d, err := Open(makeLegacyNotesDB(t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()

	// 整张索引表丢掉，模拟「虚拟表损坏/影子表缺失」导致 DELETE 报错的场景。
	if _, err := d.conn.Exec(`DROP TABLE notes_fts`); err != nil {
		t.Fatalf("drop notes_fts: %v", err)
	}
	if err := d.rebuildNoteIndexWithLock(); err != nil {
		t.Fatalf("rebuildNoteIndex 自愈失败: %v", err)
	}

	assertIntegrity(t, d)
	noteFTSCounts(t, d)
	if got := noteFTSHits(t, d, "数据库"); got != 2 {
		t.Errorf("自愈重建后 MATCH '数据库' = %d（应为 2）", got)
	}
}
