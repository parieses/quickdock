package db

import (
	"sync"
	"sync/atomic"
	"time"
)

// 操作流水的种类。前端按 kind 选图标与配色，不反解 text——
// 文案会随 i18n 或措辞调整变，kind 是稳定契约。
const (
	ActivityItemOpened   = "item"      // 打开了项目/条目
	ActivitySshConnected = "ssh"       // 连上了远程主机
	ActivityPluginRun    = "plugin"    // 执行了插件命令
	ActivityScheduleRun  = "schedule"  // 触发了定时任务
	ActivitySshBatch     = "ssh_batch" // 向多台主机批量执行了同一条命令
)

// 操作来源：这条流水是谁触发的。
//
// 为什么需要：AI 通过 MCP 工具打开项目、跑插件命令，与用户自己点，写的是同一张表、
// 同一种动作。时间线里不标来源时，用户看到「打开 xxx」会以为是自己点的——
// 尤其 AI 连着开了好几个项目之后，人的记忆与流水会直接对不上。
const (
	ActivitySourceUI  = "ui"  // 用户自己操作（界面点击 / 快捷键）
	ActivitySourceMCP = "mcp" // AI 通过 MCP 工具调用触发
)

// Activity 一条操作流水。
//
// Text 存的是对象名而不是整句：动作词由 Kind 在前端按当前语言渲染
// （「打开『x』」/「Opened “x”」）。若在这里存中文整句，英文界面会直接
// 露出中文，而流水一旦落库就没法回头改语言了。
type Activity struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	RefID     string `json:"refId"`     // 关联对象 id，供前端将来做跳转
	Text      string `json:"text"`      // 对象名（项目名 / 主机名 / 插件命令名）
	Source    string `json:"source"`    // ActivitySourceUI / ActivitySourceMCP
	CreatedAt string `json:"createdAt"` // RFC3339，人读与排查用
	CreatedTs int64  `json:"createdTs"` // Unix 毫秒，排序 + 前端直接 new Date
}

// maxActivityRows 保留上限。流水只是「最近做过什么」，
// 不是审计账本，无限增长只会拖慢查询、也没什么人会翻到三个月前。
const maxActivityRows = 800

// activityTrimInterval 每多少次插入才检查一次裁剪。
// 与 plugin_exec_logs 同一套考量：把 O(n) 的 COUNT(*) 从「每写必查」
// 降为低频抽查，避免高频操作把流水表当成写放大源。
const activityTrimInterval = 50

var activityTrimTick int64

// activityDedupWindow 同一对象的重复记录抑制窗口。
//
// 为什么需要：打开项目、连接主机这类动作，用户很容易在十几秒内
// 连点两三次（双击、重试、切标签回来再点）。不去重的结果是时间线
// 被「打开 xxx」刷成一屏，真正有价值的其他动作被挤出去。
// 90 秒足够挡住手滑，又不会把「隔一会儿真的又用了一次」吞掉。
const activityDedupWindow = 90 * time.Second

// activitySeen 记录每个 kind:refID 上次落库的时刻。
//
// 条目规模受「项目数 + 主机数 + 插件命令数」限制，不会随时间无限增长，
// 所以不做淘汰。用 sync.Map 是因为埋点跑在任意 goroutine 上（终端连接、
// 调度器、UI 点击各一条路径），读数远多于写数。
var activitySeen sync.Map // string -> int64（UnixNano）

// AddActivity 记录一条操作流水，来源按「用户自己操作」处理。
// 需要区分来源（如 MCP 工具触发）时用 AddActivityFrom。
func (d *Database) AddActivity(kind, refID, name string) error {
	return d.AddActivityFrom(kind, refID, name, ActivitySourceUI)
}

// AddActivityFrom 记录一条带来源的操作流水。name 为对象名（不是整句，见 Activity.Text），
// source 取 ActivitySourceUI / ActivitySourceMCP（其它值一律归一为 ui——
// 「来源不明」时按用户自己操作处理，比标成 AI 更安全）。
//
// 全部埋点都是「主流程的旁路」：失败只返回 error，调用方一律忽略——
// 记不上一条动态绝不该让用户打开不了项目。
func (d *Database) AddActivityFrom(kind, refID, name, source string) error {
	if name == "" {
		return nil
	}
	if source != ActivitySourceMCP {
		source = ActivitySourceUI
	}
	now := time.Now()

	if refID != "" {
		key := kind + ":" + refID
		if last, ok := activitySeen.Load(key); ok {
			if ts, ok := last.(int64); ok && now.Sub(time.Unix(0, ts)) < activityDedupWindow {
				return nil
			}
		}
		activitySeen.Store(key, now.UnixNano())
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.conn.Exec(
		`INSERT INTO activity (id, kind, ref_id, text, created_at, created_ts, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID(), kind, refID, name, now.Format(time.RFC3339), now.UnixMilli(), source,
	); err != nil {
		return err
	}
	if atomic.AddInt64(&activityTrimTick, 1)%activityTrimInterval != 0 {
		return nil
	}
	var cnt int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM activity").Scan(&cnt); err != nil {
		return err
	}
	if cnt > maxActivityRows {
		_, err := d.conn.Exec(
			`DELETE FROM activity WHERE id NOT IN (
				SELECT id FROM activity ORDER BY created_ts DESC LIMIT ?)`, maxActivityRows)
		return err
	}
	return nil
}

// ListActivity 返回最近 limit 条操作流水（新的在前）。
func (d *Database) ListActivity(limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 100
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.conn.Query(
		`SELECT id, kind, ref_id, text, source, created_at, created_ts
		 FROM activity ORDER BY created_ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Activity, 0, limit)
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Kind, &a.RefID, &a.Text, &a.Source, &a.CreatedAt, &a.CreatedTs); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClearActivity 清空全部操作流水（用户手动清理，与自动裁剪无关）。
func (d *Database) ClearActivity() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.conn.Exec("DELETE FROM activity")
	return err
}

// ClearActivityWhere 按「来源 + 时间」批量清除操作流水。
// kind 为空表示不限来源；sinceTs<=0 表示不限时间（等价于全清）。
// 两者都给了就是「清除当前筛选结果」——筛选界面上的批量清除走这条，
// 而不是只有全清一个档位。
func (d *Database) ClearActivityWhere(kind string, sinceTs int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	query := "DELETE FROM activity WHERE 1=1"
	var args []interface{}
	if kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	if sinceTs > 0 {
		query += " AND created_ts >= ?"
		args = append(args, sinceTs)
	}
	_, err := d.conn.Exec(query, args...)
	return err
}
