// Package clipboard 承载原 AppService 的剪贴板历史领域方法（CRUD/复制/粘贴/窗口隐藏/热键控制）。
// 宿主保留系统剪贴板监听层 clipboard_sys.go（AppRef/SetClipboardText/recoverPanic 被
// snippet/lifecycle/monitor 等主包文件共用）与全部状态注入字段（导出，经 App 回指访问）。
package clipboard

import (
	"quickdock/internal/logger"
	"quickdock/services"
)

// ClipboardService 承载剪贴板历史领域方法。
// App 回指宿主 AppService：DB 与 MainWindow/ClipboardMode/GetClipboardWindow 等
// 均为宿主导出字段/注入回调，本包仅经 App 读取，不反向 import 宿主逻辑。
type ClipboardService struct {
	App *services.AppService
}

// NewClipboardService 创建剪贴板门面服务
func NewClipboardService(app *services.AppService) *ClipboardService {
	return &ClipboardService{App: app}
}

// dbOK 检查宿主 DB 是否就绪（原 AppService.dbOK 的包内副本，跨包不可访问宿主私有方法）
func (a *ClipboardService) dbOK() *services.ApiResult {
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
