package services

import (
	"context"
	"encoding/json"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

// aiProfileStored 落库结构（API Key 为密文）。
// 与 AIProfile 字段完全一致，为免重复维护直接用类型别名；区别仅在语义上——
// AIProfile.APIKey 在 API 层是明文/掩码，落库时写入的是 DPAPI 密文。
type aiProfileStored = AIProfile

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

// apiEndpoint 根据 provider 构建正确的 API URL 和认证头。
func apiEndpoint(cfg AIProfile) (url string, authKey, authVal string) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Provider == "azure" {
		ep := base + "/openai/deployments/" + cfg.Model + "/chat/completions?api-version=2024-02-15-preview"
		return ep, "api-key", cfg.APIKey
	}
	return base + "/chat/completions", "Authorization", "Bearer " + cfg.APIKey
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

// getActiveAIProfile 返回解密后的当前激活档案；无档案时返回 (cfg, false)。
// 结果带内存缓存（聊天高频调用时避免反复读库 + DPAPI 解密）；保存档案会失效。
func (a *AppService) getActiveAIProfile() (AIProfile, bool) {
	a.aiCacheMu.RLock()
	ok := a.aiCachedOK
	cfg := a.aiCachedCfg
	a.aiCacheMu.RUnlock()
	if ok {
		return cfg, true
	}

	stored := a.loadAIProfiles()
	if len(stored) == 0 {
		return AIProfile{}, false
	}
	id := a.loadActiveProfileID(stored)
	var s AIProfile
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
	cfg = AIProfile{
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
	a.aiCacheMu.Lock()
	a.aiCachedCfg = cfg
	a.aiCachedOK = true
	a.aiCacheMu.Unlock()
	return cfg, true
}

// invalidateAICache 使激活档案缓存失效（保存/删除/切换档案后调用）。
func (a *AppService) invalidateAICache() {
	a.aiCacheMu.Lock()
	a.aiCachedOK = false
	a.aiCacheMu.Unlock()
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
				// 只回填掩码，明文 Key 不暴露给前端 WebView（防注入脚本窃取）
				p.APIKey = maskAPIKey(dec)
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
		} else if strings.Contains(p.APIKey, "***") {
			// 前端回传的是掩码（AIListProfiles 返回的形态），不是新 Key：保留原密文
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
	a.invalidateAICache()
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
	a.invalidateAICache()
	return Ok(nil)
}

// AIGetConfig 兼容旧接口：返回当前激活档案（API Key 以掩码形式返回）
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
		APIKey:      maskAPIKey(cfg.APIKey),
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
}

// maskAPIKey 将 API Key 掩码为 "前4***后4"（短 Key 全掩码）。
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "******"
	}
	return key[:4] + "***" + key[len(key)-4:]
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
func (a *AppService) emitAI(name string, data map[string]interface{}) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit(name, data)
}

// AIStreamInfo 返回本地 AI 流式服务的端口与随机令牌。
func (a *AppService) AIStreamInfo() *ApiResult {
	if a.aiStream == nil {
		return FailMsg("流式服务未启动")
	}
	return Ok(map[string]interface{}{"port": a.aiStream.port, "token": a.aiStream.token})
}
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

	resp, err := a.aiHTTPClient.Do(req)
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