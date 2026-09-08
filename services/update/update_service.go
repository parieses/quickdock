// Package update 承载原 AppService 的自动更新领域方法（检测/下载/重启/后台检查）。
// 宿主仅保留 AppVersion/PrepareQuitFn/Notifier 注入字段（其它领域共用），
// 本包经 App 回指访问；更新检查缓存（lastUpdateCheck/锁）随拆分下沉到本包。
package update

import (
	"sync"

	"quickdock/internal/logger"
	"quickdock/services"
)

// UpdateService 承载自动更新领域方法。
type UpdateService struct {
	App *services.AppService

	// 更新检查：最近一次检测结果缓存（供 GetUpdateState 回填版本号与更新说明，
	// 因为 Wails Updater 不对外暴露 pending release）。updateCheckMu 串行化
	// Check，避免手动"检测更新"与后台定时检查并发探测。
	lastUpdateCheck   *UpdateStatus
	lastUpdateCheckMu sync.RWMutex
	updateCheckMu     sync.Mutex
}

// NewUpdateService 创建更新门面服务
func NewUpdateService(app *services.AppService) *UpdateService {
	return &UpdateService{App: app}
}

// recoverPanic 兜底 recover 打日志（原宿主包级函数，跨包不可见故包内复刻；宿主保留同名函数）
func recoverPanic(context string) {
	if r := recover(); r != nil {
		logger.E("QuickDock: [PANIC] %s: %v", context, r)
	}
}
