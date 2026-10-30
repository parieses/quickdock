package db

// 多 agent 编排的运行模型：run（一次编排）/ node（一次派活）/ event（事件轨迹）。
//
// 与 ai_conversations / ai_messages 的分工：
//   - ai_messages 存用户看到的内容（一问一答的正文），是产品数据；
//   - agent_* 存执行过程（谁派的谁、跑到第几步、花了多少、失败在哪），是运行时数据。
//
// 两者故意不合并：删会话时要保留的是前者，后者是排障用的轨迹，重建一次就没有意义了。
// 因此 run/node/event 也不进回收站——恢复一条对话不该连带恢复半年前那次失败的执行树。
//
// 这里所有写入都走显式列名的参数化 SQL，不用 structToMap + BulkInsert：
// 那套是给「整行结构体」用的，而 run/node 的更新是局部的（先建、后改状态、
// 再补结果），整行写回会把并发中别人刚写的状态覆盖成旧值。

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// run / node 的状态取值。
//
// 用常量而不是散落的字符串字面量：这些值会被前端用来上色、被 Supervisor 用来
// 判断「还能不能继续派活」，拼错一次就是永远卡在 running 的 run。
const (
	AgentStatusPending  = "pending"
	AgentStatusRunning  = "running"
	AgentStatusDone     = "done"
	AgentStatusFailed   = "failed"
	AgentStatusCanceled = "canceled"
)

// AgentRun 一次编排。
type AgentRun struct {
	ID     string `json:"id"`
	ConvID string `json:"conv_id"`
	// Goal 这次编排要达成的目标（用户原话或 Supervisor 归纳的一句话）。
	Goal   string `json:"goal"`
	Status string `json:"status"`
	Cwd    string `json:"cwd"`
	// Trigger 谁发起的：chat / schedule / plugin / mcp。
	// 必须有这一列——「这条 run 为什么自己跑起来了」只能靠它回答，
	// 事后从事件里猜是猜不出来的。
	Trigger string `json:"trigger"`
	// BudgetTokens 本次 run 的 token 上限（0 = 不限）。超限由 Supervisor 拦，
	// 不在这一层做硬中断：db 不知道该停哪一步。
	BudgetTokens int    `json:"budget_tokens"`
	UsedTokens   int    `json:"used_tokens"`
	Error        string `json:"error"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// NewAgentRun 建 run 的入参。ID 留空则自动生成。
type NewAgentRun struct {
	ID           string
	ConvID       string
	Goal         string
	Cwd          string
	Trigger      string
	BudgetTokens int
}

// AgentNode run 树的一个节点 = 一次派活。
type AgentNode struct {
	ID       string `json:"id"`
	RunID    string `json:"run_id"`
	ParentID string `json:"parent_id"`
	// Seq 同一 run 内的派活顺序，由库分配（MAX(seq)+1），用于稳定排序：
	// 时间戳精度到秒，同一秒内派出的两个节点靠 created_at 分不出先后。
	Seq              int    `json:"seq"`
	Role             string `json:"role"`
	Task             string `json:"task"`
	Status           string `json:"status"`
	Cwd              string `json:"cwd"`
	Model            string `json:"model"`
	Result           string `json:"result"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	StartedAt        string `json:"started_at"`
	EndedAt          string `json:"ended_at"`
}

// NewAgentNode 建节点的入参。Seq 由库分配，不从入参取。
type NewAgentNode struct {
	ID       string
	RunID    string
	ParentID string
	Role     string
	Task     string
	Cwd      string
	Model    string
}

// AgentEvent 一条执行事件。
//
// Kind 与 SSE 事件名同构（tool / tool_result / notice / error / done），
// 这样同一个 sink 接口既能写连接、也能写表，前端两种来源用同一套渲染。
type AgentEvent struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	NodeID    string `json:"node_id"`
	Seq       int    `json:"seq"`
	Kind      string `json:"kind"`
	Payload   string `json:"payload"`
	CreatedAt string `json:"created_at"`
}

// 事件载荷上限：工具结果可能是几 MB 的日志/文件内容，整段塞进事件表会让
// 「按 run 拉增量」变成拉一个大文档。超限只留预览 + 原始大小，
// 完整内容在工具层自己的日志/消息里。
const (
	agentEventMaxPayload   = 32 << 10 // 32 KB
	agentEventPreviewBytes = 2 << 10  // 截断后保留 2 KB 预览
	agentEventKeepPerRun   = 2000     // 每个 run 最多保留的事件条数
	agentEventPruneEvery   = 500      // 每追加这么多条才修剪一次（避免每条都 COUNT/DELETE）
)

func mapToAgentRun(m map[string]interface{}) AgentRun {
	return AgentRun{
		ID:           str(m["id"]),
		ConvID:       str(m["conv_id"]),
		Goal:         str(m["goal"]),
		Status:       str(m["status"]),
		Cwd:          str(m["cwd"]),
		Trigger:      str(m["trigger"]),
		BudgetTokens: integer(m["budget_tokens"]),
		UsedTokens:   integer(m["used_tokens"]),
		Error:        str(m["error"]),
		CreatedAt:    str(m["created_at"]),
		UpdatedAt:    str(m["updated_at"]),
	}
}

func mapToAgentNode(m map[string]interface{}) AgentNode {
	return AgentNode{
		ID:               str(m["id"]),
		RunID:            str(m["run_id"]),
		ParentID:         str(m["parent_id"]),
		Seq:              integer(m["seq"]),
		Role:             str(m["role"]),
		Task:             str(m["task"]),
		Status:           str(m["status"]),
		Cwd:              str(m["cwd"]),
		Model:            str(m["model"]),
		Result:           str(m["result"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
		StartedAt:        str(m["started_at"]),
		EndedAt:          str(m["ended_at"]),
	}
}

func mapToAgentEvent(m map[string]interface{}) AgentEvent {
	return AgentEvent{
		ID:        str(m["id"]),
		RunID:     str(m["run_id"]),
		NodeID:    str(m["node_id"]),
		Seq:       integer(m["seq"]),
		Kind:      str(m["kind"]),
		Payload:   str(m["payload"]),
		CreatedAt: str(m["created_at"]),
	}
}

// normAgentStatus 把未知/空状态收口到 running。
//
// 不做报错：状态是内部产生的，出现未知值说明代码里写错了常量，
// 但「因此让整次执行失败」比「当成 running 继续跑」对用户更糟。
func normAgentStatus(s string) string {
	switch strings.TrimSpace(s) {
	case AgentStatusPending, AgentStatusRunning, AgentStatusDone, AgentStatusFailed, AgentStatusCanceled:
		return strings.TrimSpace(s)
	}
	return AgentStatusRunning
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// shrinkAgentPayload 把超大事件载荷换成「预览 + 原始大小」的合法 JSON。
//
// 不能直接 payload[:n]：那可能切在多字节 UTF-8 字符或 JSON 转义序列中间，
// 前端 JSON.parse 会整帧失败（表现为「事件全丢」而不是「内容被截断」）。
// 所以截断后再用 json.Marshal 重新转义，保证输出始终是合法 JSON。
func shrinkAgentPayload(payload string) string {
	if len(payload) <= agentEventMaxPayload {
		return payload
	}
	preview := payload[:agentEventPreviewBytes]
	preview = strings.ToValidUTF8(preview, "")
	wrapped, err := json.Marshal(map[string]interface{}{
		"truncated": true,
		"size":      len(payload),
		"preview":   preview,
	})
	if err != nil {
		return `{"truncated":true}`
	}
	return string(wrapped)
}

// CreateAgentRun 建一次 run。
func (d *Database) CreateAgentRun(in NewAgentRun) (*AgentRun, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = newID()
	}
	trigger := strings.TrimSpace(in.Trigger)
	if trigger == "" {
		trigger = "chat"
	}
	ts := now()
	budget := in.BudgetTokens
	if budget < 0 {
		budget = 0
	}
	if err := d.ExecuteParams(
		`INSERT INTO agent_runs (id, conv_id, goal, status, cwd, trigger, budget_tokens, used_tokens, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
		[]interface{}{id, strings.TrimSpace(in.ConvID), strings.TrimSpace(in.Goal),
			AgentStatusRunning, strings.TrimSpace(in.Cwd), trigger, budget, ts, ts},
	); err != nil {
		return nil, err
	}
	return d.GetAgentRun(id)
}

// GetAgentRun 读一条 run。
func (d *Database) GetAgentRun(id string) (*AgentRun, error) {
	row, err := d.QueryOne(`SELECT * FROM agent_runs WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r := mapToAgentRun(row)
	return &r, nil
}

// ListAgentRunsByConv 某条对话下的 run（新的在前）。
//
// 侧栏不展示 run，但对话详情要显示「这条对话里有哪几次编排」——
// 收在对话里而不是刷出一堆平级会话，是「子 agent 不进侧栏」的落地方式。
func (d *Database) ListAgentRunsByConv(convID string, limit int) ([]AgentRun, error) {
	rows, err := d.Query(
		`SELECT * FROM agent_runs WHERE conv_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		strings.TrimSpace(convID), clampLimit(limit, 20, 200))
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAgentRun), nil
}

// UpdateAgentRunStatus 改 run 的状态（终态时同时记错误信息）。
//
// 不动 used_tokens：用量是累加的，和状态推进不是同一件事，
// 合在一个 UPDATE 里迟早出现「补状态时把用量覆盖成 0」。
func (d *Database) UpdateAgentRunStatus(id, status, errMsg string) error {
	return d.ExecuteParams(
		`UPDATE agent_runs SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		[]interface{}{normAgentStatus(status), strings.TrimSpace(errMsg), now(), strings.TrimSpace(id)},
	)
}

// AddAgentRunUsage 累加一次 run 已消耗的 token。
func (d *Database) AddAgentRunUsage(id string, tokens int) error {
	if tokens <= 0 {
		return nil
	}
	return d.ExecuteParams(
		`UPDATE agent_runs SET used_tokens = used_tokens + ?, updated_at = ? WHERE id = ?`,
		[]interface{}{tokens, now(), strings.TrimSpace(id)},
	)
}

// CreateAgentNode 在 run 树里建一个节点，seq 由库分配。
//
// 分配 seq 与插入必须同事务：两个子 agent 并发派活时，先 SELECT MAX 再 INSERT
// 会拿到同一个 seq，排序就变成「谁先提交谁靠前」这种不可解释的顺序。
func (d *Database) CreateAgentNode(in NewAgentNode) (*AgentNode, error) {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = newID()
	}
	status := AgentStatusPending
	startedAt := ""
	if strings.TrimSpace(in.Task) != "" {
		// 建出来就带任务，说明这次派活立刻要跑（pending 是「已登记未开工」，
		// 只在编排器要按依赖顺序排队时才用得上）。
		status = AgentStatusRunning
		startedAt = now()
	}
	err := d.Transaction(func(tx *sql.Tx) error {
		var seq int
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM agent_nodes WHERE run_id = ?`,
			strings.TrimSpace(in.RunID),
		).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO agent_nodes (id, run_id, parent_id, seq, role, task, status, cwd, model,
			                          result, prompt_tokens, completion_tokens, started_at, ended_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, 0, ?, '')`,
			id, strings.TrimSpace(in.RunID), strings.TrimSpace(in.ParentID), seq,
			strings.TrimSpace(in.Role), strings.TrimSpace(in.Task), status,
			strings.TrimSpace(in.Cwd), strings.TrimSpace(in.Model), startedAt,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d.GetAgentNode(id)
}

// GetAgentNode 读一个节点。
func (d *Database) GetAgentNode(id string) (*AgentNode, error) {
	row, err := d.QueryOne(`SELECT * FROM agent_nodes WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	n := mapToAgentNode(row)
	return &n, nil
}

// UpdateAgentNodeResult 写节点的执行结果与用量。
func (d *Database) UpdateAgentNodeResult(id, status, result string, u TokenUsage) error {
	st := normAgentStatus(status)
	// 结束后才盖 ended_at：跑着的节点结束时间为空，前端才能算「还在跑多久」。
	endedAt := ""
	if st == AgentStatusDone || st == AgentStatusFailed || st == AgentStatusCanceled {
		endedAt = now()
	}
	return d.ExecuteParams(
		`UPDATE agent_nodes SET status = ?, result = ?, prompt_tokens = ?, completion_tokens = ?,
		   ended_at = CASE WHEN ? = '' THEN ended_at ELSE ? END
		 WHERE id = ?`,
		[]interface{}{st, strings.TrimSpace(result), u.Prompt, u.Completion, endedAt, endedAt, strings.TrimSpace(id)},
	)
}

// ListAgentNodes run 树的全部节点（按派活顺序）。
func (d *Database) ListAgentNodes(runID string) ([]AgentNode, error) {
	rows, err := d.Query(
		`SELECT * FROM agent_nodes WHERE run_id = ? ORDER BY seq ASC`, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAgentNode), nil
}

// AppendAgentEvent 追加一条事件，返回分配的 seq。
//
// seq 单调递增是「前端轮询增量拉取」的全部依据：客户端记住上次的 seq，
// 只拉 seq > 它的那些。用时间戳做不到——同一毫秒内会写出多条事件，边界会重复或漏。
func (d *Database) AppendAgentEvent(runID, nodeID, kind, payload string) (int, error) {
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return 0, sql.ErrNoRows
	}
	body := shrinkAgentPayload(payload)
	var seq int
	err := d.Transaction(func(tx *sql.Tx) error {
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM agent_events WHERE run_id = ?`, rid,
		).Scan(&seq); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO agent_events (id, run_id, node_id, seq, kind, payload, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			newID(), rid, strings.TrimSpace(nodeID), seq, strings.TrimSpace(kind), body, now(),
		); err != nil {
			return err
		}
		// 长 run 会无限增长：每 agentEventPruneEvery 条修剪一次，只留最近
		// agentEventKeepPerRun 条。修剪放在「追加」路径而不是定时任务里，
		// 因为事件表只在执行期有写入，闲时不需要它动。
		if seq%agentEventPruneEvery == 0 && seq > agentEventKeepPerRun {
			if _, err := tx.Exec(
				`DELETE FROM agent_events WHERE run_id = ? AND seq <= ?`, rid, seq-agentEventKeepPerRun,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// ListAgentEvents 拉取 run 的增量事件（seq > afterSeq，升序）。
//
// afterSeq = 0 表示从头拉。limit 由调用方给（轮询一般 200），
// 上限 500：一次拉太多会让前端单帧处理时间超过轮询间隔，越积越慢。
func (d *Database) ListAgentEvents(runID string, afterSeq, limit int) ([]AgentEvent, error) {
	rows, err := d.Query(
		`SELECT * FROM agent_events WHERE run_id = ? AND seq > ? ORDER BY seq ASC LIMIT ?`,
		strings.TrimSpace(runID), afterSeq, clampLimit(limit, 200, 500))
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAgentEvent), nil
}

// DeleteAgentRunsForConv 删除某条对话的全部 run（节点与事件靠外键级联）。
//
// 由 DeleteAIConversation 调用：会话是产品数据，run 是它的附庸，
// 留一堆指向已删会话的执行树只会污染排障视图。
func (d *Database) DeleteAgentRunsForConv(convID string) error {
	return d.ExecuteParams(`DELETE FROM agent_runs WHERE conv_id = ?`, []interface{}{strings.TrimSpace(convID)})
}
