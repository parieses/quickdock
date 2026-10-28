package db

import (
	"fmt"
	"strings"
)

// AIConversation AI 对话会话
type AIConversation struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Summary          string `json:"summary"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	RequestCount     int    `json:"request_count"`
	Model            string `json:"model"` // 最近一次问答使用的模型名（老数据为空）
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// AIMessage AI 对话消息
//
// Images 为 JSON 数组字符串，元素形如 {"kind":"url","url":"https://..."} 或
// {"kind":"file","path":"ai-images/xxx.png","name":"shot.png"}（path 相对数据目录）。
// 只出现在 user 消息上：assistant 回复永远是纯文本。图片字节本身不放 DB——
// file 类落盘到数据目录，发送时按需重读；url 类只存链接。
//
// 后四个字段只在 assistant 消息上有值：这一次调用的模型与用量。
// 存到消息上而不是只放 ai_usage，是因为「回答下面那行小字」要能随消息一起
// 重新加载出来 —— 前端内存里的统计一刷新就没了。
type AIMessage struct {
	ID               string `json:"id"`
	ConvID           string `json:"conv_id"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Images           string `json:"images"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CachedTokens     int    `json:"cached_tokens"`
	CreatedAt        string `json:"created_at"`
}

func mapToAIConversation(m map[string]interface{}) AIConversation {
	return AIConversation{
		ID:               str(m["id"]),
		Title:            str(m["title"]),
		Summary:          str(m["summary"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
		RequestCount:     integer(m["request_count"]),
		Model:            str(m["model"]),
		CreatedAt:        str(m["created_at"]),
		UpdatedAt:        str(m["updated_at"]),
	}
}

func mapToAIMessage(m map[string]interface{}) AIMessage {
	return AIMessage{
		ID:               str(m["id"]),
		ConvID:           str(m["conv_id"]),
		Role:             str(m["role"]),
		Content:          str(m["content"]),
		ReasoningContent: str(m["reasoning_content"]),
		Images:           str(m["images"]),
		Model:            str(m["model"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
		CachedTokens:     integer(m["cached_tokens"]),
		CreatedAt:        str(m["created_at"]),
	}
}

// ListAIConversations 会话列表（按更新时间倒序）
func (d *Database) ListAIConversations() ([]AIConversation, error) {
	rows, err := d.ListTableWhere("ai_conversations", "1=1 ORDER BY updated_at DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAIConversation), nil
}

// CreateAIConversation 新建会话
func (d *Database) CreateAIConversation(title string) (*AIConversation, error) {
	c := &AIConversation{
		ID:        newID(),
		Title:     strings.TrimSpace(title),
		CreatedAt: now(),
		UpdatedAt: now(),
	}
	if err := d.BulkInsert("ai_conversations", []map[string]interface{}{structToMap(c)}); err != nil {
		return nil, err
	}
	return c, nil
}

// GetAIConversation 读取单个会话
func (d *Database) GetAIConversation(id string) (*AIConversation, error) {
	row, err := d.QueryOne("SELECT * FROM ai_conversations WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("会话不存在")
	}
	c := mapToAIConversation(row)
	return &c, nil
}

// DeleteAIConversation 删除会话及其消息（级联）
func (d *Database) DeleteAIConversation(id string) error {
	if err := d.ExecuteParams("DELETE FROM ai_messages WHERE conv_id = ?", []interface{}{id}); err != nil {
		return err
	}
	return d.ExecuteParams("DELETE FROM ai_conversations WHERE id = ?", []interface{}{id})
}

// UpdateAIConversationMeta 更新标题/摘要/更新时间（空字段表示不更新）
func (d *Database) UpdateAIConversationMeta(id, title, summary string) error {
	sets := []string{}
	args := []interface{}{}
	if title != "" {
		sets = append(sets, "title = ?")
		args = append(args, title)
	}
	if summary != "" {
		sets = append(sets, "summary = ?")
		args = append(args, summary)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now())
	args = append(args, id)
	sql := "UPDATE ai_conversations SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	return d.ExecuteParams(sql, args)
}

// RecordAIUsage 记录一次模型调用的用量明细（ai_usage 一行）。
//
// 只写明细、**不动会话上的累加列**，两者分开是有意为之：
// 生成标题 / 压缩摘要也是模型调用（要花钱、要进统计），但它们不属于
// 「这个对话里的一问一答」。早期把两者混在一起累加，对话列表里的 token
// 就会比「各条回复下面那些数字之和」大出一截 —— 用户一眼就看出不对。
// 累加交给 AddAIConversationUsage，由调用方决定这次算不算一轮问答。
//
// convID 为空表示这次调用不属于任何对话（SSH 面板 / 剪贴板 / 笔记 / 插件的一次性调用）。
func (d *Database) RecordAIUsage(convID, model, kind string, u TokenUsage) error {
	if u.Empty() {
		return nil
	}
	if strings.TrimSpace(kind) == "" {
		kind = "chat"
	}
	return d.ExecuteParams(
		`INSERT INTO ai_usage (id, conv_id, model, kind, prompt_tokens, completion_tokens, cached_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		[]interface{}{newID(), strings.TrimSpace(convID), strings.TrimSpace(model), kind,
			u.Prompt, u.Completion, u.Cached, now()},
	)
}

// AddAIConversationUsage 把一次问答累加到会话行（列表要显示「这个对话花了多少」，
// 逐次 SUM 查一遍不划算）。只有真正的一问一答才该调它。
func (d *Database) AddAIConversationUsage(convID, model string, u TokenUsage) error {
	if u.Empty() || strings.TrimSpace(convID) == "" {
		return nil
	}
	return d.ExecuteParams(
		`UPDATE ai_conversations SET
		   prompt_tokens = prompt_tokens + ?, completion_tokens = completion_tokens + ?,
		   request_count = request_count + 1, model = ?, updated_at = ?
		 WHERE id = ?`,
		[]interface{}{u.Prompt, u.Completion, strings.TrimSpace(model), now(), strings.TrimSpace(convID)},
	)
}

// AIUsageGroup 用量明细的分组行（天 × 模型）
type AIUsageGroup struct {
	Day              string `json:"day"`
	Model            string `json:"model"`
	Kind             string `json:"kind"`
	Requests         int    `json:"requests"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CachedTokens     int    `json:"cachedTokens"`
}

// aiUsageFilter 窗口过滤条件；since 为空表示不限时间。
func aiUsageFilter(since string) (string, []interface{}) {
	if strings.TrimSpace(since) == "" {
		return "", nil
	}
	return " WHERE created_at >= ?", []interface{}{since}
}

// ListAIUsageGroups 按「天 × 模型」聚合用量明细。
//
// 一次查询同时喂给按天、按模型、总额三路统计：分三次查各写一遍过滤条件，
// 很容易出现「总额 ≠ 各模型之和」这种自相矛盾的界面。分组交给 SQL，
// 汇总与计价交给调用方，口径只有一处。
func (d *Database) ListAIUsageGroups(since string) ([]AIUsageGroup, error) {
	where, args := aiUsageFilter(since)
	d.mu.RLock()
	defer d.mu.RUnlock()
	rows, err := d.conn.Query(
		`SELECT substr(created_at, 1, 10) AS day, COALESCE(model, ''), COALESCE(kind, ''),
		        COUNT(*), COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0)
		 FROM ai_usage`+where+`
		 GROUP BY day, model, kind ORDER BY day DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AIUsageGroup{}
	for rows.Next() {
		var g AIUsageGroup
		if err := rows.Scan(&g.Day, &g.Model, &g.Kind, &g.Requests, &g.PromptTokens, &g.CompletionTokens, &g.CachedTokens); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CountAIUsageConversations 统计窗口内有过模型调用的会话数。
// 窗口口径与 ListAIUsageGroups 一致（按明细时间），否则「会话数」会对不上 token。
func (d *Database) CountAIUsageConversations(since string) (int, error) {
	where, args := aiUsageFilter(since)
	q := `SELECT COUNT(DISTINCT conv_id) FROM ai_usage`
	if where == "" {
		q += ` WHERE conv_id <> ''`
	} else {
		q += where + ` AND conv_id <> ''`
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	var n int
	err := d.conn.QueryRow(q, args...).Scan(&n)
	return n, err
}

// ListAIMessages 某会话的全部消息（按插入顺序正序）
// 注意：同一次问答里 user 与 assistant 的 created_at 往往落在同一秒，再用随机 UUID 作为
// tiebreaker 会导致顺序不确定（偶发 assistant 排在 user 前面）。改用 rowid（插入顺序）可
// 精确保证 user 消息始终先于其 assistant 回复，且对已存的乱序历史记录无需迁移即可正确展示。
func (d *Database) ListAIMessages(convID string) ([]AIMessage, error) {
	rows, err := d.ListTableWhere("ai_messages", "conv_id = ? ORDER BY rowid ASC", convID)
	if err != nil {
		return nil, err
	}
	return mapSlice(rows, mapToAIMessage), nil
}

// AddAIMessage 追加一条消息（不含 reasoning）
func (d *Database) AddAIMessage(convID, role, content string) (*AIMessage, error) {
	m := &AIMessage{
		ID:        newID(),
		ConvID:    convID,
		Role:      role,
		Content:   content,
		CreatedAt: now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// AddAIMessageFull 追加一条消息（含 reasoning_content）
func (d *Database) AddAIMessageFull(convID, role, content, reasoningContent string) (*AIMessage, error) {
	m := &AIMessage{
		ID:               newID(),
		ConvID:           convID,
		Role:             role,
		Content:          content,
		ReasoningContent: reasoningContent,
		CreatedAt:        now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// SaveAssistantTurn 追加一条助手回复，并记下这一次调用自己的用量。
//
// 与 AddAIMessageFull 分开：那个签名已经被「不带用量的写入」用着（例如外部
// 直接补一条消息），把 usage 塞成第 5 个参数会让每个调用点都要判断传什么。
// 用量放消息上是为了「回答下面那行小字」能持久化——前端 statMap 只活在当前会话。
func (d *Database) SaveAssistantTurn(convID, content, reasoningContent, model string, u TokenUsage) (*AIMessage, error) {
	m := &AIMessage{
		ID:               newID(),
		ConvID:           convID,
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoningContent,
		Model:            strings.TrimSpace(model),
		PromptTokens:     u.Prompt,
		CompletionTokens: u.Completion,
		CachedTokens:     u.Cached,
		CreatedAt:        now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// AddAIMessageImages 追加一条带图片引用的 user 消息。
// images 为 JSON 数组字符串（结构见 AIMessage.Images 注释），无图片时传 ""。
// 与 AddAIMessageFull 分开而不是加可选参数：调用点（流式链路）两种形态都要用，
// 分开后每个调用点的意图在签名上就可见，不会出现"这个参数到底传不传"的犹豫。
func (d *Database) AddAIMessageImages(convID, role, content, images string) (*AIMessage, error) {
	m := &AIMessage{
		ID:        newID(),
		ConvID:    convID,
		Role:      role,
		Content:   content,
		Images:    images,
		CreatedAt: now(),
	}
	if err := d.BulkInsert("ai_messages", []map[string]interface{}{structToMap(m)}); err != nil {
		return nil, err
	}
	return m, nil
}

// ClearAIConversation 清空某会话的全部消息与摘要（保留会话本身，用于"清空上下文"）
func (d *Database) ClearAIConversation(convID string) error {
	if err := d.ExecuteParams("DELETE FROM ai_messages WHERE conv_id = ?", []interface{}{convID}); err != nil {
		return err
	}
	return d.ExecuteParams("UPDATE ai_conversations SET summary = '', updated_at = ? WHERE id = ?", []interface{}{now(), convID})
}

// DeleteOldAIMessages 仅保留最近 keep 条，删除更早的消息（摘要压缩后清理冗余上下文）
// 用 rowid 代替 created_at 判定"最新"，避免同秒消息按随机 UUID 误删（可能把同一对问答中的
// user 留下而删掉 assistant，或反之）。rowid 即插入顺序，保留的是最近插入的 keep 条。
func (d *Database) DeleteOldAIMessages(convID string, keep int) error {
	if keep <= 0 {
		return nil
	}
	total, err := d.CountWhere("ai_messages", "conv_id = ?", convID)
	if err != nil {
		return err
	}
	if total <= keep {
		return nil
	}
	sql := `DELETE FROM ai_messages WHERE conv_id = ? AND id NOT IN (
		SELECT id FROM ai_messages WHERE conv_id = ? ORDER BY rowid DESC LIMIT ?
	)`
	return d.ExecuteParams(sql, []interface{}{convID, convID, keep})
}
