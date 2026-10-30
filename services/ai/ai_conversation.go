package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
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

// ---- 空间 ----

// AIListSpaces 列出全部空间
func (a *AIService) AIListSpaces() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.App.DB.ListAISpaces()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// AICreateSpace 新建空间：绑定一个目录，把后续任务收在它下面。
//
// 同一目录只建一个空间：用户换一种写法（大小写、结尾斜杠、盘符）再选一次，
// 不该凭空多出一个并行空间——否则同一个项目在侧栏里出现两次，谁也不知道该进哪个。
// 命中已有空间时直接返回它，而不是报错：用户的心智是「我要在这个目录里干活」，
// 不是「我要新建一个空间」。
//
// 目录必须是真实存在的文件夹：把不存在的路径存进空间，之后每条任务的工具调用
// 都会以「找不到文件」告终，而用户看不出根因在这里。
func (a *AIService) AICreateSpace(name, cwd string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return services.FailMsg("请先选择一个目录")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return services.FailMsg("目录路径无法解析：" + err.Error())
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return services.FailMsg("选中的不是文件夹或目录打不开：" + abs)
	}
	abs = filepath.Clean(abs)
	if exist, err := a.App.DB.FindAISpaceByCwd(abs); err == nil && exist != nil {
		return services.Ok(exist)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(abs)
	}
	if utf8.RuneCountInString(name) > 40 {
		name = string([]rune(name)[:40]) + "…"
	}
	s, err := a.App.DB.CreateAISpace(name, abs)
	if err != nil {
		return services.Fail(err)
	}
	logger.I("[ai] 新建空间 %s → %s", s.ID, abs)
	return services.Ok(s)
}

// AIRenameSpace 改空间名（保留绑定目录）。
func (a *AIService) AIRenameSpace(id, name string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	if id == "" {
		return services.FailMsg("空间不存在")
	}
	if name == "" {
		return services.FailMsg("名称不能为空")
	}
	if utf8.RuneCountInString(name) > 40 {
		name = string([]rune(name)[:40]) + "…"
	}
	if err := a.App.DB.UpdateAISpaceMeta(id, name, ""); err != nil {
		return services.Fail(err)
	}
	return services.Ok(name)
}

// AIDeleteSpace 删除空间：连同它下面的全部任务与消息一起进回收站。
//
// 危险度比删单个任务高得多（一次带走十几条对话），所以提示语里要把数量说清楚，
// 让用户在点确认前就知道这会带走什么。
func (a *AIService) AIDeleteSpace(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return services.FailMsg("空间不存在")
	}
	if err := a.App.DB.DeleteAISpaceToRecycle(id); err != nil {
		return services.Fail(err)
	}
	logger.I("[ai] 空间 %s 已删除（进回收站）", id)
	return services.Ok(nil)
}

// ---- 任务 ----

// AICreateConversation 新建任务。
//
// mode 是创建时选定的任务类型（对话/翻译/总结/解释/使用教程），存库后跟着这个
// 任务走：之前模式只是前端一个 ref，切走再回来就掉回「对话」，于是「这条是用来
// 翻译的」这个最关键的属性丢了。
// 非法 mode 直接回落 chat 而不是报错：模式只是一个提示词分支，为它拦下整个创建
// 流程不值得，而且旧客户端可能传空串。
func (a *AIService) AICreateConversation(title, mode, spaceID string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	mode = strings.TrimSpace(mode)
	if aiModePrompts[mode] == "" {
		mode = "chat"
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID != "" {
		if _, err := a.App.DB.GetAISpace(spaceID); err != nil {
			// 空间已被删（比如在另一个窗口里）时按「普通任务」创建，而不是让新建失败：
			// 用户此刻的意图是开一条对话，不是维护空间引用完整性。
			logger.W("[ai] 空间 %s 不存在，任务按普通任务创建: %v", spaceID, err)
			spaceID = ""
		}
	}
	c, err := a.App.DB.CreateAIConversation(db.NewAIConversation{
		Title:   title,
		Mode:    mode,
		SpaceID: spaceID,
	})
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(c)
}

// AIDeleteConversation 删除任务（进回收站，可恢复）。
//
// 走回收站而不是硬删：一条任务里攒着几十轮对话，误删等于丢掉一整段上下文。
func (a *AIService) AIDeleteConversation(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.DeleteAIConversationToRecycle(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// AIMessageView 一条消息 + 它的费用估算。
//
// 为什么不把 Cost 加到 db.AIMessage 上：那个结构是表列映射，
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

// AISetConversationMode 改任务的模式（bound）。
//
// 模式本来是「创建时选定的属性」，但计划模式打破了这一点：计划只能想不能做，
// 用户要动手就必须把这条任务切回对话模式。不落库的话，切走再回来
// （selectConv 以库里的 mode 为准）又变回计划模式，工具面继续只有只读的，
// 模型会一边说「我来改」一边什么都调不动。
func (a *AIService) AISetConversationMode(convID, mode string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	convID, mode = strings.TrimSpace(convID), strings.TrimSpace(mode)
	if convID == "" {
		return services.FailMsg("任务不存在")
	}
	// 与创建时同一套校验：认不出的模式直接拒绝。这里不能像发送那样回落 chat——
	// 回落会把用户的「计划模式任务」悄悄改成普通对话，而用户看不出发生过什么。
	if aiModePrompts[mode] == "" {
		return services.FailMsg("未知模式：" + mode)
	}
	if err := a.App.DB.SetAIConversationMode(convID, mode); err != nil {
		return services.Fail(err)
	}
	return services.Ok(mode)
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
	// 只喂最近的一小段（见 aiTitleMaxMsgs / aiTitleMaxRune 的说明）：
	// 标题需要的只是「这段对话现在在说什么」，不是全部历史。
	// 从后往前累积，先保证最新的内容一定在，超预算就停——
	// 反过来从前往后截断，会把最近的话题砍掉，标题反而会答非所问。
	tail := msgs
	if len(tail) > aiTitleMaxMsgs {
		tail = tail[len(tail)-aiTitleMaxMsgs:]
	}
	used := 0
	kept := make([]db.AIMessage, 0, len(tail))
	for i := len(tail) - 1; i >= 0; i-- {
		line := tail[i].Role + ": " + tail[i].Content + "\n"
		used += len([]rune(line))
		if used > aiTitleMaxRune && len(kept) > 0 {
			break
		}
		kept = append(kept, tail[i])
	}
	if len(kept) < len(tail) {
		b.WriteString("（以下为这个会话最近的一部分）\n\n")
	}
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString(kept[i].Role + ": " + kept[i].Content + "\n")
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
