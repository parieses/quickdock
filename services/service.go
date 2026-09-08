package services

import (
	"sync"
	"sync/atomic"

	"quickdock/internal/db"
	dshcore "quickdock/internal/dsh"
	"quickdock/internal/env"
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
	GetNoteWindow      func() *application.WebviewWindow

	// 状态标志（注入 main 包的 atomic.Bool 指针，共享状态）
	WindowVisible *atomic.Bool
	ClipboardMode *atomic.Bool
	PaletteMode   *atomic.Bool
	NoteMode      *atomic.Bool

	// main 包注入的回调（避免循环依赖）
	StartHotkeyListenerFn func(app *application.App, svc *AppService)
	SuspendHotkeysFn      func()
	ResumeHotkeysFn       func()
	// RegisterPluginHostFn 注入插件 Host API 注册（实现在 services/plugin 包，避免循环依赖）
	RegisterPluginHostFn func()

	// 标记"这是一次真退出"，令主窗口的 WindowClosing 钩子放行而不是取消关闭并隐藏到托盘。
	// 更新时（RestartApp 拉起安装器 → app.Quit）必须先调用，否则 cleanup() 里的
	// window.Close() 会被钩子 event.Cancel() 掉，窗口不销毁、进程退出不干净。
	PrepareQuitFn func()

	// 插件管理器
	PluginMgr     *plugin.Manager
	PluginHotkeys *PluginHotkeyRegistry
	PluginsDir    string // 插件根目录（用于定位 common.css 等共享资源）

	// 插件窗口管理器（每个插件独立窗口）
	PluginWindowMgr *plugin.PluginWindowManager

	// 内置插件自动安装（由 main.go 注入，含 embed.FS）
	InstallBuiltinPluginsFn func(mgr *plugin.Manager, database *db.Database)

	// 应用版本号（由 main.go 在编译时通过 -ldflags -X 注入）
	AppVersion string

	// 系统通知服务（由 main.go 创建并注入，用于待办定时提醒）
	Notifier *notifications.NotificationService

	// 调度器唤醒通道（任务增删改时立即重排，避免空轮询/延迟触发）
	schedWake   chan struct{} // 定时任务调度器
	monitorWake chan struct{} // 网站监控检查器

	// 调度器退出通道：ServiceShutdown 时关闭，令三个常驻 goroutine 干净退出，
	// 避免 DB 已关闭后它们仍触发并访问导致 use-after-close panic。
	schedulerQuit     chan struct{}
	schedulerQuitOnce sync.Once

	// 监控在检标记：防止同一监控并发检测（慢探针未更新 last_checked_ts 时被重复选为待检）
	// 导致宕机时重复发送通知。LoadOrStore 原子保证同一时刻仅一个检查协程通过。
	monitorInflight sync.Map // monitorID -> struct{}

	// 定时任务在运行标记：防止慢任务（http 阻塞 / command·open 挂起）重排前被重复选中并发执行。
	// 与 monitorInflight 同款 LoadOrStore 模式。
	schedInflight sync.Map // taskID -> struct{}

	// DeepSeek Harness 运行环境（检测/下载便携 Node + 安装 dsh）与进程管理
	NodeEnv *dshcore.NodeEnvManager
	DSH     *dshcore.DSHProcessManager

	// 环境管理：Node/PHP/Go/Redis/Nginx 便携运行时（参考 FlyEnv 的部署与版本切换）
	Env *env.Manager
}

// App 返回底层 Wails 应用实例（供拆分到子包的服务通过回指访问未导出字段）。
func (a *AppService) App() *application.App {
	return a.app
}

// NewAppService 创建应用服务实例
func NewAppService() *AppService {
	nodeEnv := dshcore.NewNodeEnvManager()
	return &AppService{
		NodeEnv: nodeEnv,
		DSH:     dshcore.NewDSHProcessManager(nil, nodeEnv),
		Env:     env.NewManager(),
	}
}

// SetApp 设置 App 引用（由 main.go 在创建后调用）
func (a *AppService) SetApp(app *application.App) {
	a.app = app
	a.NodeEnv.SetApp(app)
	a.DSH.SetApp(app)
	// 启动后台扫描 PATH / 便携目录并重新保存检测结果（触发点之一：自动扫描 PATH），
	// 完成后通知前端刷新——首次运行无缓存、或应用关闭期间系统 PATH 变化时在此对齐。
	if a.Env != nil {
		a.Env.RefreshAllAsync(func() {
			app.Event.Emit("quickdock:env:refreshed")
		})
	}
}

// dshAutoStartEnabled 读取 dsh web 自动启动配置（默认开启）。
// 供 lifecycle.go 启动时判断是否延迟拉起 dsh；键与门面 DSHService 共用引擎常量。
func (a *AppService) dshAutoStartEnabled() bool {
	if a.DB == nil {
		return true
	}
	v, err := a.DB.GetValue(dshcore.AutoStartKey)
	if err != nil || v == "" {
		return true
	}
	return v == "1"
}
