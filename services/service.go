package services

import (
	"sync/atomic"

	"quickdock/internal/db"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const DefaultWorkspaceName = "默认工作空间"

// AppService 应用服务 — 所有 Wails 前端绑定方法集中在此
type AppService struct {
	app *application.App
	DB  *db.Database

	// 窗口引用（由 main.go 在创建窗口后设置）
	MainWindow      *application.WebviewWindow
	ClipboardWindow *application.WebviewWindow
	PaletteWindow   *application.WebviewWindow

	// 状态标志（注入 main 包的 atomic.Bool 指针，共享状态）
	WindowVisible *atomic.Bool
	ClipboardMode *atomic.Bool
	PaletteMode   *atomic.Bool

	// 隐藏窗口 HWND（给剪贴板系统 API 用）
	HiddenHWND atomic.Uint64

	// main 包注入的回调（避免循环依赖）
	StartHotkeyListenerFn func(app *application.App, svc *AppService)
	SuspendHotkeysFn      func()
	ResumeHotkeysFn       func()
}

// NewAppService 创建应用服务实例
func NewAppService() *AppService {
	return &AppService{}
}

// SetApp 设置 App 引用（由 main.go 在创建后调用）
func (a *AppService) SetApp(app *application.App) {
	a.app = app
}
