package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"quickdock/internal/platform"
	"quickdock/services"

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

// apiEndpoint 根据 provider 构建正确的 API URL 和认证头。
func apiEndpoint(cfg AIProfile) (url string, authKey, authVal string) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Provider == "azure" {
		ep := base + "/openai/deployments/" + cfg.Model + "/chat/completions?api-version=2024-02-15-preview"
		return ep, "api-key", cfg.APIKey
	}
	// 无 API Key 时不产出空 "Bearer "（本地 Ollama / 免鉴权自建网关属正常场景）。
	auth := ""
	if cfg.APIKey != "" {
		auth = "Bearer " + cfg.APIKey
	}
	return base + "/chat/completions", "Authorization", auth
}

// ---- 多档案配置存储 ----

// loadAIProfiles 读取档案列表（自动从旧的单配置迁移）
func (a *AIService) loadAIProfiles() []aiProfileStored {
	out := []aiProfileStored{}
	if a.App.DB == nil {
		return out
	}
	raw, err := a.App.DB.GetSetting(aiProfilesKey)
	if err != nil || raw == "" {
		// 迁移旧的单一配置
		if lraw, e := a.App.DB.GetSetting(aiLegacyKey); e == nil && lraw != "" {
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
func (a *AIService) loadActiveProfileID(profiles []aiProfileStored) string {
	if raw, e := a.App.DB.GetSetting(aiActiveKey); e == nil && raw != "" {
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
func (a *AIService) getActiveAIProfile() (AIProfile, bool) {
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
		dec, e := platform.DecryptSecret(s.APIKey)
		if e != nil {
			// DPAPI 解密失败（如凭据被其他用户/机器替换）：不缓存空 Key 配置，
			// 下次调用会重新解密重试，而不是永远拿到空 Key 直到用户手动保存档案。
			return AIProfile{}, false
		}
		cfg.APIKey = dec
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
func (a *AIService) invalidateAICache() {
	a.aiCacheMu.Lock()
	a.aiCachedOK = false
	a.aiCacheMu.Unlock()
}

// AIListProfiles 列出所有档案（API Key 已解密）与当前激活项
func (a *AIService) AIListProfiles() *services.ApiResult {
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
	return services.Ok(AIProfilesResult{Active: active, Profiles: profiles})
}

// AISaveProfiles 保存完整档案列表与激活项（API Key 留空则保留原密文）
func (a *AIService) AISaveProfiles(req AISaveProfilesRequest) *services.ApiResult {
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
		} else if e, ok := existing[id]; ok && p.APIKey == e.APIKey {
			// 传入的密文与已存储的完全一致（档案更新时原样回传未改动 Key），
			// 直接保留，避免对已有密文二次加密导致无法解密（AISetConfig 合并其它档案时触发）。
			s.APIKey = e.APIKey
		} else {
			enc, err := platform.EncryptSecret(p.APIKey)
			if err != nil {
				return services.Fail(err)
			}
			s.APIKey = enc
		}
		out = append(out, s)
	}
	b, _ := json.Marshal(out)
	if err := a.App.DB.SetSetting(aiProfilesKey, string(b)); err != nil {
		return services.Fail(err)
	}
	active := req.Active
	if active == "" && len(out) > 0 {
		active = out[0].ID
	}
	if err := a.App.DB.SetSetting(aiActiveKey, active); err != nil {
		return services.Fail(err)
	}
	_ = a.App.DB.SetSetting(aiLegacyKey, "")
	a.invalidateAICache()
	return services.Ok(nil)
}

// AISetActiveProfile 设置当前激活的档案（聊天中切换模型用）
func (a *AIService) AISetActiveProfile(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.SetSetting(aiActiveKey, id); err != nil {
		return services.Fail(err)
	}
	a.invalidateAICache()
	return services.Ok(nil)
}

// AIGetConfig 兼容旧接口：返回当前激活档案（API Key 以掩码形式返回）
func (a *AIService) AIGetConfig() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	cfg, ok := a.getActiveAIProfile()
	if !ok {
		return services.Ok(AIConfig{Provider: "openai", Temperature: aiDefaultTemp, MaxTokens: aiDefaultMax})
	}
	return services.Ok(AIConfig{
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

// AISetConfig 兼容旧接口：写入/更新单个默认档案。
// 必须合并进现有档案列表，不能整体覆盖——否则会静默删除用户已有的其它 AI 档案。
func (a *AIService) AISetConfig(cfg AIConfig) *services.ApiResult {
	profiles := a.loadAIProfiles()
	idx := -1
	for i := range profiles {
		if profiles[i].ID == "default" {
			idx = i
			break
		}
	}
	if idx >= 0 {
		// 仅覆盖非空字段，避免把已有档案清空（Key 留空表示不修改，由 AISaveProfiles 保留原密文）
		cur := profiles[idx]
		if cfg.Provider != "" {
			cur.Provider = cfg.Provider
		}
		if cfg.BaseURL != "" {
			cur.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
		}
		if cfg.APIKey != "" {
			cur.APIKey = cfg.APIKey
		}
		if cfg.Model != "" {
			cur.Model = cfg.Model
		}
		if cfg.Temperature != 0 {
			cur.Temperature = cfg.Temperature
		}
		if cfg.MaxTokens != 0 {
			cur.MaxTokens = cfg.MaxTokens
		}
		profiles[idx] = cur
	} else {
		profiles = append(profiles, AIProfile{
			ID:          "default",
			Name:        "默认",
			Provider:    cfg.Provider,
			BaseURL:     strings.TrimRight(cfg.BaseURL, "/"),
			APIKey:      cfg.APIKey,
			Model:       cfg.Model,
			Temperature: cfg.Temperature,
			MaxTokens:   cfg.MaxTokens,
		})
	}
	active := "default"
	if cur := a.loadActiveProfileID(profiles); cur != "" {
		active = cur
	}
	return a.AISaveProfiles(AISaveProfilesRequest{Active: active, Profiles: profiles})
}

// aiModePrompts 五种模式的 system prompt（仅切换提示，不另写接口）
// chat 只做通用助手；功能清单与操作入口在 tutorial（「使用教程」模式）中注入。
var aiModePrompts = map[string]string{
	"chat": "你是 QuickDock 的 AI 助手，回答简洁准确。",
	"tutorial": `你是 QuickDock（快启坞） 的使用教程助手。用户切到「使用教程」模式，就是想学会「某个功能具体怎么操作」。你只讲这个软件自己的用法，用中文、按步骤教学。当前仅支持 Windows 10 1809+ / Windows 11。

下面是本软件的功能与入口，回答时据此给出可照做的操作路径：

【主界面】Ctrl+Space 显示 / 隐藏主窗口。左侧边栏按职责分组：工作空间 / 场景 / 集合 / 项目；环境管理（语言 / 网络服务 / 数据库 / 中间件 / AI 服务 / 开发工具）；以及待办、定时任务、网站监控、笔记、端口全景、插件管理等页面。
【工作空间管理】四层结构：工作空间 → 场景 → 集合 → 项目。项目支持六种类型：目录 / 文件 / 网页链接 / 终端命令 / 应用程序 / 快速链接，网页链接可直接粘贴 URL 保存为项目。全层级支持拖拽排序，可用全文搜索快速定位。
【剪贴板历史】Ctrl+反引号（键盘左上角 ~ 键）唤出。自动记录文本 / 图片 / 文件，可固定、搜索、复制、批量删除；保留天数可配置，过期自动清理。
【命令面板】Ctrl+K 唤出。全局搜索工作空间 / 场景 / 集合 / 项目并直接执行，支持键盘导航与多选批量操作，另有「最近使用 / 最常使用」列表；也可用来执行「区域截图」等系统命令。
【笔记】Ctrl+Shift+N 唤出快捷笔记浮窗，自动保存；完整笔记库支持文件夹 + Markdown 文档的树形多层级组织，可搜索、重命名、删除。
【变量占位符】项目的值（命令 / 网址 / 快速链接）支持 {date} / {time} / {username} / {clipboard} 占位符，打开或执行时自动替换为当前日期 / 时间 / Windows 用户名 / 剪贴板内容。
【待办】列表视图 + 看板三列（待办 / 进行中 / 已完成）；支持子任务、标签、重复待办（每日 / 每周 / 每月）、到期系统通知；番茄专注在待办页启动倒计时，结束发系统通知并可推送 Webhook。
【定时任务】五种调度（一次性 / 间隔 / 每天 / 每周 / 每月）× 五种动作（打开软件 / 目录 / 网页 / 命令 / HTTP 请求），支持手动立即执行。
【网站监控】添加要监控的网址，选择 GET / HEAD / POST，可配 SSL 证书到期提前预警天数与关键字 / 正则内容匹配；提供在线率统计、检测日志、响应时间趋势图（24h / 7d / 全部），状态翻转时可发桌面通知与 Webhook。
【截图工具】F1 唤起全屏覆盖层（F1 被占用时自动回退 Ctrl+Shift+A）。框选松手后选区下方浮出工具条：矩形 / 椭圆 / 箭头 / 直线 / 马赛克 / 文字，可调颜色（9 色）、线宽（4 档）、字号（6 档）；鼠标划过窗口会高亮并显示尺寸，单击选中整个窗口，按住 Ctrl 下钻到子控件；已画的图形可点选、拖动、Delete 删除，文字可双击重新编辑（清空即删除）；最后按 Enter / Ctrl+C 复制、Ctrl+S 保存、或点「贴图」把截图钉在屏幕最上层（可拖动、滚轮缩放、双击关闭）。Esc 逐层退出。
【AI 助手】先在 AI 设置里添加一个 API 配置档案（支持 OpenAI / DeepSeek / Kimi / 通义千问 / Ollama / Azure OpenAI / 自定义兼容接口），填好 API Key 与模型后点「测试连接」验证可用，再回到 AI 页面开始对话；输出为流式，模型思考过程折叠展示，支持多会话、重新生成标题、Token 用量统计，API Key 加密存储。
【DeepSeek Harness】在设置页的 DSH 入口一键装填环境（自动检测 node / npx / dsh，缺失时下载便携 Node 并安装），随后以原生窗口打开 Agent 编程界面；服务后台常驻，关窗不停，真正停止在设置里手动操作。
【环境管理】进入环境管理页面，运行时按「语言 / 网络服务 / 数据库 / 中间件 / AI 服务 / 开发工具 / 内置工具」分组；点运行时名称加载版本列表并选择版本安装，装好后可切换当前激活版本（写入环境变量，不污染系统 PATH）、一键启动 / 停止 / 重启服务；服务型运行时还提供「编辑配置」（启动前会做配置校验）、「查看日志」、「打开控制台」；顶栏「端口全景」按钮在弹出的面板里汇总所有运行中服务的端口与入口。Ollama 在页面内可拉取 / 删除模型。
【本地开发站点】入口在环境管理页的「内置工具」组 →「站点」：给本地项目绑定自定义域名 + 自动 HTTPS。需先装好 Nginx 或 Caddy，对外服务交给它们（https 443，http 80 跳转）；QuickDock 只负责签发证书、写 hosts 标记区块、生成站点配置片段并触发热重载，绝不改写你的 nginx.conf / Caddyfile。同组还有「HTTP 服务」，用于把某个目录临时挂成 HTTP 服务。
【插件】在「插件管理」页的「在线市场」标签安装 / 升级官方插件，装完可启用 / 禁用 / 绑定热键，也能在独立窗口中打开插件界面；插件分纯前端、内嵌 JS 引擎（goja）、独立子进程（native）三种运行方式。
【MCP 服务】QuickDock 自身暴露 MCP 服务在 http://127.0.0.1:9230/mcp，供外部 AI 客户端接入后调用本地能力：查环境、启停服务、搜项目、建待办、搜笔记、看剪贴板与端口、读日志等。随主进程启停，QuickDock 退出即失效。
【同步与备份】WebDAV 可做全量 JSON 备份 / 恢复并保留多版本；快照可一键导出全部数据为 JSON 文件，用于迁移或重装恢复。
【其他】全局热键全部可在设置页自定义；系统命令支持锁屏 / 关机 / 重启 / 睡眠 / 清空回收站；应用支持免安装器就地自动更新。数据默认存放在 ~/.quickdock。

教学要求：
1. 先给「入口」（哪个页面 / 按哪个热键 / 点哪个按钮），再给分步操作，最后给预期结果；用有序列表，一步一句，不要跳步。
2. 假设用户是第一次使用，不要用「显然」「直接」这类省略关键步骤的说法。
3. 用户只说了功能名、没说具体目标时，先用一两句话概括它能做什么，再问他具体想实现什么，然后给步骤。
4. 涉及插件数量、运行时数量、MCP 工具数量这类会随版本变化的具体数字时，不要给出确定数字，说明以应用内「插件管理 / 环境管理」页面显示为准。
5. 本模式只讲 QuickDock 自身的用法；与软件无关的通用技术问题简要回答即可，不要长篇展开。
6. 不确定的功能不要编造，直接说明。
7. 回答用中文，简洁不啰嗦，可用 Markdown 加粗关键词、用列表分步。`,
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

// emitAI 通过 Wails 事件向前端推送（a.App.App() 未就绪时静默）。
func (a *AIService) emitAI(name string, data map[string]interface{}) {
	if a.App.App() == nil {
		return
	}
	a.App.App().Event.Emit(name, data)
}

// AIStreamInfo 返回本地 AI 流式服务的端口与随机令牌。
func (a *AIService) AIStreamInfo() *services.ApiResult {
	a.aiStreamMu.Lock()
	s := a.aiStream
	a.aiStreamMu.Unlock()
	if s == nil {
		return services.FailMsg("流式服务未启动")
	}
	return services.Ok(map[string]interface{}{"port": s.port, "token": s.token})
}
func (a *AIService) AITestConnection(profileID string) (map[string]interface{}, error) {
	stored := a.loadAIProfiles()
	if len(stored) == 0 {
		return nil, fmt.Errorf("无档案")
	}
	var s *aiProfileStored
	for i := range stored {
		if stored[i].ID == profileID {
			s = &stored[i]
			break
		}
	}
	if s == nil {
		return nil, fmt.Errorf("Profile not found")
	}
	apiKey := s.APIKey
	if apiKey != "" {
		dec, e := platform.DecryptSecret(apiKey)
		if e != nil {
			// 解密失败绝不能再把密文当 Bearer 发出去（会泄露加密 blob 且误导为 401），
			// 必须直接报错让用户重新输入。
			return nil, fmt.Errorf("API Key 解密失败，请重新输入: %w", e)
		}
		apiKey = dec
	}
	if apiKey == "" && s.Provider != "ollama" {
		return nil, fmt.Errorf("API Key 为空")
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
		return nil, fmt.Errorf("请求构建失败: %w", err)
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
		return nil, fmt.Errorf("请求创建失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// 本地 Ollama 无需鉴权，留空 API Key 时不要发空 Bearer（部分网关会因空 token 直接 401）。
	if authVal != "" {
		req.Header.Set(authKey, authVal)
	}
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := a.aiHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("网络错误: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))
		if r := []rune(msg); len(r) > 200 {
			msg = string(r[:200])
		}
		return nil, fmt.Errorf("%s", msg)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("响应解析失败: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("模型无返回")
	}
	return map[string]interface{}{"success": true, "message": "✅ 连接成功，模型回复: " + result.Choices[0].Message.Content}, nil
}
