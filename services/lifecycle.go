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
	openStart := time.Now()
	database, err := db.Open(dbPath)
	if err != nil {
		logger.E("[db] 打开失败 path=%s 耗时=%v: %v", dbPath, time.Since(openStart).Round(time.Millisecond), err)
		return err
	}
	a.DB = database
	// 合并原「正在打开」+「已打开」两条：成功路径只留一条，带上路径与耗时（迁移耗时是大头）
	logger.I("[db] 已就绪 path=%s 耗时=%v", dbPath, time.Since(openStart).Round(time.Millisecond))

	// 外键约束是本项目数据一致性的前提（级联删除全靠它），只在异常时告警。
	// 原实现把 foreign_keys=1 当常态信息逐次打印，属于噪音。
	if fkRow, ferr := database.QueryOne("PRAGMA foreign_keys"); ferr != nil {
		logger.W("[db] PRAGMA foreign_keys 检查失败: %v", ferr)
	} else if v := fmt.Sprint(fkRow["foreign_keys"]); v != "1" {
		logger.W("[db] PRAGMA foreign_keys = %s（期望 1）：级联删除与约束校验将失效", v)
	}

	// 确保默认工作空间存在
	workspaces, err := a.DB.ListWorkspaces()
	if err != nil {
		return fmt.Errorf("工作空间列表获取失败: %w", err)
	}

	if len(workspaces) == 0 {
		ws, err := a.DB.CreateWorkspace(DefaultWorkspaceName)
		if err != nil {
			logger.W("QuickDock: 创建默认工作空间失败: %v", err)
		} else {
			logger.I("QuickDock: 默认工作空间已创建, id=%v", ws.ID)
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
	if a.InstallBuiltinPluginsFn != nil && a.PluginMgr != nil {
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

	// 启动剪贴板捕获工作协程：WM_CLIPBOARDUPDATE 回调只置信号，读取/解析/入库都在
	// 这条协程上做（详见 clipboard_sys.go 的 startClipboardWorker）。必须早于
	// StartHotkeyListenerFn——监听一旦启动就会立刻收到变更通知。
	a.startClipboardWorker(a.ClipboardCtx)

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
//
// 退出是全应用最容易「静默卡死 / 留下孤儿进程」的链路：插件子进程、env 服务、
// dsh 进程都不随宿主自动结束。此处按阶段记录耗时，出问题时能直接看出卡在哪一步。
func (a *AppService) ServiceShutdown() error {
	exitStart := time.Now()
	mark := exitStart
	logger.I("[退出] 开始清理：插件=%v 插件窗口=%v 本地服务=%v dsh=%v 调度器=%v",
		a.PluginMgr != nil, a.PluginWindowMgr != nil, a.Env != nil, a.DSH != nil, a.schedulerQuit != nil)

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
	logger.I("[退出] 插件与插件窗口已关闭 耗时=%v", time.Since(mark).Round(time.Millisecond))
	mark = time.Now()

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
	logger.I("[退出] 本地服务与 dsh 已停止 耗时=%v", time.Since(mark).Round(time.Millisecond))
	mark = time.Now()

	// 先停三个常驻调度 goroutine（提醒/定时任务/网站监控），并等它们真正收尾。
	//
	// 只 close 通道是不够的：循环本体收到信号会立即返回，但此前派生的执行协程
	// 仍在访问 DB。DB 一关，它们写回执行结果就全部静默失败——用户看到的是
	// 「任务跑了，结果没记」。故必须 waitForSchedulers() 之后再 Close。
	if a.schedulerQuit != nil {
		a.StopSchedulers()
		a.waitForSchedulers()
	}
	// 取消剪贴板监听上下文，终止正在进行的剪贴板处理 goroutine
	if a.ClipboardCancel != nil {
		a.ClipboardCancel()
	}
	// 最后关闭数据库
	if a.DB != nil {
		a.DB.Close()
	}
	logger.I("[退出] 调度器/剪贴板/数据库已停止 耗时=%v；清理总耗时=%v",
		time.Since(mark).Round(time.Millisecond), time.Since(exitStart).Round(time.Millisecond))
	return nil
}
