package services

import (
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/plugin"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

const DefaultWorkspaceName = "默认工作空间"

// AppService 应用服务 — 所有 Wails 前端绑定方法集中在此
type AppService struct {
	app *application.App
	DB  *db.Database

	// 主窗口引用（由 main.go 在创建窗口后设置）
	MainWindow *application.WebviewWindow

	// 次要窗口延迟创建（由 main.go 注入工厂函数，避免启动时创建所有 WebView2 实例）
	GetClipboardWindow func() *application.WebviewWindow
	GetPaletteWindow   func() *application.WebviewWindow

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

	// 插件管理器
	PluginMgr     *plugin.Manager
	PluginHotkeys *PluginHotkeyRegistry
	PluginsDir    string // 插件根目录（用于定位 common.css 等共享资源）

	// 插件窗口管理器（每个插件独立窗口）
	PluginWindowMgr *plugin.PluginWindowManager

	// 内置插件自动安装（由 main.go 注入，含 embed.FS）
	InstallBuiltinPluginsFn func(mgr *plugin.Manager, database *db.Database)

	// 系统通知服务（由 main.go 创建并注入，用于待办定时提醒）
	Notifier *notifications.NotificationService

	// 调度器唤醒通道（任务增删改时立即重排，避免空轮询/延迟触发）
	schedWake   chan struct{} // 定时任务调度器
	monitorWake chan struct{} // 网站监控检查器

	// 插件前端页面 HTML 缓存
	frontendCache   map[string]*frontendCacheEntry
	frontendCacheMu sync.RWMutex

	// 跨窗口传递：命令面板→插件窗口的初始计算文本 + 命中的子命令
	pendingInitText    string
	pendingInitCommand string
	pendingInitTextMu  sync.Mutex
}

type frontendCacheEntry struct {
	html        string
	htmlMtime   time.Time
	commonMtime time.Time // common.css 修改时间，变化时全部失效
}

// NewAppService 创建应用服务实例
func NewAppService() *AppService {
	return &AppService{
		frontendCache: make(map[string]*frontendCacheEntry),
	}
}

// SetApp 设置 App 引用（由 main.go 在创建后调用）
func (a *AppService) SetApp(app *application.App) {
	a.app = app
}
