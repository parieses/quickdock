package services

import (
	"context"
	"sync"
	"sync/atomic"

	"quickdock/internal/db"
	dshcore "quickdock/internal/dsh"
	"quickdock/internal/env"
	"quickdock/internal/logger"
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
	GetWinmgrWindow    func() *application.WebviewWindow
	GetSshWindow       func() *application.WebviewWindow

	// Flags 主窗口与各浮窗的可见/模式标志（见 WindowFlags）。
	// 由 NewAppService 创建，main 包持有同一实例——不再是 4 个跨包注入的裸指针，
	// 故使用点无需判 nil。
	Flags *WindowFlags

	// main 包注入的回调（避免循环依赖）
	StartHotkeyListenerFn func(app *application.App, svc *AppService)
	SuspendHotkeysFn      func()
	ResumeHotkeysFn       func()
	// RegisterPluginHostFn 注入插件 Host API 注册（实现在 services/plugin 包，避免循环依赖）
	RegisterPluginHostFn func()

	// AskAIFn 注入「一次性 AI 问答」能力（实现在 services/ai 包，避免循环依赖）。
	//
	// 为什么是函数而不是 *ai.AIService 指针：services/ai 反过来 import services
	// （门面需要 ApiResult 与 App 回指），若这里持具体类型就构成环。
	// 函数签名是唯一能同时被 sshx 与 plugin 复用、又不必 import ai 包的形状。
	//
	// 语义：传入 system 与 question，返回模型正文与所用模型名。
	// 由 main.go 在创建 aiSvc 之后注入；未注入时调用方须判 nil（插件无 AI 配置、
	// AI 服务构造失败等）。
	AskAIFn func(system, question string, timeoutSec int) (text string, model string, err error)

	// AskAIFnStream 与 AskAIFn 同源，但 onToken 逐段回调增量文本，供前端流式渲染。
	// 调用方（SSH / 剪贴板面板）在 onToken 里把增量经 Wails 事件推给前端即可。
	// onToken 允许为 nil（如插件这类拿不到前端通道的调用方，退化成一次性返回）。
	AskAIFnStream func(system, question string, timeoutSec int, onToken func(text string)) (text string, model string, err error)

	// AIAvailableFn 报告当前是否配置了可用的 AI 档案（供插件在调用 host.ai.ask 前预检，
	// 避免「盲目调用 → 拿到未配置/不可用 的裸错」）。
	//
	// 与 AskAIFn 同因：services/ai 反向 import services，这里不能直接持 *ai.AIService，
	// 函数签名是唯一能复用 AIAvailable() 又不构成环的形状。返回值为
	// (可用, 不可用原因, 激活档案名, 激活模型名)；原因取 AIAvailable 的 reason
	// （no_profile / no_endpoint / unavailable），无具体原因时为空串。
	AIAvailableFn func() (available bool, reason, profile, model string)

	// PurgeAICheckpointsFn 清理过期的 AI 编辑快照（库行 + 磁盘目录）。
	//
	// 与上面几个 Fn 同因：实现在 services/ai（快照目录的布局只有它知道），
	// 而清理要挂在 services 的启动后台协程里与剪贴板、墓碑同一趟做。
	// 未注入时（AI 服务没起来）跳过——那种情况下本来也没有快照。
	PurgeAICheckpointsFn func()

	// PurgeAIJobsFn 清掉过期的 AI 后台任务（库行 + 日志文件），启动时调一次。
	// 与快照同因：日志目录的布局只有 services/ai 知道。
	PurgeAIJobsFn func()

	// StopAIJobsFn 终止所有还在跑的 AI 后台任务，退出时调一次。
	//
	// 必须主动杀：它们是独立进程组（为了能整组杀掉 npm→node 这样的孙子进程），
	// 宿主退出不会带走。留下的 node / dotnet 会一直占着端口，
	// 而用户下次启动只看到「端口被占用」，完全联想不到是 AI 起的。
	// 这也正是「关闭 QuickDock 会终止后台任务」这句话的兑现方式。
	StopAIJobsFn func()

	// 标记"这是一次真退出"，令主窗口的 WindowClosing 钩子放行而不是取消关闭并隐藏到托盘。
	// 更新时（RestartApp 拉起安装器 → app.Quit）必须先调用，否则 cleanup() 里的
	// window.Close() 会被钩子 event.Cancel() 掉，窗口不销毁、进程退出不干净。
	PrepareQuitFn func()

	// RestartAppFn 注入「重启应用」能力（实现在 main 包 tray.go：脱离式拉起新进程 +
	// --restart-wait 等待旧进程退出，再走统一退出路径）。存储位置切换等场景需要
	// 真正重启才能让新数据目录生效——注意与 UpdateService.RestartApp 区分：
	// 那个是「就地替换更新」专用，无待更新时会失败，不能用于普通重启。
	RestartAppFn func()

	// 插件管理器
	PluginMgr     *plugin.Manager
	PluginHotkeys *PluginHotkeyRegistry
	PluginsDir    string // 插件根目录（用于定位 common.css 等共享资源）

	// 插件窗口管理器（每个插件独立窗口）
	PluginWindowMgr *plugin.PluginWindowManager

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

	// schedWG 跟踪三个常驻循环本体；taskWG 跟踪它们派生出的探测/执行协程。
	//
	// 光 close(schedulerQuit) 是不够的：循环收到信号后会立即返回，但此前已 go
	// 出去的执行协程（HTTP 监控最长 30s）仍持有 DB 引用。若紧接着关闭 DB，
	// 这些协程写回执行结果的操作会全部静默失败——用户看到的是「任务明明跑了，
	// 结果却没记录」。退出前必须等它们收尾，超时兜底见 waitForSchedulers。
	schedWG sync.WaitGroup
	taskWG  sync.WaitGroup

	// 剪贴板监听上下文：用于取消正在进行的剪贴板处理 goroutine
	ClipboardCtx    context.Context
	ClipboardCancel func()

	// 剪贴板变更信号（容量 1，丢弃式发送做合并）：Win32 消息线程只置信号，
	// 真正的读取/解析/入库在 startClipboardWorker 起的协程上执行（见 clipboard_sys.go）。
	clipboardWake chan struct{}

	// 剪贴板捕获看护状态（见 clipboard_sys.go 的 runClipboardCapture / pollClipboardSequence）。
	//
	// clipboardSeq 记录「已处理到哪个剪贴板序号」，兜底轮询靠它发现「序号变了却没收到
	// WM_CLIPBOARDUPDATE」的漏抓；clipboardBusy 防事件与轮询并发捕获；clipboardBusyAt
	// 用于把卡住的捕获写进日志（GetClipboardData 对延迟渲染格式没有超时）。
	clipboardSeq    atomic.Uint32
	clipboardBusy   atomic.Bool
	clipboardBusyAt atomic.Int64

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
	envMgr := env.NewManager()
	// 项目级版本切换：把「运行时 id + 期望版本 → bin 目录」的解析能力注入 db 层。
	// db 是叶子包、不 import internal/env，故用回调注入而不是直接引用。
	// 这里做一层 id 转换，让 db 的签名只认裸 string，不沾 env.Runtime 类型。
	db.SetPathEnvResolver(func(rt, version string) string {
		return envMgr.BinDirFor(env.Runtime(rt), version)
	})
	// 内置站点服务的证书由 mkcert 签发；sites 包只认 CertIssuer 接口，不 import env。
	siteMgr.SetCertIssuer(envMgr)
	return &AppService{
		NodeEnv: nodeEnv,
		DSH:     dshcore.NewDSHProcessManager(nil, nodeEnv),
		Env:     envMgr,
		Flags:   &WindowFlags{},
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
		// 崩溃自愈看门狗：内部自起 goroutine，首个巡检在 15s 后，不阻塞启动路径。
		a.Env.StartWatchdog(context.Background())
		// Everything 文件检索预热：后台无头拉起实例（内部自带全部前置条件判断：
		// 已装/未运行/总开关开/本进程建得了索引），把冷索引的空窗挪出用户交互期。
		if ev := a.Env.EverythingRuntime(); ev != nil {
			go ev.WarmUp()
		}
	}
	// 站点服务：用户此前显式启用过站点则自动拉起。
	// 失败只记日志不弹窗——开机就抛一个「443 绑定失败」没有意义，状态在 SitesList 里可见，
	// 用户点「启动」即可拿到完整报错。
	siteMgr.Resume()
}

// StartEnvServicesAsync 在后台对账拉起「已开启常驻」的本地服务（redis / caddy / php / nginx ...）。
//
// 必须由主窗口显示之后触发（main.go 注册 WindowShow 钩子），不能在启动路径上同步执行：
// ReconcileEnabled 对每个已开启运行时都要跑版本扫描 + 端口占用探测 + 启动前配置校验
// （caddy validate / nginx -t），全是子进程调用，实测 4 个运行时串行吃掉约 7 秒；
// 而它原先在 app.Run() 之前执行，等于把主窗口显示硬生生拖后同样长的时间。
// 改到窗口就绪后触发：主界面先可用，本地服务在后台按「期望状态」补齐。
func (a *AppService) StartEnvServicesAsync() {
	if a.Env == nil {
		return
	}
	go func() {
		defer logger.RecoverToLog("env:reconcile")
		// 先回收上次异常退出遗留的孤儿服务（占着端口会导致后续对账跳过 / 启动冲突），
		// 再按期望态拉起本会话的服务。
		a.Env.ReclaimOrphans(context.Background())
		a.Env.ReconcileEnabled(context.Background())
		// 对账完成前，环境页读到的是「未运行」，与该运行时的期望态不一致；
		// 完成后通知前端重拉一次状态（EnvironmentPage 已监听该事件）。
		if a.app != nil {
			a.app.Event.Emit("quickdock:env:refreshed")
		}
	}()
}

// ShowMainWindow 把主窗口唤起/前置（最小化则还原）。
//
// 供前端从独立浮窗（剪贴板 / SSH）触发「跳转配置」时调用：
// 浮窗里的「问 AI」若发现没配档案，需要把主窗口带上来并打开 AI 设置页，
// 否则用户点完仍在浮窗里，看不到配置界面。
func (a *AppService) ShowMainWindow() {
	w := a.MainWindow
	if w == nil {
		return
	}
	if w.IsMinimised() {
		w.Restore()
	}
	w.Show()
}

// OpenAISettings 跨窗口跳转：唤起主窗口并令其打开 AI 配置页。
//
// 为什么做成一个 Go 方法而不是直接前端 Events.Emit：事件从浮窗发往主窗口
// 虽能送达，但「唤起主窗口」这一步必须落在 Go 侧（窗口控制权在宿主），
// 集中在 Go 里做比前端各自 Emit + 再 Emit 更不易分叉。
func (a *AppService) OpenAISettings() {
	a.ShowMainWindow()
	if a.app != nil {
		a.app.Event.Emit("app:open-ai-settings")
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
