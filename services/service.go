package services

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/plugin"
	"quickdock/services/dsh"

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
	schedulerQuit      chan struct{}
	schedulerQuitOnce sync.Once

	// 监控在检标记：防止同一监控并发检测（慢探针未更新 last_checked_ts 时被重复选为待检）
	// 导致宕机时重复发送通知。LoadOrStore 原子保证同一时刻仅一个检查协程通过。
	monitorInflight sync.Map // monitorID -> struct{}

	// 定时任务在运行标记：防止慢任务（http 阻塞 / command·open 挂起）重排前被重复选中并发执行。
	// 与 monitorInflight 同款 LoadOrStore 模式。
	schedInflight sync.Map // taskID -> struct{}

	// 插件前端页面 HTML 缓存
	frontendCache   map[string]*frontendCacheEntry
	frontendCacheMu sync.RWMutex

	// 跨窗口传递：命令面板→插件窗口的初始计算文本 + 命中的子命令
	// pendingInitPlugin 记录归属插件 id，消费方须匹配才取用，防止快速连开时跨插件错配。
	pendingInitPlugin  string
	pendingInitText    string
	pendingInitCommand string
	pendingInitTextMu  sync.Mutex

	// 本地 AI 流式服务（127.0.0.1 随机端口，前端 fetch 读取分块响应）
	// aiStreamMu 保护 aiStream 字段（启动/停止/查询可并发调用）
	aiStreamMu sync.Mutex
	aiStream   *aiStreamServer

	// DeepSeek Harness 运行环境（检测/下载便携 Node + 安装 dsh）与进程管理
	NodeEnv *dsh.NodeEnvManager
	DSH     *dsh.DSHProcessManager

	// 共享 HTTP 客户端（连接复用，避免每次 AI 请求新建 TLS 握手）
	aiHTTPClient *http.Client

	// 更新检查：最近一次检测结果缓存（供 GetUpdateState 回填版本号与更新说明，
	// 因为 Wails Updater 不对外暴露 pending release）。updateCheckMu 串行化
	// Check，避免手动"检测更新"与后台定时检查并发探测。
	lastUpdateCheck   *UpdateStatus
	lastUpdateCheckMu sync.RWMutex
	updateCheckMu     sync.Mutex

	// 当前激活 AI 档案缓存：聊天流式请求会高频调用 getActiveAIProfile（读库+DPAPI 解密）。
	// 保存档案时通过 invalidateAICache 失效。mu 保护可重入。
	aiCacheMu   sync.RWMutex
	aiCachedCfg AIProfile
	aiCachedOK  bool
}

type frontendCacheEntry struct {
	html        string
	htmlMtime   time.Time
	commonMtime time.Time // common.css 修改时间，变化时全部失效
}

// NewAppService 创建应用服务实例
func NewAppService() *AppService {
	nodeEnv := dsh.NewNodeEnvManager()
	return &AppService{
		frontendCache: make(map[string]*frontendCacheEntry),
		NodeEnv:       nodeEnv,
		DSH:           dsh.NewDSHProcessManager(nil, nodeEnv),
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

// SetApp 设置 App 引用（由 main.go 在创建后调用）
func (a *AppService) SetApp(app *application.App) {
	a.app = app
	a.NodeEnv.SetApp(app)
	a.DSH.SetApp(app)
}

// DetectNodeEnv 检测 node/npx/dsh 运行状态
func (a *AppService) DetectNodeEnv() *ApiResult {
	if a.NodeEnv == nil {
		return FailMsg("node env 未初始化")
	}
	return Ok(a.NodeEnv.Detect())
}

// SetupDSH 一键安装运行环境（缺失时下载便携 Node + 安装 dsh），进度经事件推送前端。
// 异步执行：立即返回，前端订阅 quickdock:dsh:progress 展示进度。
func (a *AppService) SetupDSH() *ApiResult {
	if a.NodeEnv == nil {
		return FailMsg("node env 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("安装 DeepSeek Harness 异常: %v", r)
				a.NodeEnv.EmitLog("error", msg)
				// panic 也必须补发 error 事件，否则前端 settingUp 永远无法复位，按钮永久禁用
				if a.app != nil {
					a.app.Event.Emit("quickdock:dsh:progress", dsh.SetupProgress{Stage: "error", Message: msg})
				}
			}
		}()
		_ = a.NodeEnv.SetupDSH(context.Background(), nil)
	}()
	return Ok(nil)
}

// DSHInstallPlugin 安装指定插件（执行 dsh plugin --profile web add <plugin>）。
// 异步执行，输出经 quickdock:dsh:log 事件推送到前端日志面板。
func (a *AppService) DSHInstallPlugin(plugin string) *ApiResult {
	if a.DSH == nil {
		return FailMsg("DSH 未初始化")
	}
	plugin = strings.TrimSpace(plugin)
	if plugin == "" {
		return FailMsg("插件名不能为空")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 与 SetupDSH/UpdateDSH 相同：goroutine 内 panic 必须消化，否则会崩掉整个应用
				msg := fmt.Sprintf("安装插件异常: %v", r)
				a.NodeEnv.EmitLog("error", msg)
				if a.app != nil {
					a.app.Event.Emit("quickdock:dsh:plugin", map[string]bool{"ok": false})
				}
			}
		}()
		_ = a.DSH.InstallPlugin(plugin)
	}()
	return Ok(nil)
}

// OpenDSHWindow 拉起 dsh web 并在原生窗口加载其 URL（dsh 未安装时返回错误）
func (a *AppService) OpenDSHWindow() *ApiResult {
	if a.DSH == nil {
		return FailMsg("DSH 未初始化")
	}
	url, err := a.DSH.OpenDSHWindow()
	if err != nil {
		return Fail(err)
	}
	return Ok(map[string]string{"url": url})
}

// dshAutoStartKey dsh web 自动启动配置（app_state 键，"1"=开启，默认开启）
const dshAutoStartKey = "dsh_auto_start"

// DSHStatus 返回 dsh web 服务运行状态与自动启动配置（供设置页状态展示与开关）
func (a *AppService) DSHStatus() *ApiResult {
	st := map[string]any{
		"running":   false,
		"autoStart": a.dshAutoStartEnabled(),
		"port":      dsh.DefaultDSHPort,
		"url":       "",
	}
	if a.DSH != nil {
		port := a.DSH.Port()
		st["running"] = a.DSH.Running()
		st["port"] = port
		if a.DSH.Running() {
			st["url"] = "http://127.0.0.1:" + strconv.Itoa(port)
		}
	}
	return Ok(st)
}

// DSHStart 启动 dsh web 服务（只起进程，不开窗口；进程已运行则直接复用返回）
func (a *AppService) DSHStart() *ApiResult {
	if a.DSH == nil {
		return FailMsg("DSH 未初始化")
	}
	url, err := a.DSH.Start()
	if err != nil {
		return Fail(err)
	}
	return Ok(map[string]string{"url": url})
}

// DSHStop 停止 dsh web 服务（杀进程树，释放 3080 端口；窗口若开着会导航到"已停止"提示页）
func (a *AppService) DSHStop() *ApiResult {
	if a.DSH == nil {
		return FailMsg("DSH 未初始化")
	}
	a.DSH.Stop()
	a.DSH.NotifyStopped()
	return Ok(nil)
}

// DSHSetAutoStart 开关 dsh web 随 QuickDock 启动延迟自动开启（默认开启）
func (a *AppService) DSHSetAutoStart(enabled bool) *ApiResult {
	if a.DB == nil {
		return FailMsg("database not initialized")
	}
	v := "0"
	if enabled {
		v = "1"
	}
	if err := a.DB.SetValue(dshAutoStartKey, v); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// dshAutoStartEnabled 读取 dsh web 自动启动配置（默认开启）
func (a *AppService) dshAutoStartEnabled() bool {
	if a.DB == nil {
		return true
	}
	v, err := a.DB.GetValue(dshAutoStartKey)
	if err != nil || v == "" {
		return true
	}
	return v == "1"
}

// CheckDSHUpdate 检测已安装 dsh 是否有新版本（联网查 latest；查询失败静默返回当前状态，不阻塞 UI）
func (a *AppService) CheckDSHUpdate() *ApiResult {
	if a.NodeEnv == nil {
		return FailMsg("node env 未初始化")
	}
	return Ok(a.NodeEnv.DetectWithUpdate())
}

// UpdateDSH 将 dsh 更新到最新版（异步执行；进度经 quickdock:dsh:progress/log 事件推送前端）
func (a *AppService) UpdateDSH() *ApiResult {
	if a.NodeEnv == nil {
		return FailMsg("node env 未初始化")
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("更新 DeepSeek Harness 异常: %v", r)
				a.NodeEnv.EmitLog("error", msg)
				if a.app != nil {
					a.app.Event.Emit("quickdock:dsh:progress", dsh.SetupProgress{Stage: "error", Message: msg})
				}
			}
		}()
		err := a.NodeEnv.UpdateDSH(context.Background())
		if err == nil && a.DSH != nil && a.DSH.Running() {
			// dsh 包已更新，但正在运行的进程仍是旧代码——自动重启服务让新版本生效，
			// 否则用户点完"更新"以为升级了，实际跑的还是旧版。
			a.NodeEnv.EmitLog("info", "dsh 更新完成，正在重启服务使新版本生效…")
			a.DSH.Stop()
			time.Sleep(500 * time.Millisecond) // 兜底等端口完全释放（Stop 内部已等）
			if _, err2 := a.DSH.Start(); err2 != nil {
				a.NodeEnv.EmitLog("error", "dsh 更新完成，但重启服务失败: "+err2.Error()+"（可在设置中手动启动）")
			}
		}
	}()
	return Ok(nil)
}
