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
}

// NewAIService 创建 AI 门面服务
func NewAIService(app *services.AppService) *AIService {
	return &AIService{
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
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本，跨包不可访问宿主私有方法）
func (a *AIService) dbOK() *services.ApiResult {
	if a.App.DB == nil {
		logger.E("QuickDock: database not initialized")
		return services.FailMsg("database not initialized")
	}
	return nil
}

// recoverPanic 兜底 recover 打日志（原宿主包级函数，跨包不可见故包内复刻；宿主保留同名函数）
func recoverPanic(context string) {
	if r := recover(); r != nil {
		logger.E("QuickDock: [PANIC] %s: %v", context, r)
	}
}
