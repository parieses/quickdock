package ai

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

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

// AIGetMessages 读取某会话的消息
func (a *AIService) AIGetMessages(convID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	msgs, err := a.App.DB.ListAIMessages(convID)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(msgs)
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
	title, err := a.callAIOnce(ctx, cfg, []map[string]string{
		{"role": "system", "content": "你是标题生成器，输出极简。"},
		{"role": "user", "content": b.String()},
	})
	if err != nil {
		return services.Fail(err)
	}
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
