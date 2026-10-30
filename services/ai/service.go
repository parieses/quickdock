// Package ai 承载原 AppService 的 AI 领域方法（档案/对话/聊天/本地流式服务）。
// AI 逻辑纯 DB + platform 依赖，无独立引擎可下沉，门面即实现：
// 通过 App 回指宿主 AppService 访问共享 DB 与应用实例，services 不反向 import 本包。
package ai

import (
	"net/http"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/services"
	aitools "quickdock/services/ai/tools"
)

// AIService 承载 AI 领域方法。App 回指宿主 AppService；
// AI 专用运行状态（流式服务/档案缓存/HTTP 客户端）随拆分下沉到本包。
type AIService struct {
	App *services.AppService

	// 本地 AI 流式服务（127.0.0.1 随机端口，前端 fetch 读取分块响应）
	// aiStreamMu 保护 aiStream 字段（启动/停止/查询可并发调用）
	aiStreamMu sync.Mutex
	aiStream   *aiStreamServer

	// 共享 HTTP 客户端（连接复用，避免每次 AI 请求新建 TLS 握手）
	aiHTTPClient *http.Client

	// 当前激活 AI 档案缓存：聊天流式请求会高频调用 getActiveAIProfile（读库+DPAPI 解密）。
	// 保存档案时通过 invalidateAICache 失效。mu 保护可重入。
	aiCacheMu   sync.RWMutex
	aiCachedCfg AIProfile
	aiCachedOK  bool

	// 会话 → 审批通道索引（见 ai_approval.go）。
	//
	// 用一张 map 而不是单个字段：前台可能同时开着几条对话，各自都有待确认的工具调用，
	// 单字段必然串台（「点 A 的允许、放下了 B 的操作」）。RunAgent 进入时注册、
	// 退出时注销，漏了注销就是每问一次泄漏一个 gate。
	gatesMu sync.Mutex
	gates   map[string]*approvalGate
}

// NewAIService 创建 AI 门面服务
func NewAIService(app *services.AppService) *AIService {
	s := &AIService{
		App: app,
		aiHTTPClient: &http.Client{
			// 不设总超时：流式响应可能持续数分钟，客户端总超时会在读取 body 中途强杀连接，
			// 导致已流出的内容丢失（流式调用改由各自 ctx 控制生命周期）。
			// 仅用 Transport 级别的响应头超时防御「连接成功但永不返回首字节」的挂死。
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
	// agent 工具在这里注册而不是在 services/mcp：那批能力（抓网页、读目录、看 git、
	// 改文件、跑命令）不属于 QuickDock 对外提供的 MCP 能力，只有内置 AI 用得上。
	// 注册到 mcpsrv.Agent 后，环境页的工具面板仍能看到它们并单独开关。
	if app != nil {
		aitools.Register(aitools.Deps{DB: app.DB, Env: app.Env})
		// spawn_agent 也走同一个注册表（同样能在面板里关掉），但实现在本包：
		// 它要用 s.RunAgent 跑子 agent，还要拿 ctx 里带的编排作用域，
		// 这两样都只有 AIService 实例才有。
		s.registerSpawnTool()
	}
	return s
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本，跨包不可访问宿主私有方法）
func (a *AIService) dbOK() *services.ApiResult {
	return services.CheckDB(a.App.DB)
}

// recoverPanic 兜底 recover 打日志（委托给 logger.RecoverPanic）
func recoverPanic(context string) {
	logger.RecoverPanic(context)
}
