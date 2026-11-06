package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/plugin"
	"quickdock/internal/sysutil"
	"quickdock/services"
	aisvc "quickdock/services/ai"
	clipboardsvc "quickdock/services/clipboard"
	collectionsvc "quickdock/services/collection"
	diagsvc "quickdock/services/diag"
	dshsvc "quickdock/services/dsh"
	envsvc "quickdock/services/env"
	frecencysvc "quickdock/services/frecency"
	gitsvc "quickdock/services/git"
	itemsvc "quickdock/services/item"
	logtailsvc "quickdock/services/logtail"
	mailboxsvc "quickdock/services/mailbox"
	mcpsvc "quickdock/services/mcp"
	monitorsvc "quickdock/services/monitor"
	notesvc "quickdock/services/note"
	notifysvc "quickdock/services/notify"
	pluginsvc "quickdock/services/plugin"
	portsvc "quickdock/services/port"
	recyclesvc "quickdock/services/recycle"
	scenesvc "quickdock/services/scene"
	shelfsvc "quickdock/services/shelf"
	snapshotsvc "quickdock/services/snapshot"
	sshxsvc "quickdock/services/sshx"
	systemsvc "quickdock/services/system"
	todosvc "quickdock/services/todo"
	updatesvc "quickdock/services/update"
	weathersvc "quickdock/services/weather"
	workspacesvc "quickdock/services/workspace"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/kvstore"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// ---- 主窗口尺寸/位置记忆 ----
// 启动时 DB 尚未初始化（ServiceStartup 在 app.Run 内执行），故用独立的 JSON 文件
// 持久化窗口矩形，避免依赖数据库时序。启动时 loadWindowState 恢复，移动/缩放后防抖写回。
//
// Width/Height 存的是**逻辑尺寸**（= 物理 DIP / uiZoom），不是屏幕上量到的那两个数。
// 为什么：窗口的 Width 是 DIP、Zoom 是 WebView 的 CSS 缩放，两者独立——同一份
// 「1100×800」记录在 zoom 1.0 下给内容 1100 CSS px，在 zoom 1.2 下只剩 917。
// 用户调大缩放是为了「字大一点」，不是「能看见的东西少一点」，所以记录必须按
// 缩放归一化存储，恢复时再乘当前档位。
//
// Zoom 字段标记这份记录是在哪个档位下存的：0 表示旧版本写的（当时只有 1.0 档，
// 逻辑尺寸恰好等于屏幕上的值），按当前档位直接换算即可。
type windowState struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Zoom   float64 `json:"zoom,omitempty"`
}

// mainWinWidth/mainWinHeight 等：主窗口的尺寸同样要乘 uiZoom。
//
// 浮窗（剪贴板/命令面板/窗口管理）一直是这么做的，主窗口漏了——于是把缩放从
// 1.0 调到 1.2，浮窗的内容空间不变、主窗口的内容空间却少了 1/6：
// 167px 的宽度差看着不多，但侧栏还占 210，剩下给内容的不足 710，一票固定
// 宽度的弹层（680 的任务弹窗）当场溢出。
func windowPx(logical int) int {
	if uiZoom == 1 || logical <= 0 {
		return logical
	}
	return int(math.Round(float64(logical) * uiZoom))
}

// unwindowPx 是 windowPx 的逆运算，用于落盘。
func unwindowPx(px int) int {
	if uiZoom == 1 || px <= 0 {
		return px
	}
	return int(math.Round(float64(px) / uiZoom))
}

func windowStatePath() string {
	return filepath.Join(EnsureConfigDir(), "window_state.json")
}

func loadWindowState() *windowState {
	data, err := os.ReadFile(windowStatePath())
	if err != nil {
		return nil
	}
	var s windowState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	// 基本合理性校验：小于最小尺寸或越界视为无效
	if s.Width < 800 || s.Height < 500 {
		return nil
	}
	return &s
}

var (
	windowStateMu        sync.Mutex
	windowStateSaveTimer *time.Timer
	windowStateQuit      atomic.Bool // app.Run 返回后置位，阻止延迟 timer 回调操作已销毁窗口
)

// scheduleSaveWindowState 防抖保存窗口矩形。最大化/最小化态不记录，避免恢复成畸形窗口。
func scheduleSaveWindowState(w *application.WebviewWindow) {
	if w == nil || w.IsMaximised() || w.IsMinimised() {
		return
	}
	windowStateMu.Lock()
	if windowStateSaveTimer != nil {
		windowStateSaveTimer.Stop()
	}
	windowStateSaveTimer = time.AfterFunc(400*time.Millisecond, func() {
		// 取 Bounds 必须在 windowStateMu 内，且与退出路径的 Stop 串行：
		// 否则「Quit.Load()==false 通过之后、w.Bounds() 之前」窗口可能已被销毁 → panic（TOCTOU）。
		windowStateMu.Lock()
		if windowStateQuit.Load() {
			windowStateMu.Unlock()
			return
		}
		b := w.Bounds()
		windowStateMu.Unlock()

		// 落盘前归一化到逻辑尺寸：见 windowState 的注释——存屏幕上的值，
		// 换个缩放档位打开就变成「内容被挤」或「窗口莫名变大」。
		s := windowState{
			X: b.X, Y: b.Y,
			Width: unwindowPx(b.Width), Height: unwindowPx(b.Height),
			Zoom: uiZoom,
		}
		data, err := json.Marshal(s)
		if err != nil {
			return
		}
		if err := os.WriteFile(windowStatePath(), data, 0644); err != nil {
			logger.W("[窗口状态] 保存失败: %v", err)
		}
	})
	windowStateMu.Unlock()
}

// logStartupDetail 在 app.Run() 前打印宿主侧 WebView2 启动细节，
// 补足框架那行笼统的 "[WebView2] Environment created successfully"。
func logStartupDetail(app *application.App, mainOpts application.WebviewWindowOptions, saved *windowState) {
	wvPath := EnsureConfigDir() + "\\WebView2"
	rect := "默认"
	if saved != nil {
		// 记录里是逻辑尺寸，日志要打实际会用的 DIP，否则「记录 917、窗口 1100」
		// 看着像 bug（其实只是减掉缩放档位）。
		rect = fmt.Sprintf("%dx%d @(%d,%d)", windowPx(saved.Width), windowPx(saved.Height), saved.X, saved.Y)
		switch {
		case saved.Zoom == 0:
			// 旧版本写的记录没有档位字段，按当前档位当逻辑值处理（一次性迁移误差）
			rect += "（旧记录，无档位标记）"
		case saved.Zoom != uiZoom:
			rect += fmt.Sprintf("（记录于 %.1f 档，已换算到当前档位）", saved.Zoom)
		}
	} else {
		rect = fmt.Sprintf("%dx%d @默认", mainOpts.Width, mainOpts.Height)
	}
	screens := 0
	if app != nil {
		screens = len(app.Screen.GetAll())
	}
	logger.I("[启动] 应用版本=%s | WebView2 数据目录=%s | 屏幕数=%d | 界面缩放=%.1f", appVersion, wvPath, screens, uiZoom)
	logger.I("[启动] 窗口计划：主窗口=%s（内容区 CSS 宽 %d）；浮窗=剪贴板/命令面板/笔记（懒创建，Run 后预创建并 Hide）",
		rect, int(float64(mainOpts.Width)/uiZoom))
	logger.I("[启动] WebView2 优化：附加参数 %d 项（--in-process-gpu / --renderer-process-limit=4 等），禁用特性 %d 项（%s）",
		len(memoryOptimizedArgs), len(disabledFeatures), strings.Join(disabledFeatures, ","))
}

// startRenderHeartbeat 渲染进程看门狗心跳：每 5s 向所有窗口广播 qd:heartbeat。
// 前端据此检测 WebView2 渲染/桥接卡死（日志里表现为 "Eval failed: not in correct state"），
// 自动重载页面自我恢复，避免"时间一长页面空白"需手动重启。发射失败（前端未就绪/桥接已断）静默忽略。
func startRenderHeartbeat(app *application.App, quit <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				// app.Run() 返回后窗口已销毁，再 Emit 只会打出一串
				// "Eval failed: not in correct state"，且 goroutine 永远不退出。
				return
			case <-ticker.C:
				if app == nil {
					continue
				}
				app.Event.Emit("qd:heartbeat", time.Now().Unix())
			}
		}
	}()
}

// precreateFloatingWindows 是 QD_PRECREATE_WINDOWS=1 时的可选预热路径。
//
// 关键不是「要不要预建」，而是「在哪儿建」：三个 WebView2 窗口必须在主线程创建，
// 又不能挤在 app.Run() 之前拖慢首屏。所以后台 goroutine 先等主窗口跑起来，再逐个
// 用 InvokeAsync 投递到主线程，中间留出间隔让消息循环处理完用户自己的第一批操作。
// getter 内部还有 newFloatingWindowOnMainThread，此时已在主线程会直接执行，不会重入。
func precreateFloatingWindows(svc *services.AppService) {
	go func() {
		defer logger.RecoverToLog("precreate-floating-windows")
		time.Sleep(1500 * time.Millisecond)
		getters := []func() *application.WebviewWindow{
			svc.GetClipboardWindow, svc.GetPaletteWindow, svc.GetNoteWindow,
		}
		for _, get := range getters {
			g := get
			application.InvokeAsync(func() {
				if w := g(); w != nil {
					w.Hide()
				}
			})
			time.Sleep(400 * time.Millisecond)
		}
	}()
}

//go:embed all:frontend/dist
var assets embed.FS

// 插件共享资源（common.css / common.js）：随 exe 打包，每次启动同步到
// ~/.quickdock/plugins/builtin/ 供历史外部插件引用。
//
// 目录里不再有内置插件：内置插件方案已搁置（插件一律来自市场或本地 zip 安装），
// 故 plugins/ 下只留这一份共享样式，不参与插件的注册与加载。
//
//go:embed all:plugins/shared
var sharedPluginAssets embed.FS

//go:embed updater.key.pub
var updaterPublicKey []byte

// appVersion 在编译时通过 -ldflags="-X main.appVersion=0.2.0" 注入版本号
var appVersion = "0.0.0"

const (
	appTitle  = "快启坞 QuickDock"
	appWidth  = 1100
	appHeight = 800

	// 主窗口允许的最小尺寸（逻辑值，见下方 windowPx）。800×600 是「侧栏 210 +
	// 主区 590」的下限：再窄，页头的标题 + 按钮组就放不下一行（会横向滚动）。
	appMinWidth  = 800
	appMinHeight = 600
)

// uiZoom 是全局 UI 等比缩放（WebView ZoomFactor），主窗口与所有独立浮窗共用，
// 改这一个值即统一改全部窗口的文字/图标/间距大小。
//
// 取值来自「设置 → 基础 → 界面缩放」，由 platform.LoadUiZoom 读持久化配置，默认 1.0（标准）。
// 用 var 而非 const 是有意的：窗口的 Zoom 与尺寸在创建时即固定，而配置只有进程启动
// 后才读得到（此时数据库尚未打开，缩放因此存在数据目录之外的 quickdock.json，见
// internal/platform/uizoom.go）。包级 var 在 main() 之前完成初始化，且 Go 按依赖顺序
// 初始化——下面各尺寸依赖 uiZoom，必然排在其后，故无需显式的初始化函数。
//
// 定尺寸浮窗（剪贴板/笔记/命令面板/窗口管理）的宽高必须按同一系数推导：
// 这些窗口 Frameless 且宽高固定，若只放大内容（Zoom）不改窗口尺寸，
// 放大后的内容会被固定窗口裁掉或整块溢出滚动——两者必须同步。
// 下面尺寸写成「基准 × uiZoom」即为此；SSH 可缩放，同样缩放保持默认观感一致。
var (
	uiZoom = platform.LoadUiZoom()

	clipWinWidth     = int(480 * uiZoom)
	clipWinHeight    = int(540 * uiZoom)
	paletteWinWidth  = int(900 * uiZoom)
	paletteWinHeight = int(600 * uiZoom)
	winmgrWinWidth   = int(900 * uiZoom)
	winmgrWinHeight  = int(700 * uiZoom)

	// SSH 终端窗口比浮窗大得多：终端 + 会话列表 + 状态条需要横向空间，
	// 且 80 列终端在 640px 以下会挤（见 windows.go 的 MinWidth，已按 uiZoom 放大）。
	sshWinWidth  = int(1000 * uiZoom)
	sshWinHeight = int(680 * uiZoom)
)

// 主窗口与各浮窗的可见/模式标志：与 services 共享同一实例（见 services.WindowFlags）。
// 此前是 4 个独立包级 atomic.Bool 逐个以裸指针注入 AppService，跨包共享裸指针，
// 归属与生命周期都不清晰；收成单一对象后由 main 持有并显式交给 AppService。
var windowFlags = &services.WindowFlags{}

func main() {
	// 提权子进程快速路径：必须早于 logger.Init 与 application.New（单实例互斥在 New 里），
	// 详见 elevated_cli.go。
	runElevatedCLI()

	// 数据目录迁移的收尾：把数据库搬到新目录，并删掉旧目录。
	//
	// 之所以在启动时做（而不是迁移当时），是因为迁移发生在应用运行时——
	// 数据库正被本进程独占，Windows 上既不能复制（WAL 拆分导致快照不一致）
	// 也不能删除（文件被占用）。重启后句柄已释放，才能用 VACUUM INTO 导出
	// 一致快照、再整体删除旧目录。
	//
	// 必须早于 logger.Init 与 DB 打开——此时应用尚未触碰任何数据目录，最安全。
	if old := platform.PendingCleanupDir(); old != "" {
		if err := db.FinishDataDirMigration(old, platform.DefaultDataDir()); err != nil {
			fmt.Fprintf(os.Stderr, "[quickdock] 旧数据目录清理失败: %v\n", err)
		}
	}

	// 数据目录迁移的兜底路径：正常流程是用户在「设置 → 存储与同步」里点迁移，
	// 由 StorageService 在运行时执行（这样才有进度条）。这里只处理一种遗留情况：
	// 上一版实现把「迁移」记录成 config 里的 migrateFrom 标记，等下次启动时搬。
	// 若检测到该标记（旧版本留下的），仍在此静默完成，避免数据卡在两处。
	//
	// 必须早于 logger.Init 与 DB 打开——此时应用尚未触碰任何数据目录，拷贝最安全。
	if platform.HasPendingDataDirMigration() {
		if err := platform.ApplyPendingDataDirMigration(nil); err != nil {
			fmt.Fprintf(os.Stderr, "[quickdock] 数据目录迁移失败: %v\n", err)
		}
	}

	// 全局日志：先于一切初始化，之后所有包（services/plugin/tray）的关键事件统一落盘
	// ~/.quickdock/logs/quickdock-YYYYMMDD.log
	logger.Init(filepath.Join(platform.DefaultDataDir(), "logs"))
	// 第三方库日志汇入统一日志面板：slog 默认记录器与标准库 log 都桥接到 logger
	logger.EnableSlogBridge()
	// 顶层兜底：panic 落盘完整堆栈后原样重抛（不吞异常），崩溃现场留在 <logs>/crash/
	defer logger.CapturePanic("main")
	logger.I("QuickDock 启动 -------------------------------------------------------------")

	// 窗口材质（Mica / Acrylic）在「窗口创建时」就固定了，必须在下面建主窗口之前
	// 一次性判定系统能力 + 读持久化档位。浮窗是懒创建，读同一份缓存即可。
	// 不支持的系统（Win10 / 非 Windows）恒判定为 off，后面所有窗口都走原来的实色路径。
	if backdrop := platform.ResolveWindowBackdrop(); platform.SupportsWindowBackdrop() {
		logger.I("[启动] 窗口材质：系统支持，本次生效=%s", backdrop)
	} else {
		logger.I("[启动] 窗口材质：系统不支持（需 Win11 22621+），保持实色窗口")
	}

	// 托盘「重启」拉起的新进程带 --restart-wait=<旧PID>：先等旧进程退出再继续。
	// 必须早于 application.New——单实例互斥在 New 里，旧进程还在的话本进程
	// 会被判为重复启动而直接 os.Exit(0)（表现为「点了重启，应用没了」）。
	waitForRestartPredecessor()

	// 自建隐藏控制台，供后续所有子进程继承（Windows）：原先靠 CREATE_NO_WINDOW 让子进程
	// "隐藏"，实际语义是每个子进程分配一个新控制台 → 各带一个 conhost.exe（约 6.5MB）。
	// 47 插件全启用时 29 个后代进程白吃约 190MB。改为继承后全系统只剩 1 个 conhost。
	// 失败（或 dev 版已有可见控制台）时自动退回 CREATE_NO_WINDOW，功能不受影响。
	if sysutil.InitHiddenConsole() {
		logger.I("[启动] 已建立隐藏控制台：子进程统一继承，不再各自创建 conhost")
	} else {
		logger.I("[启动] 未建立隐藏控制台：子进程退回 CREATE_NO_WINDOW 兜底")
	}

	// macOS 无开发者账号时 ad-hoc 签名产物带 quarantine 隔离属性会被 Gatekeeper 拦截；
	// 启动时自检并清除（仅 darwin 生效，其他平台 no-op）。
	platform.ClearSelfQuarantine()

	// 创建 AppService 实例
	appService := services.NewAppService()

	// 共享窗口状态标志（main 与 AppService 持有同一实例）
	appService.Flags = windowFlags

	// 注入热键监听回调（避免循环依赖）
	appService.StartHotkeyListenerFn = StartHotkeyListener
	appService.SuspendHotkeysFn = SuspendHotkeys
	appService.ResumeHotkeysFn = ResumeHotkeys

	// 真退出标记：与托盘"退出"走同一路径，让 WindowClosing 钩子放行
	appService.PrepareQuitFn = func() { trayQuitRequested.Store(true) }

	// 重启应用（存储位置切换等场景的前端入口）：复用托盘同一套脱离式重启逻辑
	appService.RestartAppFn = restartApp

	// 初始化插件管理器
	pluginsDir := filepath.Join(platform.DefaultDataDir(), "plugins")
	os.MkdirAll(pluginsDir, 0755)
	pluginMgr := plugin.NewManager(pluginsDir)
	appService.PluginMgr = pluginMgr
	appService.PluginHotkeys = services.NewPluginHotkeyRegistry()
	appService.PluginsDir = pluginsDir

	// 插件服务：领域逻辑下沉到 services/plugin 子包，单独绑定为独立 Wails service
	pluginSvc := pluginsvc.NewPluginService(appService)
	appService.RegisterPluginHostFn = func() { pluginSvc.RegisterPluginHostMethods() }

	// 环境管理服务门面：承载 Env 领域方法（引擎在 internal/env），单独绑定为独立 Wails service
	envSvc := envsvc.NewEnvironmentService(appService)

	// 存储迁移服务门面：承载数据目录迁移（复制核心数据 + 清理旧目录 + 环境数据重装），
	// 单独绑定为独立 Wails service（前端要订阅其进度事件）
	storageSvc := services.NewStorageService(appService)

	// dsh 服务门面：承载 DeepSeek Harness 领域方法（引擎在 internal/dsh），单独绑定为独立 Wails service
	dshSvc := dshsvc.NewDSHService(appService)

	// AI 服务门面：承载 AI 档案/对话/聊天领域方法（门面即实现，纯 DB 依赖），单独绑定为独立 Wails service
	aiSvc := aisvc.NewAIService(appService)

	// 把「一次性 AI 问答」能力回注宿主，供 SSH 面板与插件复用。
	//
	// 走一次性接口（不落库、不占流式 convId）而不是复用流式对话：
	// 这两类调用都是一次性问答，写进用户的 AI 对话历史只会污染列表；
	// 且流式接口靠前端 SSE 驱动，插件与 Go 侧调用方拿不到那条通道。
	appService.AskAIFn = func(system, question string, timeoutSec int) (string, string, error) {
		r := aiSvc.AIAsk(aisvc.AIAskRequest{
			System:     system,
			Question:   question,
			TimeoutSec: timeoutSec,
		})
		if r == nil {
			return "", "", errors.New("AI 服务无响应")
		}
		if r.Code != 0 {
			msg := r.Msg
			if msg == "" {
				msg = "AI 调用失败"
			}
			return "", "", errors.New(msg)
		}
		res, ok := r.Data.(aisvc.AIAskResult)
		if !ok {
			return "", "", errors.New("AI 返回结构异常")
		}
		return res.Text, res.Model, nil
	}

	// 流式版本：onToken 把增量交给调用方，最终完整文本仍一次性返回。
	// 与 AskAIFn 共用 AIAskStream（见 services/ai），保证两套路径的截断/超时/档案选择一致。
	appService.AskAIFnStream = func(system, question string, timeoutSec int, onToken func(string)) (string, string, error) {
		r := aiSvc.AIAskStream(aisvc.AIAskRequest{
			System:     system,
			Question:   question,
			TimeoutSec: timeoutSec,
		}, onToken)
		if r == nil {
			return "", "", errors.New("AI 服务无响应")
		}
		if r.Code != 0 {
			msg := r.Msg
			if msg == "" {
				msg = "AI 调用失败"
			}
			return "", "", errors.New(msg)
		}
		res, ok := r.Data.(aisvc.AIAskResult)
		if !ok {
			return "", "", errors.New("AI 返回结构异常")
		}
		return res.Text, res.Model, nil
	}

	// goja 插件跑在宿主的 goja VM 里：没有 stdin、也没有前端，拿不到 stdio 通知
	// 与 Wails 事件，只能靠同步回调（api.aiStream）拿增量，故单独回注一份。
	pluginMgr.SetAIStreamFn(appService.AskAIFnStream)

	// 带思考增量的版本：只给面板用（SSH / 剪贴板 / 笔记走本地 SSE 时）。
	// 插件拿不到前端通道，思考流对它没有意义。
	appService.AskAIFnStreamReason = func(system, question string, timeoutSec int, onToken, onReason func(string)) (string, string, error) {
		r := aiSvc.AIAskStreamReason(aisvc.AIAskRequest{
			System:     system,
			Question:   question,
			TimeoutSec: timeoutSec,
		}, onToken, onReason)
		if r == nil {
			return "", "", errors.New("AI 服务无响应")
		}
		if r.Code != 0 {
			msg := r.Msg
			if msg == "" {
				msg = "AI 调用失败"
			}
			return "", "", errors.New(msg)
		}
		res, ok := r.Data.(aisvc.AIAskResult)
		if !ok {
			return "", "", errors.New("AI 返回结构异常")
		}
		return res.Text, res.Model, nil
	}

	// 把「AI 是否可用」能力回注宿主，供插件在调用 host.ai.ask 前预检
	// （宿主自身面板用 Wails 绑定 AIAvailable 判断，插件走 JSON-RPC 拿不到那条路）。
	// 解包 ApiResult.Data 里的 available/reason/profile/model，未配置时返回
	// (false, reason, "", "") 让插件直接提示「去设置配置 AI 档案」，而非盲目调用后拿裸错。
	appService.AIAvailableFn = func() (bool, string, string, string) {
		r := aiSvc.AIAvailable()
		if r == nil || r.Code != 0 {
			return false, "unavailable", "", ""
		}
		d, ok := r.Data.(map[string]interface{})
		if !ok {
			return false, "unavailable", "", ""
		}
		var available bool
		if b, ok := d["available"].(bool); ok {
			available = b
		}
		var reason, profile, model string
		if s, ok := d["reason"].(string); ok {
			reason = s
		}
		if s, ok := d["profile"].(string); ok {
			profile = s
		}
		if s, ok := d["model"].(string); ok {
			model = s
		}
		return available, reason, profile, model
	}

	// 更新服务门面：承载自动更新领域方法（检测/下载/重启/后台检查），单独绑定为独立 Wails service
	updateSvc := updatesvc.NewUpdateService(appService)

	// 剪贴板服务门面：承载剪贴板历史领域方法（宿主保留系统监听层），单独绑定为独立 Wails service
	clipSvc := clipboardsvc.NewClipboardService(appService)

	// 诊断/系统/工作空间/待办/监控 门面：承载对应领域 CRUD（宿主保留常驻循环），单独绑定为独立 Wails service
	diagSvc := diagsvc.NewDiagService(appService)
	systemSvc := systemsvc.NewSystemService(appService)
	workspaceSvc := workspacesvc.NewWorkspaceService(appService)
	todoSvc := todosvc.NewTodoService(appService)
	monitorSvc := monitorsvc.NewMonitorService(appService)

	// 日志尾随门面：会话/轮询/事件推送（读取内核在 internal/logtail）
	logtailSvc := logtailsvc.NewLogTailService(appService)

	// 通知中心门面：读/已读/删除（写入统一走 appService.PushNotification）
	notifySvc := notifysvc.NewNotifyService(appService)

	// 回收站门面：误删的项目/集合/场景/笔记在此恢复（墓碑由 db 删除路径写入）
	recycleSvc := recyclesvc.NewRecycleService(appService)

	// 笔记/收藏/场景/快照/项目/端口/频率 门面：承载剩余纯 CRUD 领域方法，单独绑定为独立 Wails service
	noteSvc := notesvc.NewNoteService(appService)
	collectionSvc := collectionsvc.NewCollectionService(appService)
	frecencySvc := frecencysvc.NewFrecencyService(appService)
	sceneSvc := scenesvc.NewSceneService(appService)
	snapshotSvc := snapshotsvc.NewSnapshotService(appService)
	itemSvc := itemsvc.NewItemService(appService)
	portSvc := portsvc.NewPortService(appService)
	// 天气请求必须留在宿主：WebView2 跟随系统代理，代理端口没起时前端 fetch 全超时。
	weatherSvc := weathersvc.NewWeatherService()

	// 邮件验证码与文件置物架：两个常驻/有状态能力都是宿主单例。
	// 前者持有 IMAP 长连接与唯一快照，后者在窗口拖放事件里被直接调用。
	mailboxSvc := mailboxsvc.NewMailboxService(appService)
	shelfSvc := shelfsvc.NewShelfService(appService)

	// Git 面板门面：仓库清单/状态/分支/提交历史与 fetch/pull/切分支。
	// 仓库清单落 app_state（KV），不建表——它只是路径串，没有关联查询。
	gitSvc := gitsvc.NewGitService(appService)

	// SSH 服务门面：主机管理/指纹信任/连通性测试/命令执行（引擎在 internal/sshx），
	// 单独绑定为独立 Wails service。
	sshSvc := sshxsvc.NewSshService(appService)
	// 供全局热键走同一条打开路径（见 windows.go 的 showSshWindow）
	setSshService(sshSvc)
	// SSH 面板的「问 AI」走本地 SSE（见 services/ai/ai_ask_stream.go）。
	// 注册只能发生在这里：prompt 组装在 sshx、流式端点在 ai，两边互相 import 会成环。
	aiSvc.RegisterAskStream("ssh", sshSvc.AskOverStream)
	// 剪贴板 / 笔记的「问 AI」同理，同一条 SSE（kind 区分场景）。
	aiSvc.RegisterAskStream("clipboard", clipSvc.AskAIOverStream)
	aiSvc.RegisterAskStream("note", noteSvc.AskAIOverStream)

	// MCP 服务门面：协议/传输在 internal/mcp，本门面注册业务工具并提供前端启停接口。
	// 依赖剪贴板/工作空间/待办/诊断/系统/笔记/收藏/端口/集合/监控门面（工具复用其能力），故在其后创建。
	envengine.SetMCPAppVersion(appVersion)
	mcpSvc := mcpsvc.NewMCPService(appService, clipSvc, pluginSvc, workspaceSvc, todoSvc, diagSvc, systemSvc, noteSvc, frecencySvc, portSvc, collectionSvc, monitorSvc, appVersion)

	// 系统通知服务（用于待办定时提醒）
	notifier := notifications.New()

	// KV 持久化服务（浮窗位置记忆等轻量状态；ServiceStartup/Shutdown 自动 Load/Save）
	kvStore := kvstore.New()

	opts := application.Options{
		Name:        "快启坞",
		Description: "快启坞 QuickDock — 开发者资源集合与快速启动工具",
		// 单实例：框架内置实现（CreateMutex + 隐藏事件窗口）。二次启动时框架先通知
		// 首实例（OnSecondInstanceLaunch 把主窗口带到前台），再以 ExitCode 退出本进程。
		// UniqueID 与旧 Local\\QuickDock-Instance 同义：dev/prod 共用数据目录与同一把锁。
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "QuickDock-Instance",
			ExitCode: 0,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if win := GetMainWindow(); win != nil {
					showMainWindow(win)
				}
			},
		},
		Services: []application.Service{
			application.NewService(appService),
			application.NewService(envSvc),
			application.NewService(storageSvc),
			application.NewService(dshSvc),
			application.NewService(aiSvc),
			application.NewService(updateSvc),
			application.NewService(clipSvc),
			application.NewService(diagSvc),
			application.NewService(systemSvc),
			application.NewService(workspaceSvc),
			application.NewService(todoSvc),
			application.NewService(monitorSvc),
			application.NewService(logtailSvc),
			application.NewService(notifySvc),
			application.NewService(recycleSvc),
			application.NewService(noteSvc),
			application.NewService(collectionSvc),
			application.NewService(frecencySvc),
			application.NewService(sceneSvc),
			application.NewService(snapshotSvc),
			application.NewService(itemSvc),
			application.NewService(portSvc),
			application.NewService(weatherSvc),
			application.NewService(mailboxSvc),
			application.NewService(shelfSvc),
			application.NewService(gitSvc),
			application.NewService(sshSvc),
			application.NewService(mcpSvc),
			application.NewService(pluginSvc),
			application.NewService(notifier),
			application.NewService(kvStore),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	}

	applyPlatformAppOptions(&opts)
	app := application.New(opts)

	// 先把插件共享资源（common.css / common.js）同步到 ~/.quickdock/plugins/builtin/
	// 必须早于插件加载，确保宿主注入的兼容样式/脚本是最新版本。
	// 放在 application.New 之后：二次启动已在 New 内部退出，避免与首实例并发写插件目录。
	extractSharedPluginAssets(pluginMgr, &sharedPluginAssets)

	// 插件扫描与加载不在这里：磁盘目录只代表「曾经被放到这里」，真正的启用状态在数据库里，
	// 而数据库要到 ServiceStartup 才打开。改由 services/lifecycle.go 在 DB 就绪后按启用列表加载。

	// 传入 App 引用给 AppService
	appService.SetApp(app)

	// 启动本地 AI 流式服务（127.0.0.1 随机端口，供前端 fetch 流式读取）
	aiSvc.StartAIStreamServer()

	// 初始化自动更新器（GitHub Releases Provider）
	if err := initUpdater(app, appVersion); err != nil {
		logger.E("更新器初始化失败（非关键错误，自动更新不可用）: %v", err)
	} else {
		// 核对上一次就地替换更新是否真的把版本提上去（防"反复提示同一个装不上的版本"）
		updatesvc.ResolvePendingUpdate(appVersion)
	}

	// 启动后台定时检查（仅"检查+通知"，下载/重启复用手动检测路径）
	updateSvc.StartAutoUpdateChecker()

	// 注入版本号到 AppService（供前端获取）
	appService.AppVersion = appVersion

	// 注入通知服务引用（供待办提醒调度器使用）
	appService.Notifier = notifier

	// 浮窗位置记忆：windows.go 的延迟工厂在创建窗口时读取、失焦/退出时保存。
	// 必须在 app.Run() 前赋值——kvstore 的数据在 ServiceStartup 时才加载，
	// 而浮窗均为懒创建（Run 之后），时序安全。
	winPosKV = kvStore

	// 创建插件窗口管理器（需要 app 引用，只能放在 New 之后）
	appService.PluginWindowMgr = plugin.NewPluginWindowManager(app, pluginMgr, uiZoom)

	// 注入窗口可见性判定，供插件管理器闲置自动降级时跳过「正打开」的插件。
	pluginMgr.SetWindowVisibleChecker(appService.PluginWindowMgr.IsWindowVisible)

	// 创建主窗口（启动时恢复上次记住的尺寸/位置）
	mainRect := loadWindowState()
	mainOpts := application.WebviewWindowOptions{
		Title:  appTitle,
		Width:  windowPx(appWidth),
		Height: windowPx(appHeight),
		// 最小尺寸同样按档位放大：否则 zoom 1.2 时用户能把窗口拖到 CSS 视口
		// 800/1.2 = 667，一票 680~780px 的弹层直接溢出屏幕。
		MinWidth:         windowPx(appMinWidth),
		MinHeight:        windowPx(appMinHeight),
		Frameless:        false,
		BackgroundColour: application.RGBA{Red: 27, Green: 27, Blue: 27, Alpha: 255},
		URL:              "/",
		// 文件置物架需要真实绝对路径：WebView2 下 HTML5 dataTransfer.files 只给文件名，
		// 路径只能由宿主的 WindowFilesDropped 事件传回来。只有带
		// data-file-drop-target 的元素会触发，不会干扰其它页面的内部拖拽排序。
		EnableFileDrop: true,
		// 整体等比缩放（值见常量 uiZoom，主窗口与所有独立浮窗共用同一系数）。
		Zoom: uiZoom,
	}
	if mainRect != nil {
		// 记录里是逻辑尺寸，还原成当前档位下的 DIP。
		mainOpts.Width = windowPx(mainRect.Width)
		mainOpts.Height = windowPx(mainRect.Height)
		mainOpts.X = mainRect.X
		mainOpts.Y = mainRect.Y
	}
	// 按档位放大后的初始尺寸可能超出小屏工作区（1.2 档 = 1320 宽，1280 的屏放不下）。
	// 收到工作区的 95%，但不得低于最小尺寸——低于 min 时 Wails 会自己夹，窗口比例
	// 会变得很怪，不如让超宽那侧由系统去处理。
	if scr := app.Screen.GetPrimary(); scr != nil && scr.WorkArea.Width > 0 {
		wa := scr.WorkArea
		if maxW := wa.Width * 95 / 100; mainOpts.Width > maxW && maxW >= mainOpts.MinWidth {
			mainOpts.Width = maxW
		}
		if maxH := wa.Height * 95 / 100; mainOpts.Height > maxH && maxH >= mainOpts.MinHeight {
			mainOpts.Height = maxH
		}
	}
	// 主窗口用用户选的档位（浮窗在 applyWindowPlatformOptions 里统一用 Acrylic）。
	// off 时是 no-op，不会动 BackgroundType / Windows，保持原有实色配置。
	applyWindowBackdrop(&mainOpts, false)
	mainWindow := app.Window.NewWithOptions(mainOpts)

	// 保存主窗口引用供 tray.go 使用
	SetMainWindow(mainWindow)
	appService.MainWindow = mainWindow
	// 让插件窗口创建时对齐到主窗口所在显示器（多屏场景）
	appService.PluginWindowMgr.SetMainWindow(mainWindow)

	// 窗口尺寸/位置记忆：移动或缩放结束后（防抖）把矩形写入配置文件，
	// 下次启动 loadWindowState() 恢复。最大化/最小化态不记录，避免恢复成畸形窗口。
	mainWindow.RegisterHook(events.Common.WindowDidResize, func(*application.WindowEvent) {
		scheduleSaveWindowState(mainWindow)
	})
	mainWindow.RegisterHook(events.Common.WindowDidMove, func(*application.WindowEvent) {
		scheduleSaveWindowState(mainWindow)
	})

	// 窗口关闭时隐藏到托盘（而不是退出）
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !trayQuitRequested.Load() {
			windowFlags.Main.Store(false)
			windowFlags.Clipboard.Store(false)
			event.Cancel()
			go mainWindow.Hide()
		}
	})

	// 同步窗口可见状态
	mainWindow.RegisterHook(events.Common.WindowMinimise, func(event *application.WindowEvent) {
		windowFlags.Main.Store(false)
	})
	mainWindow.RegisterHook(events.Common.WindowRestore, func(event *application.WindowEvent) {
		windowFlags.Main.Store(true)
	})

	// 主窗口首次显示后，才在后台对账拉起「已开启常驻」的本地服务（redis / caddy / php ...）。
	// 这些服务启动前的探测与校验（端口占用 / caddy validate / 版本扫描）全是子进程调用，
	// 串行耗时 7s 以上；原先同步跑在 SetApp 里、也就是 app.Run() 之前，把主窗口显示拖到 16s。
	// 拆开后主界面先可用，本地服务随后在后台按期望态补齐。
	// 用 sync.Once：WM_SHOWWINDOW 在每次显示（含最小化还原）都会发，只认首次。
	var envServicesOnce sync.Once
	mainWindow.RegisterHook(events.Common.WindowShow, func(*application.WindowEvent) {
		envServicesOnce.Do(func() {
			logger.I("[启动] 主窗口已显示，转后台对账启动本地服务")
			appService.StartEnvServicesAsync()
		})
	})

	// 文件置物架：只认拖到 #qd-shelf-drop 上的文件。插件页拖 zip 安装、AI 页拖图片
	// 附件都走各自的 HTML5 通道，这些地方不能顺手把路径塞进置物架。
	mainWindow.RegisterHook(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		details := event.Context().DropTargetDetails()
		if details == nil {
			return
		}
		// ElementID 是主判据；ClassList 兜底，避免不同平台上 Wails 回填的 id 为空。
		inShelf := details.ElementID == "qd-shelf-drop"
		for _, cls := range details.ClassList {
			if cls == "qc-drop" {
				inShelf = true
				break
			}
		}
		if !inShelf {
			return
		}
		files := event.Context().DroppedFiles()
		if len(files) == 0 {
			return
		}
		if res := shelfSvc.AddPaths(files); res != nil && res.Code != 0 {
			logger.W("[WRN] shelf drop: %s", res.Msg)
			return
		}
		app.Event.Emit("shelf:changed")
	})

	// 剪贴板/命令面板/插件窗口使用延迟创建（按需初始化，减少启动内存占用）
	// 将延迟工厂函数注入 AppService，供前端 Wails 绑定调用
	InjectWindowGetters(appService, app)

	// 三个 WebView2 浮窗改为「首次用到才创建」。原先在 app.Run() 之前同步预建并
	// Hide，是为了绕开热键回调 goroutine 上跨线程建窗的死锁/白屏；代价是每个渲染
	// 进程空闲也常驻 30–80MB，一开机就多占 100–240MB，而多数会话根本用不到命令
	// 面板或快捷笔记。跨线程约束现在由 windows.go 的 newFloatingWindowOnMainThread
	// 承担（投递到主线程 + 同步等结果），预建不再是必需。
	//
	// 想要「热键零延迟」时可设 QD_PRECREATE_WINDOWS=1：仍然挪到 app.Run() 之后、
	// 主线程空闲时错峰创建，不阻塞首屏，也不在 Run 前碰 WebView2。
	if os.Getenv("QD_PRECREATE_WINDOWS") == "1" {
		precreateFloatingWindows(appService)
	}

	// 预创建截图覆盖窗口（原生 Win32 分层窗口，不走 WebView）。
	// 放在 app.Run() 之前的原因同上面的浮窗：预创建后热键触发路径只剩
	// 「抓屏 + 显示窗口」，不必在热键回调 goroutine 上承担建窗开销。
	services.InitScreenshotOverlay()
	// 贴图钉屏的右键菜单要用到宿主能力（剪贴板 / 原生保存对话框），注入进去。
	// 钉图窗口是纯 GDI 的原生窗口，自己在独立线程上跑消息泵。
	services.InitScreenshotPins(app)

	// 渲染进程看门狗：先于 Run 启动，给前端提供自愈所需的心跳信号。
	heartbeatQuit := make(chan struct{})
	startRenderHeartbeat(app, heartbeatQuit)

	// 运行应用
	// 注意：Wails 框架在创建首个 WebView2 环境时会打印笼统的
	// "[WebView2] Environment created successfully"，此处补充宿主侧细节日志，
	// 让启动信息更具可读性（数据目录 / 版本 / 优化参数 / 窗口计划 / 屏幕数）。
	logStartupDetail(app, mainOpts, mainRect)

	err := app.Run()
	// Run 返回即主窗口已销毁：先停心跳，否则它还会对着已销毁窗口 Emit 到进程结束。
	close(heartbeatQuit)
	if err != nil {
		// 启动失败必须让用户「当场」看到，而不只是躺在日志里。
		//
		// 三条通道各管一段，互不替代：
		//   ① logger.E → 日志文件（排障的事实来源，带时间戳与上下文）
		//   ② stderr   → dev 构建有真实控制台，由 logger 的 tee 自动带走
		//   ③ 原生弹窗 → 正式版是 -H windowsgui，没有控制台；不弹窗的话
		//      用户看到的就是「双击了，窗口没起来，也没有任何提示」。
		// v0.0.25 的迁移失败正是这样被漏掉的：控制台空白，只有翻 log 才知道
		// 是哪条 SQL 报错。
		logger.E("应用运行失败: %v", err)
		sysutil.ShowFatalError("QuickDock 无法启动",
			fmt.Sprintf("%v\n\n详细日志：\n%s", err, logger.LogDir()))
		// 不调用 log.Fatal，确保下面的 ShutdownAll 执行
	}

	// 标记窗口态保存已停用：app.Run 返回后主窗口将被销毁，延迟的 timer 回调若仍触发
	// 会操作已销毁窗口 → panic。Stop 双重保险（guard 已能挡住绝大多数情况）。
	windowStateQuit.Store(true)
	windowStateMu.Lock()
	if windowStateSaveTimer != nil {
		windowStateSaveTimer.Stop()
	}
	windowStateMu.Unlock()

	// 退出清理（安全网）：停止 AI 流式服务、关闭所有插件进程与窗口。
	// 与 AppService.ServiceShutdown 中的 ShutdownAll/CloseAll 形成双重保险——
	// 两条退出路径（托盘退出 vs v3 生命周期）各自兜底；ShutdownAll/CloseAll 内部
	// 以 guard 保证幂等，重复调用安全无副作用，故此处与 ServiceShutdown 重复是有意设计。
	aiSvc.StopAIStreamServer()
	pluginMgr.ShutdownAll()
	// 关闭 SSH 终端会话：会话常驻在 Go 侧（关窗口不断连），
	// 进程退出时必须主动收掉，否则远端会残留一串没断的 shell 进程。
	sshSvc.Shutdown()
	if appService.PluginWindowMgr != nil {
		appService.PluginWindowMgr.CloseAll()
	}
	// env 服务（redis / caddy / nginx / php-cgi ...）是独立子进程，不随宿主退出而结束，
	// 主动停止以免留下孤儿（孤儿会被下次启动的对账误判为「已在运行」而永久脱管）。
	// 与 ServiceShutdown 中的调用互为安全网，StopAllOnExit 幂等。
	if appService.Env != nil {
		appService.Env.StopAllOnExit()
	}
}

// initUpdater 初始化 Wails 自动更新器（endpoint provider + Ed25519 签名验证）
//
// 安全模型：
//
//	CI 用 Ed25519 私钥（仓库 Secret UPDATER_PRIVATE_KEY）对裸二进制签名，
//	产出 manifest.json（含 sha512 摘要 + ed25519ph 签名），随每个 release 发布。
//	应用经 GitHub 稳定地址 releases/latest/download/manifest.json 拉取该 manifest，
//	用编译期内嵌的公钥（updater.key.pub，见文件顶部 //go:embed）验签——
//	fail closed：签名或摘要不符直接报错、不安装。
//	这能防"发布账号 / CI 被攻破"级别的供应链攻击：攻击者即便替换了 release 资产，
//	没有私钥也伪造不出有效签名，更新会被拒绝。（相较于此前的 SHA256 校验和，
//	校验和只能防传输损坏 / 下错文件，挡不住发布者被冒充。）
func initUpdater(app *application.App, version string) error {
	// 自定义 HTTP 客户端：
	// 1) 沿用 Go 默认传输层各项超时（连接/TLS 握手等）。
	// 2) 代理优先级：环境变量 HTTP_PROXY/HTTPS_PROXY/NO_PROXY 优先；
	//    未配置时回退读取 Windows 系统代理注册表（Clash/v2rayN 等工具的"系统代理"模式
	//    只写 WinINET 注册表、不设环境变量，Go 的 ProxyFromEnvironment 默认读不到）。
	// 3) 不设整体 Timeout（默认 30s 会把大体积安装包下载直接掐断），
	//    真正的超时由调用方传入的 ctx 控制（见 services/update.go）。
	// 自定义 HTTP 传输层：沿用 Go 默认传输层的各项超时。
	// http.DefaultTransport 默认为 *http.Transport，但可能被第三方库替换；用类型断言失败兜底新建，
	// 避免非 *http.Transport 时断言 panic（此前若被替换会直接崩在启动期）。
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	transport.Proxy = updaterProxyFunc
	httpClient := &http.Client{Transport: transport, Timeout: 0}

	// endpoint provider：从签名 manifest 拉取更新信息并验签。
	// 镜像重试由 NewMirrorUpdaterProvider 包装（国内网络加速），Check/Download
	// 直连失败都会自动走镜像重试。
	ep, err := endpoint.New(endpoint.Config{
		URL:        updatesvc.ManifestURL,
		HTTPClient: httpClient,
	})
	if err != nil {
		return fmt.Errorf("创建 endpoint provider 失败: %w", err)
	}

	// 用镜像 provider 包装：直连 GitHub 失败时自动尝试加速镜像（国内网络）。
	// 签名验证不受影响（镜像只改传输 URL，无法篡改内容）。
	provider := updater.Provider(updatesvc.NewMirrorUpdaterProvider(ep, httpClient, updatesvc.ManifestURL))

	return app.Updater.Init(updater.Config{
		CurrentVersion: version,
		Providers:      []updater.Provider{provider},
		PublicKey:      updaterPublicKey,
		// 不启用 Wails 内置周期检查：它会走 CheckAndInstall 自动下载却永不重启
		// （Restart 仅由 Wails 内置窗口触发，本项目用自定义 UI）。后台"检查+通知"
		// 改由 AppService.StartAutoUpdateChecker 驱动，复用手动检测的同一套 UI 与重启逻辑。
		CheckInterval: 0,
	})
}

// extractSharedPluginAssets 把插件共享资源（common.css / common.js）增量同步到
// ~/.quickdock/plugins/builtin/，供早期安装的外部插件引用——它们的前端仍写着
// ../builtin/common.css，新版插件自带 qd-theme.css，不再依赖它。
//
// 在插件加载（services/lifecycle.go 按 DB 启用列表扫描）之前调用，确保宿主注入的
// 兼容样式/脚本是最新版本；仅覆盖内容变化的文件。
//
// 这里不再有内置插件：内置插件方案搁置，插件一律来自市场或本地 zip 安装，
// 因此本函数只同步共享资源，不注册也不加载任何插件。
func extractSharedPluginAssets(mgr *plugin.Manager, sharedFS *embed.FS) {
	// 目标目录沿用 builtin 这个名字：它已经写进历史插件前端的 <link> 路径，
	// 改名会让已装插件丢样式。仓库侧源目录已改为 plugins/shared。
	sharedDir := filepath.Join(mgr.PluginsDir(), "builtin")
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		logger.W("QuickDock: 创建插件共享资源目录失败: %v", err)
		return
	}
	for _, name := range []string{"common.css", "common.js"} {
		data, err := sharedFS.ReadFile(path.Join("plugins/shared", name))
		if err != nil {
			logger.W("QuickDock: 读取共享资源 %s 失败: %v", name, err)
			continue
		}
		if err := syncEmbeddedFile(filepath.Join(sharedDir, name), data); err != nil {
			logger.W("QuickDock: 同步共享资源 %s 失败: %v", name, err)
		}
	}
}

// syncEmbeddedFile 写入文件，内容与本地一致时跳过（避免每次启动全量重写磁盘）
func syncEmbeddedFile(dstPath string, data []byte) error {
	if cur, err := os.ReadFile(dstPath); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	return os.WriteFile(dstPath, data, 0644)
}
