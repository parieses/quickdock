package db

import (
	"fmt"
	"strings"
)

// AIConversation AI 对话会话
type AIConversation struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	PromptTokens    int    `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// AIMessage AI 对话消息
type AIMessage struct {
	ID               string `json:"id"`
	ConvID           string `json:"conv_id"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	CreatedAt        string `json:"created_at"`
}

func mapToAIConversation(m map[string]interface{}) AIConversation {
	return AIConversation{
		ID:               str(m["id"]),
		Title:            str(m["title"]),
		Summary:          str(m["summary"]),
		PromptTokens:     integer(m["prompt_tokens"]),
		CompletionTokens: integer(m["completion_tokens"]),
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

// UpdateAIConversationUsage 更新对话的 token 用量统计
func (d *Database) UpdateAIConversationUsage(id string, promptTokens, completionTokens int) error {
	if promptTokens <= 0 && completionTokens <= 0 {
		return nil
	}
	return d.ExecuteParams(
		"UPDATE ai_conversations SET prompt_tokens = prompt_tokens + ?, completion_tokens = completion_tokens + ?, updated_at = ? WHERE id = ?",
		[]interface{}{promptTokens, completionTokens, now(), id},
	)
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
