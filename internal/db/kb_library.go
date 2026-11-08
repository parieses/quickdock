package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 知识库分组（多库）的持久层。
//
// 与 kb.go 同样的铁律：持锁期间不许再调 d.Query / d.Transaction（RWMutex 不可重入）。
// 需要跨表动的时候一律走 d.Transaction 里手写 tx。

// KBLibraryDefaultID 默认知识库的固定 id。
//
// 固定而不是随机：升级迁移要把存量源归进这个库（schema.go 的数据迁移里写死），
// 随机 id 会让「同一台机器升级两次得到两个默认库」成为可能。
const KBLibraryDefaultID = "kb-default"

// ErrLastKBLibrary 最后一个知识库不能删。
//
// 库里至少要有一个分组，否则「导入文档」没有落点，前端得额外处理一种空状态，
// 而那种空状态本身没有意义。
var ErrLastKBLibrary = errors.New("至少保留一个知识库")

// KBLibrary 知识库分组。
//
// Name 为空串是合法状态：默认库刻意不存名字，由前端按当前语言显示，
// 避免中文文案顺着数据库流到英文界面里。
type KBLibrary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Sort        int    `json:"sort"`
	SourceCount int    `json:"sourceCount"`
	ChunkCount  int    `json:"chunkCount"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// ListKBLibraries 列出全部分组，带文档数与切片数。
//
// 用 LEFT JOIN + COUNT(s.id) 一次查完：分组通常只有几个，
// 但每个分组都单独 COUNT 一次就是 N+1 查询，而列表是每次打开页面都要读的。
func (d *Database) ListKBLibraries() ([]KBLibrary, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(`
		SELECT l.id, l.name, l.icon, l.sort, l.created_at, l.updated_at,
		       COUNT(s.id), COALESCE(SUM(s.chunk_count), 0)
		FROM kb_libraries l
		LEFT JOIN kb_sources s ON s.library_id = l.id
		GROUP BY l.id
		ORDER BY l.sort ASC, l.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KBLibrary{}
	for rows.Next() {
		var l KBLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.Icon, &l.Sort, &l.CreatedAt, &l.UpdatedAt,
			&l.SourceCount, &l.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetKBLibrary 按 id 取分组。不存在返回 (nil, nil)。
func (d *Database) GetKBLibrary(id string) (*KBLibrary, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var l KBLibrary
	err := d.conn.QueryRow(`SELECT id, name, icon, sort, created_at, updated_at
		FROM kb_libraries WHERE id = ?`, id).
		Scan(&l.ID, &l.Name, &l.Icon, &l.Sort, &l.CreatedAt, &l.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// FirstKBLibraryID 取排序第一的分组 id。
//
// 用途：前端停在「全部」视图时点「添加文件」，得有个默认落点。
// 返回空串表示一个分组都没有（正常情况下迁移已经建好默认库）。
func (d *Database) FirstKBLibraryID() (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var id string
	err := d.conn.QueryRow(`SELECT id FROM kb_libraries ORDER BY sort ASC, created_at ASC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// CreateKBLibrary 新建分组，追加到末尾。
func (d *Database) CreateKBLibrary(name, icon string) (*KBLibrary, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("知识库名称不能为空")
	}
	id := newID()
	ts := now()
	d.mu.Lock()
	defer d.mu.Unlock()
	var sort int
	if err := d.conn.QueryRow(`SELECT COALESCE(MAX(sort), -1) + 1 FROM kb_libraries`).Scan(&sort); err != nil {
		return nil, err
	}
	if _, err := d.conn.Exec(`INSERT INTO kb_libraries (id, name, icon, sort, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, name, icon, sort, ts, ts); err != nil {
		return nil, err
	}
	return &KBLibrary{ID: id, Name: name, Icon: icon, Sort: sort, CreatedAt: ts, UpdatedAt: ts}, nil
}

// RenameKBLibrary 改名 / 换图标。name 传空表示「恢复成默认名」（只有默认库该这么用）。
func (d *Database) RenameKBLibrary(id, name, icon string) error {
	name = strings.TrimSpace(name)
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE kb_libraries SET name = ?, icon = ?, updated_at = ? WHERE id = ?`,
		name, icon, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("知识库不存在")
	}
	return nil
}

// DeleteKBLibrary 删除分组，连同它下面的全部源与切片。
//
// 磁盘上的原文件一律不动——知识库对文件只有引用关系，删库不等于删用户的文档。
// 所以前端确认框必须写明这一点，否则没人敢点。
func (d *Database) DeleteKBLibrary(id string) error {
	return d.Transaction(func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM kb_libraries`).Scan(&total); err != nil {
			return err
		}
		if total <= 1 {
			return ErrLastKBLibrary
		}
		rows, err := tx.Query(`SELECT id FROM kb_sources WHERE library_id = ?`, id)
		if err != nil {
			return err
		}
		var sourceIDs []string
		for rows.Next() {
			var sid string
			if err := rows.Scan(&sid); err != nil {
				rows.Close()
				return err
			}
			sourceIDs = append(sourceIDs, sid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, sid := range sourceIDs {
			if err := deleteKBSourceTx(tx, sid); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM kb_libraries WHERE id = ?`, id)
		return err
	})
}

// MoveKBSource 把一份文档挪到另一个分组。
//
// 只改归属、不重算索引：切片与向量跟源走，与库无关。
// 目标库不存在时报错而不是静默写入一个悬空 library_id——
// 那种行在列表里谁都看不见，用户只会觉得「文档消失了」。
func (d *Database) MoveKBSource(sourceID, libraryID string) error {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(libraryID) == "" {
		return fmt.Errorf("缺少文档或知识库 id")
	}
	lib, err := d.GetKBLibrary(libraryID)
	if err != nil {
		return err
	}
	if lib == nil {
		return fmt.Errorf("目标知识库不存在")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(`UPDATE kb_sources SET library_id = ?, updated_at = ? WHERE id = ?`,
		libraryID, now(), sourceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("文档不存在")
	}
	return nil
}
