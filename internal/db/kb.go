package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// 知识库（kb_*）的持久层。
//
// 这里只做数据存取，不含解析、切片、向量计算——那些在 internal/kb。
// 检索的「两路召回 + RRF 融合」也不在这里：融合需要读向量矩阵并持有缓存，
// 属于应用层决策，放在 services/kb。这里只提供两路各自的原始召回。
//
// 一条铁律：任何方法都不许在持锁期间调 d.Query / d.QueryOne / d.Transaction
// （它们内部各自加锁，RWMutex 不可重入，二次加锁必死锁）。所以下面全是
// 自己 d.mu.RLock + d.conn.Query 或直接 d.Transaction。

// KBSource 知识库源文件。
type KBSource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	LibraryID  string `json:"libraryId"`
	Path       string `json:"path"`
	Ext        string `json:"ext"`
	Size       int64  `json:"size"`
	Mtime      int64  `json:"mtime"`
	Hash       string `json:"hash"`
	Status     string `json:"status"`
	ChunkCount int    `json:"chunkCount"`
	PageCount  int    `json:"pageCount"`
	Err        string `json:"err"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// KBChunk 知识库切片。Vec 不导出 JSON：向量矩阵动辄几十 MB，
// 前端拿到也没用，需要时由检索接口单独返回命中项。
type KBChunk struct {
	ID         string `json:"id"`
	SourceID   string `json:"sourceId"`
	Idx        int    `json:"idx"`
	Heading    string `json:"heading"`
	Text       string `json:"text"`
	CharCount  int    `json:"charCount"`
	Page       int    `json:"page"`
	Dim        int    `json:"dim"`
	EmbedModel string `json:"embedModel"`
	HasVec     bool   `json:"hasVec"`
}

// KBHit 一路召回的原始命中（还没做 RRF；分数只是该路自己的名次依据）。
type KBHit struct {
	RowID      int64  `json:"-"`
	ChunkID    string `json:"chunkId"`
	SourceID   string `json:"sourceId"`
	SourceName string `json:"sourceName"`
	SourcePath string `json:"sourcePath"`
	LibraryID  string `json:"libraryId"`
	Ext        string `json:"ext"`
	Heading    string `json:"heading"`
	Text       string `json:"text"`
	Page       int    `json:"page"`
	Idx        int    `json:"idx"`
}

// 知识库源状态。刻意把「向量未建立」排除在失败之外（见 kb_sources 注释）。
const (
	KBSourcePending   = "pending"
	KBSourceParsing   = "parsing"
	KBSourceChunking  = "chunking"
	KBSourceEmbedding = "embedding"
	KBSourceReady     = "ready"
	KBSourceFailed    = "failed"
	KBSourceMissing   = "missing"
	KBSourceStale     = "stale"
)

const kbSourceCols = `id, name, library_id, path, ext, size, mtime, hash, status, chunk_count, page_count, err, created_at, updated_at`

func scanKBSource(rows interface {
	Scan(dest ...interface{}) error
}) (*KBSource, error) {
	var s KBSource
	err := rows.Scan(&s.ID, &s.Name, &s.LibraryID, &s.Path, &s.Ext, &s.Size, &s.Mtime, &s.Hash, &s.Status,
		&s.ChunkCount, &s.PageCount, &s.Err, &s.CreatedAt, &s.UpdatedAt)
	return &s, err
}

// UpsertKBSource 按 path 登记源文件。
//
// 冲突时只更新「文件指纹 + 名称 + 扩展名」，保留 status / chunk_count / created_at：
// 重复添加同一个文件时不该把已经建好的索引状态清成 pending，
// 要不要重建由调用方按 hash 比对后显式决定（见 services/kb 的扫描逻辑）。
//
// library_id 特殊处理：入参为空时**保留库里已有的归属**，不做覆盖。
// 否则「在全部视图里点重新导入」这类不带库 id 的调用会把文档悄悄挪回默认库。
func (d *Database) UpsertKBSource(s KBSource) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("源文件路径不能为空")
	}
	if s.ID == "" {
		s.ID = newID()
	}
	if s.Status == "" {
		s.Status = KBSourcePending
	}
	ts := now()
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`
		INSERT INTO kb_sources (id, name, library_id, path, ext, size, mtime, hash, status, chunk_count, page_count, err, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, '', ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			name = excluded.name,
			library_id = CASE WHEN excluded.library_id <> '' THEN excluded.library_id ELSE kb_sources.library_id END,
			ext = excluded.ext,
			size = excluded.size,
			mtime = excluded.mtime,
			hash = excluded.hash,
			updated_at = excluded.updated_at`,
		s.ID, s.Name, s.LibraryID, s.Path, s.Ext, s.Size, s.Mtime, s.Hash, s.Status, ts, ts)
	return err
}

// GetKBSource 按 id 取源。
func (d *Database) GetKBSource(id string) (*KBSource, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.conn.QueryRow(`SELECT `+kbSourceCols+` FROM kb_sources WHERE id = ?`, id)
	s, err := scanKBSource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// GetKBSourceByPath 按路径取源（扫描时判「这个文件是否已登记」）。
func (d *Database) GetKBSourceByPath(path string) (*KBSource, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.conn.QueryRow(`SELECT `+kbSourceCols+` FROM kb_sources WHERE path = ?`, path)
	s, err := scanKBSource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// ListKBSources 列出源文件。libraryID 为空 = 跨库列出全部。
// 就绪的排前面，同状态按名称。
func (d *Database) ListKBSources(libraryID string) ([]KBSource, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	query := `SELECT ` + kbSourceCols + ` FROM kb_sources`
	args := []interface{}{}
	if libraryID != "" {
		query += ` WHERE library_id = ?`
		args = append(args, libraryID)
	}
	query += ` ORDER BY CASE status WHEN 'ready' THEN 0 WHEN 'embedding' THEN 1
		WHEN 'pending' THEN 2 ELSE 3 END, name ASC`
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBSource{}
	for rows.Next() {
		s, err := scanKBSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// DeleteKBSource 删除源及其全部切片与索引。
//
// 必须三步在同一事务里：先取 rowid → 删 FTS → 删 chunks → 删源。
// 少删 FTS 会留下孤儿索引，检索命中后 JOIN 不到 chunk，结果里出现空条目
// （页面表现为「有一条结果点不开」，日志里什么都没有）。
func (d *Database) DeleteKBSource(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		return deleteKBSourceTx(tx, id)
	})
}

// DeleteKBSourceByPath 按路径删除（导入时发现文件已不存在，清掉登记）。
func (d *Database) DeleteKBSourceByPath(path string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRow(`SELECT id FROM kb_sources WHERE path = ?`, path).Scan(&id); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		return deleteKBSourceTx(tx, id)
	})
}

func deleteKBSourceTx(tx *sql.Tx, id string) error {
	rows, err := tx.Query(`SELECT rowid FROM kb_chunks WHERE source_id = ?`, id)
	if err != nil {
		return err
	}
	var rowids []int64
	for rows.Next() {
		var rid int64
		if err := rows.Scan(&rid); err != nil {
			rows.Close()
			return err
		}
		rowids = append(rowids, rid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, rid := range rowids {
		if _, err := tx.Exec(`DELETE FROM kb_chunks_fts WHERE rowid = ?`, rid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM kb_chunks WHERE source_id = ?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM kb_sources WHERE id = ?`, id)
	return err
}

// UpdateKBSourceStatus 状态迁移。errMsg 只对 failed 有意义，其余状态自动清空。
func (d *Database) UpdateKBSourceStatus(id, status, errMsg string) error {
	if status != KBSourceFailed && status != KBSourceMissing {
		errMsg = ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE kb_sources SET status = ?, err = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, now(), id)
	return err
}

// UpdateKBSourceMeta 更新文件指纹与块数（索引成功后回写）。
func (d *Database) UpdateKBSourceMeta(id, hash string, size, mtime int64, chunkCount, pageCount int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`UPDATE kb_sources
		SET hash = ?, size = ?, mtime = ?, chunk_count = ?, page_count = ?, updated_at = ?
		WHERE id = ?`, hash, size, mtime, chunkCount, pageCount, now(), id)
	return err
}

// ReplaceChunks 整份替换某个源的切片（含 FTS）。
//
// 这是「源变了就重建」的唯一写入口。不做增量补块：块边界是切分算法的产物，
// 文件改一个字就可能导致后续所有块错位，逐个 diff 既复杂又会残留旧块
// （检索时命中一段原文里已经删掉的话，比检索不到更伤人）。
func (d *Database) ReplaceChunks(sourceID string, chunks []KBChunk) error {
	return d.Transaction(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT rowid FROM kb_chunks WHERE source_id = ?`, sourceID)
		if err != nil {
			return err
		}
		var oldRowIDs []int64
		for rows.Next() {
			var rid int64
			if err := rows.Scan(&rid); err != nil {
				rows.Close()
				return err
			}
			oldRowIDs = append(oldRowIDs, rid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, rid := range oldRowIDs {
			if _, err := tx.Exec(`DELETE FROM kb_chunks_fts WHERE rowid = ?`, rid); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM kb_chunks WHERE source_id = ?`, sourceID); err != nil {
			return err
		}

		stmtChunk, err := tx.Prepare(`INSERT INTO kb_chunks
			(id, source_id, idx, heading, text, char_count, page)
			VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmtChunk.Close()
		stmtFTS, err := tx.Prepare(`INSERT INTO kb_chunks_fts (rowid, text, heading) VALUES (?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmtFTS.Close()

		for _, c := range chunks {
			name := c.ID
			if name == "" {
				name = newID()
			}
			res, err := stmtChunk.Exec(name, sourceID, c.Idx, c.Heading, c.Text, c.CharCount, c.Page)
			if err != nil {
				return err
			}
			rid, err := res.LastInsertId()
			if err != nil {
				return err
			}
			if _, err := stmtFTS.Exec(rid, c.Text, c.Heading); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListChunksBySource 列出某个源的全部切片（预览用）。
//
// 不查 vec 列：预览只需要正文，而向量列一行就是几 KB，
// 一次预览几百块会多读好几 MB 却一个字节都用不上。
func (d *Database) ListChunksBySource(sourceID string) ([]KBChunk, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT id, source_id, idx, heading, text, char_count, page,
		dim, embed_model, (vec IS NOT NULL)
		FROM kb_chunks WHERE source_id = ? ORDER BY idx ASC`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBChunk{}
	for rows.Next() {
		var c KBChunk
		var hasVec int
		if err := rows.Scan(&c.ID, &c.SourceID, &c.Idx, &c.Heading, &c.Text, &c.CharCount, &c.Page,
			&c.Dim, &c.EmbedModel, &hasVec); err != nil {
			return nil, err
		}
		c.HasVec = hasVec != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// KBPendingVecChunks 取某个源里还没算向量的切片 id 与正文（断点续做的游标）。
//
// 不单独存进度表：vec IS NULL 本身就是进度，而且它不会撒谎——
// 进度表会因为崩溃停在错误的位置，数据行不会。
func (d *Database) KBPendingVecChunks(sourceID, model string) ([]KBChunk, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT id, source_id, idx, heading, text, page
		FROM kb_chunks
		WHERE source_id = ? AND (vec IS NULL OR embed_model <> ?)
		ORDER BY idx ASC`, sourceID, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBChunk{}
	for rows.Next() {
		var c KBChunk
		if err := rows.Scan(&c.ID, &c.SourceID, &c.Idx, &c.Heading, &c.Text, &c.Page); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// KBVecUpdate 一批切片算出来的向量。
type KBVecUpdate struct {
	ChunkID string
	Vec     []byte
	Dim     int
}

// UpdateChunkVecs 回填一批向量。
//
// 每批一个独立短事务、绝不包住网络调用：Database.Transaction 全程持有
// 全库写锁，一个批次外的 embedding 请求（几百毫秒到几十秒）会把剪贴板监听、
// UI 轮询查询、定时任务一起堵死——表现是「导入知识库时整个应用卡住」。
func (d *Database) UpdateChunkVecs(model string, updates []KBVecUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return d.Transaction(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`UPDATE kb_chunks SET vec = ?, dim = ?, embed_model = ? WHERE id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range updates {
			if _, err := stmt.Exec(u.Vec, u.Dim, model, u.ChunkID); err != nil {
				return err
			}
		}
		return nil
	})
}

// KBVectorRow 全量向量矩阵的一行。
type KBVectorRow struct {
	RowID     int64
	ChunkID   string
	Vec       []byte
	Dim       int
	LibraryID string
}

// AllVectors 读出全部已建立向量的切片（只读 rowid/chunk_id/vec/dim/库归属五列）。
//
// 调用方必须自己缓存：1 万块 × 1024 维 = 40MB，每次检索都全量读盘
// 会让「点一下搜索」变成几百毫秒的磁盘 IO。services/kb 里有一份
// 按版本号失效的矩阵缓存（见 KBVecVersion）。
//
// 顺带带出 library_id：向量检索要支持「只搜某个知识库」，
// 而矩阵里只有 rowid，没有库归属就没法在扫描时过滤——
// 事后过滤等于把 TopK 名额浪费在别的库上，缩小范围后结果会明显变差。
func (d *Database) AllVectors() ([]KBVectorRow, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`SELECT c.rowid, c.id, c.vec, c.dim, s.library_id
		FROM kb_chunks c
		JOIN kb_sources s ON s.id = c.source_id
		WHERE c.vec IS NOT NULL AND c.dim > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBVectorRow{}
	for rows.Next() {
		var r KBVectorRow
		if err := rows.Scan(&r.RowID, &r.ChunkID, &r.Vec, &r.Dim, &r.LibraryID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// KBVecStats 向量覆盖率（前端提示「N 个片段尚未建立语义索引」）。
func (d *Database) KBVecStats() (total, withVec int, err error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.conn.QueryRow(`SELECT COUNT(*), COUNT(vec) FROM kb_chunks`)
	err = row.Scan(&total, &withVec)
	return
}

// SearchChunksFTS 关键词路召回。
//
// trigram 分词器下必须用短语查询（整串加双引号）：散词 OR 会把
// 「数据库备份」拆成任意组合，「数据库」三个字单独命中就排在前面，
// 精度比短语差一大截。双引号本身要先转义成两个双引号。
//
// bm25 的第二列是 heading 的权重：标题路径（"部署/数据库"）命中
// 比正文某处顺带提到一次更能说明这一块讲的就是这件事。
//
// libraryID 非空时只在该库内召回。过滤放在 SQL 里而不是事后筛：
// LIMIT 是在 WHERE 之后生效的，事后筛会让「TopK 里全是别的库」变成空结果。
func (d *Database) SearchChunksFTS(query string, limit int, libraryID string) ([]KBHit, error) {
	q := kbFTSEscape(query)
	if q == "" || limit <= 0 {
		return nil, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	hits, err := d.ftsQuery(q, limit, libraryID)
	// AND 全不中 → 降级 OR：用户给的关键词只要有一个出现在文档里就值得
	// 返回（bm25 自动把命中多的排前面）。典型场景是问答把口语化提问拆词
	// 后个别词在原文里没有逐字对应——整查询全空比给出弱相关结果更糟。
	if err == nil && len(hits) == 0 && strings.Contains(q, " AND ") {
		hits, err = d.ftsQuery(strings.ReplaceAll(q, " AND ", " OR "), limit, libraryID)
	}
	return hits, err
}

// ftsQuery 执行一次 FTS 匹配。这里刻意不给 kb_chunks_fts 起别名：
// bm25() 的参数必须是 FTS 表在查询里的实际表名，写成 bm25(f, ...) 会报
// "no such column: f"（实测）。于是 MATCH 与 bm25 都直接用真表名，主表用 c 区分列名。
func (d *Database) ftsQuery(match string, limit int, libraryID string) ([]KBHit, error) {
	sqlStr := `
		SELECT c.rowid, c.id, c.source_id, s.name, s.path, s.library_id, s.ext,
		       c.heading, c.text, c.page, c.idx
		FROM kb_chunks_fts
		JOIN kb_chunks c ON c.rowid = kb_chunks_fts.rowid
		JOIN kb_sources s ON s.id = c.source_id
		WHERE kb_chunks_fts MATCH ?`
	args := []interface{}{match}
	if libraryID != "" {
		sqlStr += ` AND s.library_id = ?`
		args = append(args, libraryID)
	}
	sqlStr += ` ORDER BY bm25(kb_chunks_fts, 1.0, 2.0) LIMIT ?`
	args = append(args, limit)

	rows, err := d.conn.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBHit{}
	for rows.Next() {
		var h KBHit
		if err := rows.Scan(&h.RowID, &h.ChunkID, &h.SourceID, &h.SourceName, &h.SourcePath,
			&h.LibraryID, &h.Ext, &h.Heading, &h.Text, &h.Page, &h.Idx); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ChunksByRowIDs 按 rowid 批量取命中详情（向量路召回后回表）。
func (d *Database) ChunksByRowIDs(rowIDs []int64) ([]KBHit, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}
	ph := make([]string, len(rowIDs))
	args := make([]interface{}, len(rowIDs))
	for i, id := range rowIDs {
		ph[i] = "?"
		args[i] = id
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`
		SELECT c.rowid, c.id, c.source_id, s.name, s.path, s.library_id, s.ext,
		       c.heading, c.text, c.page, c.idx
		FROM kb_chunks c
		JOIN kb_sources s ON s.id = c.source_id
		WHERE c.rowid IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byRow := make(map[int64]KBHit, len(rowIDs))
	for rows.Next() {
		var h KBHit
		if err := rows.Scan(&h.RowID, &h.ChunkID, &h.SourceID, &h.SourceName, &h.SourcePath,
			&h.LibraryID, &h.Ext, &h.Heading, &h.Text, &h.Page, &h.Idx); err != nil {
			return nil, err
		}
		byRow[h.RowID] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 按传入顺序还原：向量路的名次就是相似度名次，乱了 RRF 就失真。
	out := make([]KBHit, 0, len(rowIDs))
	for _, id := range rowIDs {
		if h, ok := byRow[id]; ok {
			out = append(out, h)
		}
	}
	return out, nil
}

// kbFTSEscape 把用户输入变成 trigram 下可用的查询。
//
// 规则：按空白拆词 → 每个词再按「CJK 段 / 非 CJK 段」切开 →
// 短段（≤6 字的中文、普通英文词）整段作短语，长中文段切成 3-gram 取 OR，
// 不足 3 字的段（trigram 根本索引不到）直接丢弃，各部分之间 AND。
//
// 为什么长中文段不能整段作短语：短语匹配要求**在原文里逐字连续出现**。
// 「任务指标」是检索词，原文里有；但问答把「任务指标如何考核」整个
// 问题传进来时，这句话不可能在文档里逐字出现，实测必然 0 命中（11-11
// 事故）。切成 3-gram 取 OR，靠 bm25 把命中 gram 最多的块排前面，
// 召回和排序都够用。
//
// 为什么只处理双引号：FTS5 的查询语法里，**双引号内的一切都是字面量**，
// 所以 * ^ : ( ) - + 这些在引号里根本不需要转义。
//
// 总 rune 数不足 3 返回空：trigram 的索引单位就是 3-gram，更短的查询
// 不会命中任何东西，前端会提示「至少 3 个字」。
func kbFTSEscape(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	fields := strings.Fields(q)
	if len([]rune(strings.Join(fields, ""))) < 3 {
		return ""
	}
	var parts []string
	for _, f := range fields {
		parts = append(parts, kbFTSSegment(f)...)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

// kbFTSSegment 把一个词切成可检索的 FTS5 条件，见 kbFTSEscape 注释。
//
// 词按 CJK/非 CJK 切段，段内再处理；但如果所有段都因不足 3 字被丢弃
// （「60分」→"60"+"分"），整词作为单个短语兜底——中文文档里混合词
// 太常见（80分、70%、第3页），不能因为切分就把整词弄丢。
func kbFTSSegment(word string) []string {
	runes := []rune(word)
	var out []string
	i := 0
	for i < len(runes) {
		j := i
		cjk := kbIsCJK(runes[i])
		for j < len(runes) && kbIsCJK(runes[j]) == cjk {
			j++
		}
		seg := runes[i:j]
		i = j
		if len(seg) < 3 {
			continue // 3-gram 索引不到，留着只会把 AND 拖死
		}
		if !cjk {
			// 英文/数字段：原文里怎么写就怎么搜，短语保精度。
			lit := strings.ReplaceAll(string(seg), `"`, `""`)
			out = append(out, `"`+lit+`"`)
			continue
		}
		// 中文段：3-gram 滑窗取 OR。原文包含完整词时所有 gram 同时命中，
		// bm25 自然把它排最前——不需要再叠一个整段短语保精度。
		var grams []string
		for k := 0; k+3 <= len(seg); k++ {
			grams = append(grams, `"`+string(seg[k:k+3])+`"`)
		}
		if len(grams) == 1 {
			out = append(out, grams[0])
		} else if len(grams) > 1 {
			out = append(out, "("+strings.Join(grams, " OR ")+")")
		}
	}
	if len(out) == 0 && len(runes) >= 3 {
		// 整词兜底：混合短词（「60分」「70%」）切段后全被丢弃，但整词
		// 本身就是 3 个以上的连续字符，trigram 索引得到，直接短语搜。
		lit := strings.ReplaceAll(word, `"`, `""`)
		out = append(out, `"`+lit+`"`)
	}
	return out
}

// kbIsCJK 判断 rune 是否属于中日韩文字（含假名），与 internal/kb 的
// 判定口径一致；db 层不引 kb 包，这里单独留一份最小实现。
func kbIsCJK(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF || r >= 0xF900 && r <= 0xFAFF
}

// KBStats 库容量与状态汇总（页面顶部展示）。
type KBStats struct {
	Sources    int `json:"sources"`
	Ready      int `json:"ready"`
	Failed     int `json:"failed"`
	Missing    int `json:"missing"`
	Stale      int `json:"stale"`
	Chunks     int `json:"chunks"`
	VecChunks  int `json:"vecChunks"`
	TotalChars int `json:"totalChars"`
}

// GetKBStats 统计。用一次聚合查完，不按状态逐个 COUNT。
func (d *Database) GetKBStats() (KBStats, error) {
	var st KBStats
	d.mu.RLock()
	defer d.mu.RUnlock()
	row := d.conn.QueryRow(`SELECT
		COUNT(*),
		SUM(CASE WHEN status='ready' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='missing' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status='stale' THEN 1 ELSE 0 END)
		FROM kb_sources`)
	var ready, failed, missing, stale sql.NullInt64
	if err := row.Scan(&st.Sources, &ready, &failed, &missing, &stale); err != nil {
		return st, err
	}
	st.Ready, st.Failed, st.Missing, st.Stale = int(ready.Int64), int(failed.Int64), int(missing.Int64), int(stale.Int64)

	row2 := d.conn.QueryRow(`SELECT COUNT(*), COUNT(vec), COALESCE(SUM(char_count), 0) FROM kb_chunks`)
	var withVec sql.NullInt64
	var chars sql.NullInt64
	if err := row2.Scan(&st.Chunks, &withVec, &chars); err != nil {
		return st, err
	}
	st.VecChunks = int(withVec.Int64)
	st.TotalChars = int(chars.Int64)
	return st, nil
}

// MarkKBSourcesStale 把使用旧 embedding 模型的源标成 stale。
//
// 换模型后旧向量与新向量不在同一空间，直接混用会让余弦相似度变成噪声——
// 而且是「结果看起来正常、只是不准」的那种坏，最难发现。所以宁可整体降级
// 成纯关键词检索，等用户显式点重建。
func (d *Database) MarkKBSourcesStale(model string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE kb_sources SET status = ?, updated_at = ?
		WHERE status = ? AND id IN (
			SELECT DISTINCT source_id FROM kb_chunks
			WHERE embed_model <> '' AND embed_model <> ?)`,
		KBSourceStale, now(), KBSourceReady, model)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
