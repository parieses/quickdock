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

// 回收站主体类型（collection 取值）。
//
// 集中定义而不是在各删除点写字面量：回收站的展示名、图标、恢复路由都要按这个
// 值分支，散落成字符串后加一个新类型就要全局搜一遍。
// 命名用 Recycle 前缀而不是 Collection：db 包里 CollectionItem 已经是项目结构体的
// 名字，同包同名常量会直接编译不过；而且这里表达的是「回收站里的一类主体」，
// 与「项目集合」本来也不是一回事。
const (
	RecycleWorkspace = "workspaces"
	RecycleScene     = "scenes"
	RecycleSet       = "collections"
	RecycleItem      = "items"
	RecycleNote      = "notes"
	RecycleSchedule  = "schedules"
	// RecycleAITask 一个 AI 任务（会话）。载荷含会话行 + 它的全部消息行。
	RecycleAITask = "ai_tasks"
	// RecycleAISpace 一个 AI 空间。载荷含空间行 + 其下全部任务 + 这些任务的全部消息。
	RecycleAISpace = "ai_spaces"
)

// Tombstone 回收站条目（列表展示用，payload 不直接暴露给前端）。
type Tombstone struct {
	// Collection 主体类型，见 Collection* 常量。
	Collection string `json:"collection"`
	ID         string `json:"id"`    // 被删主体 id
	Label      string `json:"label"` // 展示名（主体 name，笔记回退 keyword）
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
// ai_spaces 必须先于 ai_conversations（它的 space_id 指向空间），后者又必须先于
// ai_messages —— 载荷是 map，遍历顺序随机，不显式排序时外键约束会稳定地拦。
var restoreOrder = []string{
	"workspaces", "scenes", "collections", "items", "notes",
	"ai_spaces", "ai_conversations", "ai_messages",
}

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

// ErrParentMissing 恢复失败：载荷里的主体所属的父记录（集合 / 场景 / 工作空间）
// 已在删除后被单独清理，原样写回会撞外键约束，只报一句「FOREIGN KEY constraint
// failed」用户完全不知道该怎么办。
//
// 把「缺哪一层」结构化地带出来，上层才能给出可操作的选项
// （条目 → 恢复到默认集合；场景/工作空间 → 提示先恢复父级）。
type ErrParentMissing struct {
	Subject  string // 正在恢复的主体类型（items / collections / scenes）
	ID       string // 主体 id
	Missing  string // 缺失的父表名
	ParentID string // 缺失的父 id
}

func (e *ErrParentMissing) Error() string {
	switch e.Missing {
	case "collections":
		return "所属集合已删除，可恢复到默认集合"
	case "scenes":
		return "所属场景已删除，请先恢复该场景再恢复此项"
	case "workspaces":
		return "所属工作空间已删除，请先恢复该工作空间再恢复此项"
	}
	return "父级数据已删除，无法原样恢复"
}

// checkRestoreParents 恢复前检查父链是否存在。
// 只查「载荷里最外层主体」的直接父级：更深的关系由更上层的父级自己把关。
func (d *Database) checkRestoreParents(p *tombstonePayload, collection, id string) error {
	checks := []struct {
		table   string // 载荷里的表
		field   string // 指向父级的列
		parent  string // 父表
		subject string
	}{
		{"items", "collection_id", "collections", RecycleItem},
		{"collections", "scene_id", "scenes", RecycleSet},
		{"scenes", "workspace_id", "workspaces", RecycleScene},
	}
	for _, c := range checks {
		rows := p.Rows[c.table]
		if len(rows) == 0 {
			continue
		}
		for _, row := range rows {
			pid, _ := row[c.field].(string)
			if pid == "" {
				continue // 历史数据没有父字段：交给外键约束兜
			}
			// 父级也在本次载荷里（删场景/工作空间时整棵子树一起拍）＝恢复时会先写回去，
			// 不算缺失。只查数据库会把「级联删除后的正常恢复」误判成父链断裂。
			if p.hasRow(c.parent, pid) || d.rowExists(c.parent, pid) {
				continue
			}
			subject := c.subject
			if collection != "" && c.table == collection {
				subject = collection
			}
			return &ErrParentMissing{Subject: subject, ID: id, Missing: c.parent, ParentID: pid}
		}
	}
	return nil
}

// hasRow 判断载荷里是否包含某表的某条记录（级联删除时长整棵子树一起拍）。
func (p *tombstonePayload) hasRow(table, id string) bool {
	for _, row := range p.Rows[table] {
		if str(row["id"]) == id {
			return true
		}
	}
	return false
}

// rowExists 判断某表里是否还有这条记录（表名走白名单校验）。
func (d *Database) rowExists(table, id string) bool {
	if err := validateTable(table); err != nil {
		return false
	}
	row, err := d.QueryOne("SELECT 1 FROM "+table+" WHERE id = ?", id)
	return err == nil && row != nil
}

// RestoreTombstone 恢复一条墓碑：按载荷原样写回各行（BulkInsert 是
// upsert，行仍在时只更新不报错），成功后删除墓碑。
// 恢复不校验「原位置是否被新数据占用」：id 原样写回即占用，
// 用户真在那位置建了新集合，恢复会把旧数据盖回去——这是「恢复」
// 的本来语义，由二次确认兜底。
//
// 但父链存在性必须先查：父记录被删掉后，子记录原样写回必然撞外键，
// 那时错误信息是一句 SQL 约束失败，用户既不知道原因也没有别的选择。
func (d *Database) RestoreTombstone(collection, id string) error {
	p, err := d.GetTombstonePayload(collection, id)
	if err != nil {
		return err
	}
	if err := d.checkRestoreParents(p, collection, id); err != nil {
		return err
	}
	if err := d.restorePayload(p); err != nil {
		return err
	}
	return d.DeleteTombstone(collection, id)
}

// RestoreTombstoneToDefault 恢复一条墓碑，但把主体挂到默认集合下。
//
// 用于「所属集合已删除」的条目：原样恢复不可能成功（父记录已经不在），
// 直接放弃又等于让用户误删的东西彻底回不来。这里新建/复用一个
// 「回收站恢复」集合承接，条目本身的数据一行不少。
func (d *Database) RestoreTombstoneToDefault(collection, id string) error {
	if collection != RecycleItem {
		return fmt.Errorf("只有快捷项支持恢复到默认集合")
	}
	p, err := d.GetTombstonePayload(collection, id)
	if err != nil {
		return err
	}
	rows := p.Rows["items"]
	if len(rows) == 0 {
		return fmt.Errorf("墓碑载荷里没有可恢复的条目")
	}
	colID, err := d.ensureRestoreCollection()
	if err != nil {
		return err
	}
	for _, row := range rows {
		row["collection_id"] = colID
	}
	// 改写父级后不再需要原父链检查：这一步的目的就是绕开已删除的集合。
	if err := d.restorePayload(p); err != nil {
		return err
	}
	return d.DeleteTombstone(collection, id)
}

// ensureRestoreCollection 返回「回收站恢复」集合的 id（不存在则在首个工作空间/场景下建一个）。
func (d *Database) ensureRestoreCollection() (string, error) {
	const name = "回收站恢复"
	if row, err := d.QueryOne("SELECT id FROM collections WHERE name = ? ORDER BY created_at LIMIT 1", name); err == nil && row != nil {
		return str(row["id"]), nil
	}
	ws, err := d.QueryOne("SELECT id FROM workspaces ORDER BY created_at LIMIT 1")
	if err != nil || ws == nil {
		return "", fmt.Errorf("没有可用的工作空间，无法恢复到默认集合")
	}
	wsID := str(ws["id"])
	sceneID, err := d.ensureScene(wsID, "默认")
	if err != nil {
		return "", err
	}
	col, err := d.CreateCollection(wsID, sceneID, name, "目录集合", "", "")
	if err != nil {
		// 并发下另一个 goroutine 刚建好同名集合：回查一次，别把恢复判成失败
		if row, qerr := d.QueryOne("SELECT id FROM collections WHERE name = ? ORDER BY created_at LIMIT 1", name); qerr == nil && row != nil {
			return str(row["id"]), nil
		}
		return "", err
	}
	return col.ID, nil
}

// restorePayload 按恢复顺序把载荷写回各表（RestoreTombstone 与
// RestoreTombstoneToDefault 共用，避免两条路径的恢复顺序/索引重建各写一遍）。
func (d *Database) restorePayload(p *tombstonePayload) error {
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
	// 墓碑恢复走通用 BulkInsert（upsert）直接写 notes，绕开了所有 Create*/Update*
	// 方法，索引不会自动同步。尤其「恢复覆盖已有行」时行数不变，migrate 的行数
	// 比对发现不了，会留下永久陈旧索引。这里在写回完成后、删墓碑前显式重建一次。
	// BulkInsert 各自加锁且已提交，此处单独取写锁调用不加锁的重建。
	if len(p.Rows["notes"]) > 0 {
		d.mu.Lock()
		rerr := d.rebuildNoteIndex()
		d.mu.Unlock()
		if rerr != nil {
			return fmt.Errorf("恢复后重建笔记全文索引失败: %w", rerr)
		}
	}
	return nil
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

// TombstoneMaxAgeDays 回收站保留天数。
//
// 30 天与剪贴板默认值一致：用户对「删掉的东西还能找回多久」的心理预期
// 应当是同一个数字，两处给不同天数只会让人以为其中一处坏了。
const TombstoneMaxAgeDays = 30

// PurgeExpiredTombstones 清理超过保留期的墓碑，返回删除条数。
//
// 为什么必须有这道闸：墓碑里存的是整行快照（一条对话的墓碑包含它的全部
// 消息），它是全库唯一一个既不进统计、也不受任何上限约束、还会随使用无限
// 增长的表。不清理的话，一个用了两年的库光回收站就能占掉可观体积，而用户在
// 界面上看不出任何异常。
//
// 顺带清掉随之落盘的文件（ai 生成的图片等）由调用方负责——这里只动表。
// 不做「清理即 VACUUM」：VACUUM 会重写整个库文件，放在启动路径上是几秒的
// 阻塞，而 SQLite 的页复用会自然吸收腾出的空间。
func (d *Database) PurgeExpiredTombstones(maxAgeDays int) (int, error) {
	if maxAgeDays <= 0 {
		maxAgeDays = TombstoneMaxAgeDays
	}
	// 用 julianday 换算而不是直接比字符串：deleted_at 存的是 RFC3339（带本地时区
	// 偏移量），跨时区/夏令时改过系统时间之后字典序就不再等于时间序。
	// 代价是这条 DELETE 用不上 idx_tombstones_deleted_at（函数作用在列上），
	// 回收站通常几十到几千条、每次启动跑一次，全表扫可以接受；那个索引留给
	// ListTombstones 的 ORDER BY 用。
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339)
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(
		`DELETE FROM tombstones WHERE julianday(deleted_at) < julianday(?)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
