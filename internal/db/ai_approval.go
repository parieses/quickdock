package db

import (
	"fmt"
	"time"
)

// 审批结果的取值。写成常量而不是裸字符串：
// 这张表是用来回答「闸门是不是设得太严」的，一旦某个分支写成 "deny"、另一个写成
// "denied"，统计就会把同一件事算成两类，而错误只表现为「数字对不上」。
const (
	AIDecisionAllow        = "allow"
	AIDecisionDeny         = "deny"
	AIDecisionTimeout      = "timeout"
	AIDecisionStopped      = "stopped"
	AIDecisionDisconnected = "disconnected"
)

// AIApproval 一次「停下来问用户」的结果。
//
// 只记真的问过人的：读类工具自动放行，不为它写行——否则一次看十个文件就十条，
// 这张表会迅速变成噪音，而噪音里读不出「闸门太严」。
type AIApproval struct {
	ID     string
	ConvID string
	CallID string
	Tool   string
	// Decision 见上面 AIDecision* 常量。
	Decision string
	// Remember 用户是否选了「本次会话都允许」（含按命令族记住）。
	Remember  bool
	CreatedAt string
}

// RecordAIApproval 写一条审批明细。
func (d *Database) RecordAIApproval(a AIApproval) error {
	if a.CreatedAt == "" {
		a.CreatedAt = time.Now().Format(time.RFC3339)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	remember := 0
	if a.Remember {
		remember = 1
	}
	_, err := d.conn.Exec(
		`INSERT INTO ai_approvals (id, conv_id, call_id, tool, decision, remember, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.ConvID, a.CallID, a.Tool, a.Decision, remember, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("记录审批结果失败: %w", err)
	}
	return nil
}

// AIApprovalStat 按工具聚合的一行。
type AIApprovalStat struct {
	Tool string `json:"tool"`
	// Allow 用户点了允许。
	Allow int `json:"allow"`
	// Deny 用户明确点了拒绝。
	Deny int `json:"deny"`
	// Auto 没人点：超时、停止、连接断开。与 Deny 分开统计——
	// 「用户不想让它干」和「用户压根没看到这张卡」是两种完全不同的问题，
	// 混在一起看，超时会被误读成「用户很反感这个工具」。
	Auto int `json:"auto"`
}

// AIApprovalSummary 一个时间区间内的审批概况。
type AIApprovalSummary struct {
	Allow  int              `json:"allow"`
	Deny   int              `json:"deny"`
	Auto   int              `json:"auto"`
	ByTool []AIApprovalStat `json:"by_tool"`
}

// AIApprovalSummary 统计 since 之后的审批结果。
//
// 一次分组查完再汇总，不是「先查总数再查分组」：两次查询之间可能进来新行，
// 那样「总额」和「各工具之和」会对不上，而用户只会觉得这个统计在瞎报。
func (d *Database) AIApprovalSummary(since string) (AIApprovalSummary, error) {
	out := AIApprovalSummary{ByTool: []AIApprovalStat{}}
	q := `SELECT tool,
		        SUM(CASE WHEN decision = ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN decision = ? THEN 1 ELSE 0 END),
		        SUM(CASE WHEN decision NOT IN (?, ?) THEN 1 ELSE 0 END)
		 FROM ai_approvals`
	args := []any{AIDecisionAllow, AIDecisionDeny, AIDecisionAllow, AIDecisionDeny}
	if since != "" {
		// 空串 = 全部时间（与用量统计的 usageSince 同口径）。
		q += ` WHERE julianday(created_at) >= julianday(?)`
		args = append(args, since)
	}
	q += ` GROUP BY tool ORDER BY COUNT(*) DESC`

	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s AIApprovalStat
		var (
			allow, deny, auto int64
		)
		if err := rows.Scan(&s.Tool, &allow, &deny, &auto); err != nil {
			return out, err
		}
		s.Allow, s.Deny, s.Auto = int(allow), int(deny), int(auto)
		out.Allow += s.Allow
		out.Deny += s.Deny
		out.Auto += s.Auto
		out.ByTool = append(out.ByTool, s)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// PurgeAIApprovals 删掉 before 之前的审批明细。
//
// 明细表的用途是「最近这段时间闸门表现如何」，没有长期留存价值；
// 而它长得比用量表快（一次提问可以问好几次），不设上限会比 ai_usage 更早成为负担。
func (d *Database) PurgeAIApprovals(before time.Time) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	res, err := d.conn.Exec(
		`DELETE FROM ai_approvals WHERE julianday(created_at) < julianday(?)`,
		before.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
