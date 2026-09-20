package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ServiceStartup 应用启动时调用（v3 生命周期）
func (a *AppService) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	// 创建剪贴板监听上下文，用于管理剪贴板处理 goroutine 的生命周期
	a.ClipboardCtx, a.ClipboardCancel = context.WithCancel(ctx)
	// 打开数据库
	dbDir := platform.DefaultDataDir()
	os.MkdirAll(dbDir, 0755)
	dbPath := filepath.Join(dbDir, "quickdock.db")
	logger.I("正在打开数据库 %s", dbPath)

	database, err := db.Open(dbPath)
	if err != nil {
		logger.E("数据库打开失败: %s", err.Error())
		return err
	}
	a.DB = database
	logger.I("数据库已打开")

	// 检查 PRAGMA 状态
	fkRow, err := database.QueryOne("PRAGMA foreign_keys")
	if err != nil {
		logger.W("PRAGMA 检查失败: %s", err.Error())
	} else {
		logger.I("PRAGMA foreign_keys = %v", fkRow["foreign_keys"])
	}

	// 确保默认工作空间存在
	workspaces, err := a.DB.ListWorkspaces()
	if err != nil {
		return fmt.Errorf("工作空间列表获取失败: %w", err)
	}
	logger.I("QuickDock: 找到 %d 个工作空间", len(workspaces))

	if len(workspaces) == 0 {
		ws, err := a.DB.CreateWorkspace(DefaultWorkspaceName)
		if err != nil {
			logger.W("QuickDock: 创建默认工作空间失败: %v", err)
		} else {
			logger.I("QuickDock: 默认工作空间已创建, id=%v", ws.ID)
		}
	} else {
		for _, w := range workspaces {
			logger.I("QuickDock: 工作空间 id=%v 名称=%v", w.ID, w.Name)
		}
	}

	// 确保默认工具存在
	if err := a.DB.EnsureDefaultTools(); err != nil {
		logger.W("QuickDock: 默认工具初始化失败: %v", err)
	}

	// 清理过期剪贴板条目（后台执行：大批量删除可能耗时数百毫秒~数秒，
	// 放启动主路径会拖慢首屏；DB 在本函数开头已打开，goroutine 内访问安全）。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.E("QuickDock: 剪贴板过期清理 goroutine panic: %v", r)
			}
		}()
		days, _ := a.DB.GetClipboardRetentionDays()
		if count, err := a.DB.DeleteExpiredClipboardEntries(days); err != nil {
			logger.W("QuickDock: 剪贴板过期清理失败: %v", err)
		} else if count > 0 {
			logger.I("QuickDock: 已清理 %d 条过期剪贴板记录", count)
		}
	}()

	// 注入插件 Host API 真实实现（剪贴板/通知/对话框/HTTP/存储）
	// 必须早于内置插件安装与启动，否则先跑起来的插件调用会撞上"未知的 host 方法"
	// 实现在 services/plugin 包，经注入式回调调用以避免 services ↔ 子包循环依赖
	if a.RegisterPluginHostFn != nil {
		a.RegisterPluginHostFn()
	}

	// 自动安装内置插件（main.go 注入的回调，需在 DB 就绪后执行）。
	// 必须在本段同步完成：内置插件首次运行才写入磁盘 + 注册 DB，若先异步扫描会漏掉这批刚安装的插件。
	if a.InstallBuiltinPluginsFn != nil {
		a.InstallBuiltinPluginsFn(a.PluginMgr, a.DB)
	}

	// 扫描并加载插件：只加载数据库中 enabled=1 的插件。改为后台 goroutine——
	// 此前 DiscoverAndLoad 内部 wg.Wait() 会阻塞本函数（=阻塞主窗口显示）：native 插件的
	// initialize 握手最坏 15s+，一次 http-client 握手超时曾把单次启动拖到 34s。挪到后台后
	// 首屏立即可用，插件随后在后台加载，首次使用时经 EnsureLoaded 惰性拉起。
	// 必须晚于 DB 就绪、Host API 注入与内置插件安装——否则未注册 / 刚安装的插件漏加载，
	// 或 native 插件启动即回调未注入的 host 方法撞「未知方法」。
	if a.PluginMgr != nil {
		enabledIDs, err := a.DB.ListEnabledPlugins()
		if err != nil {
			logger.W("QuickDock: 读取已启用插件列表失败，本次跳过插件加载: %v", err)
		} else {
			enabledSet := make(map[string]bool, len(enabledIDs))
			for _, id := range enabledIDs {
				enabledSet[id] = true
			}
			logger.I("QuickDock: 数据库已启用插件 %d 个，转入后台加载", len(enabledSet))
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.E("QuickDock: 插件后台加载 goroutine panic: %v", r)
					}
				}()
				if err := a.PluginMgr.DiscoverAndLoad(func(id string) bool { return enabledSet[id] }); err != nil {
					logger.W("QuickDock: 插件扫描加载失败（非关键）: %v", err)
				} else {
					logger.I("QuickDock: 插件后台加载完成")
				}
			}()
		}
	}

	// 设置全局 App 引用（供 SetClipboardText 等函数使用）
	AppRef.Store(a.app)

	// 启动全局快捷键和系统托盘（由 main 包注入的回调）
	a.Flags.Main.Store(true)
	if a.StartHotkeyListenerFn != nil {
		a.StartHotkeyListenerFn(a.app, a)
	}

	// 启动待办定时提醒调度器（常驻后台轮询，到期推送系统通知）
	a.StartReminderScheduler()

	// 启动定时任务调度器（精确定时器，到期执行 打开软件/目录/网址/命令/HTTP）
	a.StartScheduleRunner()

	// 启动网站监控检查器（仿 UptimeRobot，按 interval 定时探测并记录在线率）
	a.StartMonitorChecker()

	// dsh web 自动启动：QuickDock 起来后延迟 5s 在后台拉起 dsh 服务（默认开启，
	// 可在「环境管理 → DeepSeek Harness」用开关关闭）。只起服务不开窗口，用户点侧边栏 dsh
	// 时 OpenDSHWindow 直接复用。失败静默（如未安装/运行环境未就绪），不打扰 UI。
	if a.DSH != nil && a.dshAutoStartEnabled() {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.E("QuickDock: dsh 自动启动 goroutine panic: %v", r)
				}
			}()
			time.Sleep(5 * time.Second)
			if a.DSH.Running() {
				return // 已有服务（复用外部 dsh / 启动极快），无需再拉
			}
			if _, err := a.DSH.Start(); err != nil {
				logger.W("QuickDock: dsh 自动启动失败: %v", err)
			}
		}()
	}

	return nil
}

// ServiceShutdown 应用退出时调用（v3 生命周期）
func (a *AppService) ServiceShutdown() error {
	// 先停止所有插件并关闭插件窗口，避免它们在 DB 关闭后仍尝试写入，
	// 导致 panic 或数据丢失（原顺序先关 DB，而插件子进程/goja 仍在运行）。
	// 注意：main.go 的退出回调也会调用一次 ShutdownAll/CloseAll；此处为 v3 生命周期主路径，
	// 二者互为安全网，ShutdownAll/CloseAll 幂等，重复调用无副作用（详见 main.go 退出清理注释）。
	if a.PluginMgr != nil {
		a.PluginMgr.ShutdownAll()
	}
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.CloseAll()
	}
	// 停止本会话拉起的 env 服务（redis / caddy / nginx / php-cgi ...）。它们是独立子进程，
	// 不随宿主退出而结束——不主动停就会变成孤儿。放在插件之后：插件运行期可能仍要用到这些服务。
	// 与 main.go 的退出清理互为安全网，StopAllOnExit 幂等，重复调用无副作用。
	if a.Env != nil {
		a.Env.StopAllOnExit()
	}
	// 停止 dsh 子进程（若正在运行），防止残留 Node 占用端口
	if a.DSH != nil {
		a.DSH.Stop()
	}
	// 先停三个常驻调度 goroutine（提醒/定时任务/网站监控），避免 DB 关闭后它们仍触发访问
	if a.schedulerQuit != nil {
		a.StopSchedulers()
	}
	// 取消剪贴板监听上下文，终止正在进行的剪贴板处理 goroutine
	if a.ClipboardCancel != nil {
		a.ClipboardCancel()
	}
	// 最后关闭数据库
	if a.DB != nil {
		a.DB.Close()
	}
	return nil
}
