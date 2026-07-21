package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/platform"

	"github.com/google/uuid"
)

const (
	aiProfilesKey = "ai_profiles"
	aiActiveKey   = "ai_active_profile"
	aiLegacyKey   = "ai_config"

	aiDefaultModel = "gpt-4o-mini"
	aiDefaultTemp  = 0.7
	aiDefaultMax   = 8192
)

// AIProfile 一个完整的 AI 配置档案（含 API Key，前端回填时为明文）
type AIProfile struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Provider         string  `json:"provider"`
	BaseURL          string  `json:"baseURL"`
	APIKey           string  `json:"apiKey"`
	Model            string  `json:"model"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"maxTokens"`
	SystemPrompt     string  `json:"systemPrompt"`
	TopP             float64 `json:"topP"`
	FrequencyPenalty float64 `json:"frequencyPenalty"`
	PresencePenalty  float64 `json:"presencePenalty"`
	ThinkingEnabled  bool    `json:"thinkingEnabled"`
}

// aiProfileStored 落库结构（API Key 为密文）
type aiProfileStored struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Provider         string  `json:"provider"`
	BaseURL          string  `json:"baseURL"`
	APIKey           string  `json:"apiKey"`
	Model            string  `json:"model"`
	Temperature      float64 `json:"temperature"`
	MaxTokens        int     `json:"maxTokens"`
	SystemPrompt     string  `json:"systemPrompt"`
	TopP             float64 `json:"topP"`
	FrequencyPenalty float64 `json:"frequencyPenalty"`
	PresencePenalty  float64 `json:"presencePenalty"`
	ThinkingEnabled  bool    `json:"thinkingEnabled"`
}

// AIProfilesResult 返回给前端的档案列表与当前激活项
type AIProfilesResult struct {
	Active   string      `json:"active"`
	Profiles []AIProfile `json:"profiles"`
}

// AISaveProfilesRequest 保存档案列表的请求
type AISaveProfilesRequest struct {
	Active   string      `json:"active"`
	Profiles []AIProfile `json:"profiles"`
}

// apiEndpoint 根据 provider 构建正确的 API URL 和认证头。
// - OpenAI/DK 系列：BaseURL + "/chat/completions"，Bearer token
// - Azure：BaseURL/openai/deployments/{model}/chat/completions?api-version=2024-02-15-preview，api-key header
func apiEndpoint(cfg AIProfile) (url string, authKey, authVal string) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Provider == "azure" {
		ep := base + "/openai/deployments/" + cfg.Model + "/chat/completions?api-version=2024-02-15-preview"
		return ep, "api-key", cfg.APIKey
	}
	return base + "/chat/completions", "Authorization", "Bearer " + cfg.APIKey
}

// AIConfig 兼容旧接口的单一配置（API Key 已解密）
type AIConfig struct {
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"baseURL"`
	APIKey      string  `json:"apiKey"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"maxTokens"`
}

var aiProviderPresets = map[string]string{
	"openai":   "https://api.openai.com/v1",
	"deepseek": "https://api.deepseek.com/v1",
	"kimi":     "https://api.moonshot.cn/v1",
	"qwen":     "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"ollama":   "http://localhost:11434/v1",
	"azure":    "",
	"custom":   "",
}

// aiModePrompts 四种模式的 system prompt（仅切换提示，不另写接口）
var aiModePrompts = map[string]string{
	"chat": `你正在帮用户使用 QuickDock v3，这是一款 Windows 桌面效率工具（类似 Raycast + 工作空间管理器）。

当前版本核心功能：
- 工作空间/场景/集合/项目的层级管理，支持文件/文件夹/URL/命令等类型
- 剪贴板历史管理（自动采集、搜索、置顶、过期清理）
- 命令面板（全局搜索 Ctrl+K，FTS5 全文索引）
- 文本片段 (Snippets) 快捷输入
- 网站监控、定时任务、待办事项
- AI 对话（当前你就在这个功能里）
- 插件系统（18 个内置插件，支持第三方扩展）
- 数据快照备份、WebDAV 云同步
- 全局热键自定义

你是一个有帮助的中文 AI 助手，回答简洁准确。用户可能会问本工具的功能，用上述信息回答。`,
	"explain":   "请用清晰易懂的方式解释下面的代码：说明它的功能、关键逻辑、潜在的边界情况与改进建议。",
	"translate": "请将下面的内容翻译为自然流畅的中文；若原文是中文则翻译为英文。只输出译文，不要额外解释。",
	"summarize": "请对下面的内容进行要点总结，用简洁的中文分条列出核心信息，不要展开。",
}

// 摘要压缩参数（粗略 token 估算：约 1 token ≈ 1.6 字符）
const (
	aiTokenBudget = 3000
	aiKeepRecent  = 12
)

// ---- 流式辅助 ----

// emitAI 通过 Wails 事件向前端推送（a.app 未就绪时静默）。
// 现仅用于 AIRegenerateTitle 的单条标题更新事件；AI 问答流式已改为本地 HTTP 流式服务。
func (a *AppService) emitAI(name string, data map[string]interface{}) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit(name, data)
}

// ---- 多档案配置存储 ----

// loadAIProfiles 读取档案列表（自动从旧的单配置迁移）
func (a *AppService) loadAIProfiles() []aiProfileStored {
	out := []aiProfileStored{}
	if a.DB == nil {
		return out
	}
	raw, err := a.DB.GetSetting(aiProfilesKey)
	if err != nil || raw == "" {
		// 迁移旧的单一配置
		if lraw, e := a.DB.GetSetting(aiLegacyKey); e == nil && lraw != "" {
			var s aiProfileStored
			if json.Unmarshal([]byte(lraw), &s) == nil {
				if s.ID == "" {
					s.ID = "default"
				}
				if s.Name == "" {
					s.Name = "默认"
				}
				out = append(out, s)
				return out
			}
		}
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// loadActiveProfileID 读取当前激活的档案 ID（无效时回退到第一个）
func (a *AppService) loadActiveProfileID(profiles []aiProfileStored) string {
	if raw, e := a.DB.GetSetting(aiActiveKey); e == nil && raw != "" {
		for _, p := range profiles {
			if p.ID == raw {
				return raw
			}
		}
	}
	if len(profiles) > 0 {
		return profiles[0].ID
	}
	return ""
}

// getActiveAIProfile 返回解密后的当前激活档案；无档案时返回 (cfg, false)
func (a *AppService) getActiveAIProfile() (AIProfile, bool) {
	stored := a.loadAIProfiles()
	if len(stored) == 0 {
		return AIProfile{}, false
	}
	id := a.loadActiveProfileID(stored)
	var s aiProfileStored
	found := false
	for _, p := range stored {
		if p.ID == id {
			s = p
			found = true
			break
		}
	}
	if !found {
		s = stored[0]
	}
	cfg := AIProfile{
		ID:               s.ID,
		Name:             s.Name,
		Provider:         s.Provider,
		BaseURL:          s.BaseURL,
		Model:            s.Model,
		Temperature:      s.Temperature,
		MaxTokens:        s.MaxTokens,
		SystemPrompt:     s.SystemPrompt,
		TopP:             s.TopP,
		FrequencyPenalty: s.FrequencyPenalty,
		PresencePenalty:  s.PresencePenalty,
		ThinkingEnabled:  s.ThinkingEnabled,
	}
	if s.APIKey != "" {
		if dec, e := platform.DecryptSecret(s.APIKey); e == nil {
			cfg.APIKey = dec
		}
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = aiDefaultMax
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = aiDefaultTemp
	}
	return cfg, true
}

// AIListProfiles 列出所有档案（API Key 已解密）与当前激活项
func (a *AppService) AIListProfiles() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	stored := a.loadAIProfiles()
	active := a.loadActiveProfileID(stored)
	profiles := make([]AIProfile, 0, len(stored))
	for _, s := range stored {
		p := AIProfile{
			ID:               s.ID,
			Name:             s.Name,
			Provider:         s.Provider,
			BaseURL:          s.BaseURL,
			Model:            s.Model,
			Temperature:      s.Temperature,
			MaxTokens:        s.MaxTokens,
			SystemPrompt:     s.SystemPrompt,
			TopP:             s.TopP,
			FrequencyPenalty: s.FrequencyPenalty,
			PresencePenalty:  s.PresencePenalty,
			ThinkingEnabled:  s.ThinkingEnabled,
		}
		if s.APIKey != "" {
			if dec, e := platform.DecryptSecret(s.APIKey); e == nil {
				p.APIKey = dec
			}
		}
		if p.MaxTokens <= 0 {
			p.MaxTokens = aiDefaultMax
		}
		if p.Temperature == 0 {
			p.Temperature = aiDefaultTemp
		}
		profiles = append(profiles, p)
	}
	if active == "" && len(profiles) > 0 {
		active = profiles[0].ID
	}
	return Ok(AIProfilesResult{Active: active, Profiles: profiles})
}

// AISaveProfiles 保存完整档案列表与激活项（API Key 留空则保留原密文）
func (a *AppService) AISaveProfiles(req AISaveProfilesRequest) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	existing := map[string]aiProfileStored{}
	for _, e := range a.loadAIProfiles() {
		existing[e.ID] = e
	}
	out := make([]aiProfileStored, 0, len(req.Profiles))
	for _, p := range req.Profiles {
		id := p.ID
		if id == "" {
			id = uuid.New().String()
		}
		s := aiProfileStored{
			ID:               id,
			Name:             p.Name,
			Provider:         p.Provider,
			BaseURL:          strings.TrimRight(p.BaseURL, "/"),
			Model:            p.Model,
			Temperature:      p.Temperature,
			MaxTokens:        p.MaxTokens,
			SystemPrompt:     p.SystemPrompt,
			TopP:             p.TopP,
			FrequencyPenalty: p.FrequencyPenalty,
			PresencePenalty:  p.PresencePenalty,
			ThinkingEnabled:  p.ThinkingEnabled,
		}
		if p.APIKey == "" {
			if e, ok := existing[id]; ok {
				s.APIKey = e.APIKey
			}
		} else {
			enc, err := platform.EncryptSecret(p.APIKey)
			if err != nil {
				return Fail(err)
			}
			s.APIKey = enc
		}
		out = append(out, s)
	}
	b, _ := json.Marshal(out)
	if err := a.DB.SetSetting(aiProfilesKey, string(b)); err != nil {
		return Fail(err)
	}
	active := req.Active
	if active == "" && len(out) > 0 {
		active = out[0].ID
	}
	if err := a.DB.SetSetting(aiActiveKey, active); err != nil {
		return Fail(err)
	}
	_ = a.DB.SetSetting(aiLegacyKey, "")
	return Ok(nil)
}

// AISetActiveProfile 设置当前激活的档案（聊天中切换模型用）
func (a *AppService) AISetActiveProfile(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.SetSetting(aiActiveKey, id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// AIGetConfig 兼容旧接口：返回当前激活档案（API Key 已解密）
func (a *AppService) AIGetConfig() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok {
		return Ok(AIConfig{Provider: "openai", Temperature: aiDefaultTemp, MaxTokens: aiDefaultMax})
	}
	return Ok(AIConfig{
		Provider:    cfg.Provider,
		BaseURL:     cfg.BaseURL,
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
}

// AISetConfig 兼容旧接口：写入单个默认档案
func (a *AppService) AISetConfig(cfg AIConfig) *ApiResult {
	req := AISaveProfilesRequest{
		Active: "default",
		Profiles: []AIProfile{{
			ID:          "default",
			Name:        "默认",
			Provider:    cfg.Provider,
			BaseURL:     cfg.BaseURL,
			APIKey:      cfg.APIKey,
			Model:       cfg.Model,
			Temperature: cfg.Temperature,
			MaxTokens:   cfg.MaxTokens,
		}},
	}
	return a.AISaveProfiles(req)
}

// ---- 会话 CRUD（委托 db） ----

// AIListConversations 列出会话
func (a *AppService) AIListConversations() *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.DB.ListAIConversations()
	if err != nil {
		return Fail(err)
	}
	return Ok(list)
}

// AICreateConversation 新建会话
func (a *AppService) AICreateConversation(title string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	c, err := a.DB.CreateAIConversation(title)
	if err != nil {
		return Fail(err)
	}
	return Ok(c)
}

// AIDeleteConversation 删除会话
func (a *AppService) AIDeleteConversation(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.DeleteAIConversation(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// AIGetMessages 读取某会话的消息
func (a *AppService) AIGetMessages(convID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	msgs, err := a.DB.ListAIMessages(convID)
	if err != nil {
		return Fail(err)
	}
	return Ok(msgs)
}

// AIClearMessages 清空某会话的上下文（消息与摘要）
func (a *AppService) AIClearMessages(convID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.DB.ClearAIConversation(convID); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// AIRegenerateTitle 调用模型，根据对话内容生成短标题
func (a *AppService) AIRegenerateTitle(convID string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok || cfg.APIKey == "" {
		return FailMsg("missing api key")
	}
	msgs, err := a.DB.ListAIMessages(convID)
	if err != nil {
		return Fail(err)
	}
	if len(msgs) == 0 {
		return FailMsg("会话暂无消息")
	}
	var b strings.Builder
	b.WriteString("用不超过 15 个汉字给下面的对话起一个简洁标题，只输出标题本身，不要引号、标点或任何解释：\n\n")
	for _, m := range msgs {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	title, err := a.callAIOnce(context.Background(), cfg, []map[string]string{
		{"role": "system", "content": "你是标题生成器，输出极简。"},
		{"role": "user", "content": b.String()},
	})
	if err != nil {
		return Fail(err)
	}
	title = strings.TrimSpace(title)
	title = strings.Trim(title, "\"'\"")
	title = strings.Trim(title, "。！？.!?")
	if utf8.RuneCountInString(title) > 30 {
		title = string([]rune(title)[:30]) + "…"
	}
	if title == "" {
		return FailMsg("标题生成失败")
	}
	if err := a.DB.UpdateAIConversationMeta(convID, title, ""); err != nil {
		return Fail(err)
	}
	a.emitAI("ai:conv", map[string]interface{}{"id": convID, "title": title})
	return Ok(title)
}

// AIStreamInfo 返回本地 AI 流式服务的端口与随机令牌。
// 前端据此构造 http://127.0.0.1:<port>/ai/stream?token=<token> 并用 fetch 读取
// 分块（NDJSON）响应，实现真正的逐字流式，而非依赖 Wails 事件。
func (a *AppService) AIStreamInfo() *ApiResult {
	if a.aiStream == nil {
		return FailMsg("流式服务未启动")
	}
	return Ok(map[string]interface{}{"port": a.aiStream.port, "token": a.aiStream.token})
}

// streamAIChat 调用 OpenAI 兼容流式端点，逐段通过 onToken（content）/ onReasoning（思考过程）回调推送。
// ctx 取消时立即返回已收集内容（用于前端停止生成）。onUsage 在收到 usage 数据时调用（通常为最后一段）。
func (a *AppService) streamAIChat(ctx context.Context, cfg AIProfile, messages []map[string]string, convID string, onToken func(text string), onReasoning func(text string), onUsage func(promptTokens, completionTokens int)) (string, error) {
	body := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": cfg.Temperature,
		"stream":      true,
	}
	if cfg.MaxTokens > 0 {
		body["max_tokens"] = cfg.MaxTokens
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authKey, authVal)
	// 导致逐字流式退化成“一次性整段”。强制 identity 让上游返回原始分块 SSE。
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")

	client := a.aiHTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return sb.String(), ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Index  int `json:"index"`
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"` // Agnes/DeepSeek thinking 模式
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil && onUsage != nil {
			onUsage(chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		reasoning := chunk.Choices[0].Delta.ReasoningContent
		if content != "" {
			sb.WriteString(content)
			if onToken != nil {
				onToken(content)
			}
		}
		if reasoning != "" && onReasoning != nil {
			onReasoning(reasoning)
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), err
	}
	return sb.String(), nil
}

// buildAIMessages 组装发送给模型的消息列表，并自动执行摘要压缩
func (a *AppService) buildAIMessages(ctx context.Context, cfg AIProfile, convID, mode, currentUser string) ([]map[string]string, error) {
	hist, err := a.DB.ListAIMessages(convID)
	if err != nil {
		return nil, err
	}
	conv, err := a.DB.GetAIConversation(convID)
	if err != nil {
		return nil, err
	}

	estTokens := func(msgs []db.AIMessage) int {
		n := 0
		for _, m := range msgs {
			n += len([]rune(m.Content)) * 10 / 16
		}
		return n
	}

	// 摘要压缩：历史超阈值且足够长时，总结最旧部分并清理
	if estTokens(hist) > aiTokenBudget && len(hist) > aiKeepRecent {
		toSum := hist[:len(hist)-aiKeepRecent]
		recent := hist[len(hist)-aiKeepRecent:]
		if summary, serr := a.summarizeAndStore(ctx, cfg, conv, toSum); serr == nil {
			_ = a.DB.DeleteOldAIMessages(convID, aiKeepRecent)
			hist = recent
			conv.Summary = summary
		}
	}

	system := aiModePrompts[mode]
	if cfg.SystemPrompt != "" {
		system = cfg.SystemPrompt + "\n\n" + system
	}
	if conv.Summary != "" {
		system += "\n\n[历史对话摘要]\n" + conv.Summary
	}

	messages := []map[string]string{{"role": "system", "content": system}}
	for _, m := range hist {
		messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": currentUser})
	return messages, nil
}

// summarizeAndStore 调用模型把旧对话压缩为摘要并写回会话
func (a *AppService) summarizeAndStore(ctx context.Context, cfg AIProfile, conv *db.AIConversation, toSum []db.AIMessage) (string, error) {
	var b strings.Builder
	b.WriteString("请将以下对话压缩为简洁的中文摘要，保留关键事实、结论与待办，便于后续延续上下文。只输出摘要，不要解释：\n\n")
	for _, m := range toSum {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	msgs := []map[string]string{
		{"role": "system", "content": "你是一个压缩助手，负责把对话历史提炼为要点摘要。"},
		{"role": "user", "content": b.String()},
	}
	summary, err := a.callAIOnce(ctx, cfg, msgs)
	if err != nil {
		return "", err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("摘要为空")
	}
	combined := summary
	if conv.Summary != "" {
		combined = conv.Summary + "\n" + summary
	}
	if err := a.DB.UpdateAIConversationMeta(conv.ID, "", combined); err != nil {
		return "", err
	}
	return combined, nil
}

// callAIOnce 一次性（非流式）调用，用于摘要压缩 / 标题生成
func (a *AppService) callAIOnce(ctx context.Context, cfg AIProfile, messages []map[string]string) (string, error) {
	body := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": cfg.Temperature,
		"max_tokens":  cfg.MaxTokens,
		"stream":      false,
	}
	if cfg.TopP > 0 {
		body["top_p"] = cfg.TopP
	}
	if cfg.FrequencyPenalty != 0 {
		body["frequency_penalty"] = cfg.FrequencyPenalty
	}
	if cfg.PresencePenalty != 0 {
		body["presence_penalty"] = cfg.PresencePenalty
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	ep, authKey, authVal := apiEndpoint(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authKey, authVal)

	client := a.aiHTTPClient
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(eb)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("无返回内容")
	}
	return out.Choices[0].Message.Content, nil
}

// callAIStream 已由 streamAIChat 取代（回调式，供本地 HTTP 流式服务逐段写出 NDJSON）。

// AITestConnection 发送一条简单的用户消息验证 API Key 和模型是否可用。
// 使用非流式单次调用，超时 15 秒。
func (a *AppService) AITestConnection(profileID string) (map[string]interface{}, error) {
	stored := a.loadAIProfiles()
	if len(stored) == 0 {
		return map[string]interface{}{"success": false, "message": "无档案"}, nil
	}
	var s *aiProfileStored
	for i := range stored {
		if stored[i].ID == profileID {
			s = &stored[i]
			break
		}
	}
	if s == nil {
		return map[string]interface{}{"success": false, "message": "Profile not found"}, nil
	}
	apiKey := s.APIKey
	if apiKey != "" {
		if dec, e := platform.DecryptSecret(apiKey); e == nil {
			apiKey = dec
		}
	}
	if apiKey == "" {
		return map[string]interface{}{"success": false, "message": "API Key 为空"}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	body := map[string]interface{}{
		"model":      s.Model,
		"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
		"stream":     false,
		"max_tokens": 50,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return map[string]interface{}{"success": false, "message": "请求构建失败: " + err.Error()}, nil
	}
	// 用 s 构造临时 AIProfile 以复用 apiEndpoint
	tmpCfg := AIProfile{
		Provider: s.Provider,
		BaseURL:  s.BaseURL,
		Model:    s.Model,
		APIKey:   apiKey,
	}
	ep, authKey, authVal := apiEndpoint(tmpCfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(raw))
	if err != nil {
		return map[string]interface{}{"success": false, "message": "请求创建失败: " + err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(authKey, authVal)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]interface{}{"success": false, "message": "网络错误: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return map[string]interface{}{"success": false, "message": msg}, nil
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return map[string]interface{}{"success": false, "message": "响应解析失败: " + err.Error()}, nil
	}
	if len(result.Choices) == 0 {
		return map[string]interface{}{"success": false, "message": "模型无返回"}, nil
	}
	return map[string]interface{}{"success": true, "message": "✅ 连接成功，模型回复: " + result.Choices[0].Message.Content}, nil
}