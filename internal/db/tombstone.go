package db

// 删除墓碑（回收站）。
//
// 动机：项目/集合/场景/笔记此前是硬删——误删即不可恢复，而这些都是用户
// 日积月累录进去的数据。复用长期空置的 tombstones 表：删除前把整行
// （级联删除时把整棵子树）拍成 JSON 存进 payload，恢复时原样写回，
// id 保持不变——frecency/activity 里的引用因此不会失效。
//
// 安全边界：捕获失败 = 放弃本次删除。宁可让用户点一次「删除」没反应，
// 也不能静默删掉不可恢复的数据。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Tombstone 回收站条目（列表展示用，payload 不直接暴露给前端）。
type Tombstone struct {
	Collection string `json:"collection"` // 主体类型：workspaces | scenes | collections | items | notes
	ID         string `json:"id"`         // 被删主体 id
	Label      string `json:"label"`      // 展示名（主体 name，笔记回退 keyword）
	DeletedAt  string `json:"deletedAt"`
}

// tombstonePayload 恢复载荷：表名 → 行集合。
// 级联删除（集合/场景/笔记文件夹）时一个条目带多张表的多行，
// 恢复时连子树一起回来——只恢复容器不恢复内容会比误删更难看。
type tombstonePayload struct {
	Rows map[string][]map[string]interface{} `json:"rows"`
}

// rowQuerier 抽象 *sql.DB 与 *sql.Tx：捕获行既可能在锁外直接查库，
// 也可能在删除事务内用 tx 查（两种调用方的持锁状态不同，见各删除点）。
type rowQuerier interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// captureRows 查出一批行（SELECT *）。table 走白名单校验。
func captureRows(q rowQuerier, table, where string, args ...interface{}) ([]map[string]interface{}, error) {
	if err := validateTable(table); err != nil {
		return nil, err
	}
	rows, err := q.Query("SELECT * FROM "+table+" WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// rowLabel 取展示名：name 优先，笔记回退 keyword，再退回 id。
func rowLabel(row map[string]interface{}) string {
	if v, ok := row["name"].(string); ok && v != "" {
		return v
	}
	if v, ok := row["keyword"].(string); ok && v != "" {
		return v
	}
	return ""
}

// writeTombstone 写一条墓碑（调用方负责持锁；事务内传 tx，锁外经 AddTombstone）。
// 同 (collection, id) 重复删除时覆盖旧墓碑：恢复的永远是最新一次删除的快照。
func writeTombstone(q rowQuerier, collection, id, label string, rows map[string][]map[string]interface{}) error {
	payload, err := json.Marshal(tombstonePayload{Rows: rows})
	if err != nil {
		return fmt.Errorf("序列化墓碑失败: %w", err)
	}
	_, err = q.Exec(
		`INSERT INTO tombstones (collection, id, deleted_at, label, payload)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(collection, id) DO UPDATE SET
		   deleted_at = excluded.deleted_at, label = excluded.label, payload = excluded.payload`,
		collection, id, time.Now().Format(time.RFC3339), label, string(payload))
	return err
}

// AddTombstone 锁外入口：自持写锁查库写墓碑。
func (d *Database) AddTombstone(collection, id, label string, rows map[string][]map[string]interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return writeTombstone(d.conn, collection, id, label, rows)
}

// ListTombstones 回收站全部条目（新的在前）。
func (d *Database) ListTombstones() ([]Tombstone, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(
		`SELECT collection, id, label, deleted_at FROM tombstones ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tombstone
	for rows.Next() {
		var t Tombstone
		if err := rows.Scan(&t.Collection, &t.ID, &t.Label, &t.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTombstonePayload 读取某条墓碑的恢复载荷。
func (d *Database) GetTombstonePayload(collection, id string) (*tombstonePayload, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var payload string
	err := d.conn.QueryRow(
		`SELECT payload FROM tombstones WHERE collection = ? AND id = ?`, collection, id).Scan(&payload)
	if err != nil {
		return nil, err
	}
	var p tombstonePayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, fmt.Errorf("墓碑载荷损坏: %w", err)
	}
	if len(p.Rows) == 0 {
		return nil, fmt.Errorf("墓碑载荷为空")
	}
	return &p, nil
}

// restoreOrder 恢复顺序：父表必须先于子表写入，否则外键约束会拦。
// 载荷是 map[string][...]，Go 的 map 遍历顺序随机，不显式排序时
// 「collections 先于 scenes 恢复」会稳定地失败（FOREIGN KEY constraint failed）。
// notes 是自引用（parent_id → notes.id），由 sortRowsByParentDepth 单独处理。
var restoreOrder = []string{"workspaces", "scenes", "collections", "items", "notes"}

// sortRowsByParentDepth 把行按 parent_id 层级深度升序排列（只用于 notes）。
// 深度在本次行集合内计算：父节点不在集合里就当作根（深度 0）。
func sortRowsByParentDepth(rows []map[string]interface{}) {
	byID := make(map[string]map[string]interface{}, len(rows))
	for _, r := range rows {
		if id, ok := r["id"].(string); ok {
			byID[id] = r
		}
	}
	depth := make(map[string]int, len(rows))
	var calc func(id string) int
	calc = func(id string) int {
		if d, ok := depth[id]; ok {
			return d
		}
		depth[id] = 0 // 先占位，环引用时递归立刻收敛
		row := byID[id]
		if row == nil {
			return 0
		}
		pid, _ := row["parent_id"].(string)
		if pid == "" || byID[pid] == nil {
			return 0
		}
		d := calc(pid) + 1
		depth[id] = d
		return d
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := rows[i]["id"].(string)
		b, _ := rows[j]["id"].(string)
		return calc(a) < calc(b)
	})
}

// RestoreTombstone 恢复一条墓碑：按载荷原样写回各行（BulkInsert 是
// upsert，行仍在时只更新不报错），成功后删除墓碑。
// 恢复不校验「原位置是否被新数据占用」：id 原样写回即占用，
// 用户真在那位置建了新集合，恢复会把旧数据盖回去——这是「恢复」
// 的本来语义，由二次确认兜底。
func (d *Database) RestoreTombstone(collection, id string) error {
	p, err := d.GetTombstonePayload(collection, id)
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(p.Rows))
	for _, table := range restoreOrder {
		rows := p.Rows[table]
		done[table] = true
		if len(rows) == 0 {
			continue
		}
		if table == "notes" {
			sortRowsByParentDepth(rows)
		}
		if err := d.BulkInsert(table, rows); err != nil {
			return fmt.Errorf("恢复 %s 失败: %w", table, err)
		}
	}
	// 兜底：载荷里出现了 restoreOrder 未收录的表（将来新增可删类型时不至于静默丢失）。
	// 按表名排序保证同一次恢复行为可复现。
	rest := make([]string, 0)
	for table := range p.Rows {
		if !done[table] {
			rest = append(rest, table)
		}
	}
	sort.Strings(rest)
	for _, table := range rest {
		rows := p.Rows[table]
		if len(rows) == 0 {
			continue
		}
		if err := d.BulkInsert(table, rows); err != nil {
			return fmt.Errorf("恢复 %s 失败: %w", table, err)
		}
	}
	return d.DeleteTombstone(collection, id)
}

// DeleteTombstone 彻底删除一条墓碑（不可恢复）。
func (d *Database) DeleteTombstone(collection, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec(`DELETE FROM tombstones WHERE collection = ? AND id = ?`, collection, id)
	return err
}

// ClearTombstones 清空回收站。
func (d *Database) ClearTombstones() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM tombstones")
	return err
}

// captureSubtreeRows 便捷函数：按 id 集合捕获一张表的多行（id IN ...）。
func captureSubtreeRows(q rowQuerier, table string, ids []string) ([]map[string]interface{}, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return captureRows(q, table, "id IN ("+placeholders(len(ids))+")", argsOf(ids)...)
}

// placeholders 生成 n 个占位符（n<=0 时返回空串，调用方应保证 n>0）。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// argsOf 把 id 切片转成接口切片（SQL 参数要 []interface{}）。
func argsOf(ids []string) []interface{} {
	args := make([]interface{}, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	return args
}
