package ai

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/services"
)

// ---- 会话 CRUD（委托 db） ----

// AIListConversations 列出会话
func (a *AIService) AIListConversations() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.App.DB.ListAIConversations()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// AICreateConversation 新建会话
func (a *AIService) AICreateConversation(title string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	c, err := a.App.DB.CreateAIConversation(title)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(c)
}

// AIDeleteConversation 删除会话
func (a *AIService) AIDeleteConversation(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.DeleteAIConversation(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// AIMessageView 一条消息 + 它的费用估算。
//
// 为什么不把 Cost 加到 db.AIMessage 上：那个结构是**表列映射**，
// 多出来的字段会被写库路径当成一列（structToMap 连零值也转），
// 而 ai_messages 表里没有 cost 列。费用是「按当前价目表算出来的」，不该落库
// —— 价目表一改，历史回复的价格就该跟着重算，而不是停留在我写它那天的价格。
//
// 匿名嵌入让 JSON 与原结构完全同形（字段提升到同一层），前端不用改读取方式。
type AIMessageView struct {
	db.AIMessage
	// Cost 为 nil 表示该模型未收录价目表 → 前端隐藏这一项，而不是显示 ¥0。
	Cost *float64 `json:"cost,omitempty"`
}

// AIGetMessages 读取某会话的消息（附带每条回复的费用估算）
func (a *AIService) AIGetMessages(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	msgs, err := a.App.DB.ListAIMessages(convID)
	if err != nil {
		return services.Fail(err)
	}
	views := make([]AIMessageView, 0, len(msgs))
	for _, m := range msgs {
		v := AIMessageView{AIMessage: m}
		// 只有助手回复才有用量；用户消息行的 token 列恒为 0。
		if m.Role == "assistant" {
			if cost, ok := estimateCost(m.Model, m.PromptTokens, m.CompletionTokens, m.CachedTokens); ok {
				v.Cost = &cost
			}
		}
		views = append(views, v)
	}
	return services.Ok(views)
}

// AIClearMessages 清空某会话的上下文（消息与摘要）
func (a *AIService) AIClearMessages(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.ClearAIConversation(convID); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// AIRenameConversation 手动改会话标题。
//
// 与 AIRegenerateTitle 分开而不是给它加个「可选标题」参数：
// 这个不调模型、不计费、不碰 openai，语义上是纯粹的本地改名；
// 合进一个方法会让「点了会不会花钱」变成需要读代码才能回答的问题。
func (a *AIService) AIRenameConversation(convID, title string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return services.FailMsg("会话不存在")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return services.FailMsg("标题不能为空")
	}
	// 侧栏一行放不下更长的标题；这里截断而不是拒绝，改名时被弹回去更烦人。
	if utf8.RuneCountInString(title) > 60 {
		title = string([]rune(title)[:60]) + "…"
	}
	if err := a.App.DB.UpdateAIConversationMeta(convID, title, ""); err != nil {
		return services.Fail(err)
	}
	// 与自动生成标题发同一个事件：侧栏/列表只需监听一处就能都刷到。
	a.emitAI("ai:conv", map[string]interface{}{"id": convID, "title": title})
	return services.Ok(title)
}

// AIRegenerateTitle 调用模型，根据对话内容生成短标题
func (a *AIService) AIRegenerateTitle(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok || cfg.APIKey == "" {
		return services.FailMsg("missing api key")
	}
	msgs, err := a.App.DB.ListAIMessages(convID)
	if err != nil {
		return services.Fail(err)
	}
	if len(msgs) == 0 {
		return services.FailMsg("会话暂无消息")
	}
	var b strings.Builder
	b.WriteString("用不超过 15 个汉字给下面的对话起一个简洁标题，只输出标题本身，不要引号、标点或任何解释：\n\n")
	for _, m := range msgs {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	title, usage, err := a.callAIOnce(ctx, cfg, []map[string]string{
		{"role": "system", "content": "你是标题生成器，输出极简。"},
		{"role": "user", "content": b.String()},
	})
	if err != nil {
		return services.Fail(err)
	}
	// 生成标题也是一次完整计费的调用（输入通常是整段对话），不记就会系统性少算
	a.recordAIUsage(convID, usageKindTitle, cfg, usage)
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'")
	title = strings.Trim(title, "。！？.!?")
	if utf8.RuneCountInString(title) > 30 {
		title = string([]rune(title)[:30]) + "…"
	}
	if title == "" {
		return services.FailMsg("标题生成失败")
	}
	if err := a.App.DB.UpdateAIConversationMeta(convID, title, ""); err != nil {
		return services.Fail(err)
	}
	a.emitAI("ai:conv", map[string]interface{}{"id": convID, "title": title})
	return services.Ok(title)
}
