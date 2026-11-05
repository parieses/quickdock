package plugin

import "quickdock/services"

import (
	"strings"
	"time"

	"quickdock/internal/logger"
)

func (p *PluginService) EnablePlugin(id string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	// 先加载插件：成功后再更新数据库，避免「库已启用但插件未加载」的状态不一致
	manifest, err := p.App.PluginMgr.ReloadPlugin(id)
	if err != nil {
		return services.Fail(err)
	}
	affected, err := p.App.DB.SetPluginEnabled(id, 1)
	if err != nil {
		// 数据库更新失败：回滚已加载的插件进程
		_ = p.App.PluginMgr.StopPlugin(id)
		return services.Fail(err)
	}
	// 插件此前无 DB 记录（磁盘残留 / DB 损坏等孤儿）：启用必须把记录落库，
	// 否则下次启动又被当成禁用、回到「禁用后消失」的怪圈。
	if affected == 0 && manifest != nil {
		if ierr := p.App.DB.InsertPluginFull(id, manifest.Name, manifest.Version, manifest.Author,
			manifest.Description, manifest.Category, "", nil, nil, true); ierr != nil {
			logger.W("插件 %s 启用落库失败: %v", id, ierr)
		}
	}

	// 注册插件声明的热键：先清理旧的热键避免自冲突
	if p.App.PluginHotkeys != nil && manifest != nil {
		// 先注销该插件之前注册的所有热键（系统级 + 内部注册表）
		if p.App.App() != nil {
			for _, accel := range p.App.PluginHotkeys.GetPluginAccels(id) {
				_ = p.App.App().GlobalShortcut.Unregister(accel)
			}
		}
		p.App.PluginHotkeys.UnregisterAll(id)

		// 重新注册
		for _, cmd := range manifest.Commands {
			if cmd.Hotkey == "" {
				continue
			}
			accel := hotkeyStringToAccel(cmd.Hotkey)
			if err := p.App.PluginHotkeys.Register(accel, id, cmd.ID); err != nil {
				logger.W("插件 %s 热键 %s 注册失败: %v", id, accel, err)
			} else if p.App.App() != nil {
				_ = p.App.App().GlobalShortcut.Register(accel, func() {
					p.executePluginCommand(id, cmd.ID)
				})
			}
		}
	}

	return services.Ok(manifest)
}

func (p *PluginService) DisablePlugin(id string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	// StopPlugin 停止进程但保留在列表中，禁用后仍然能看到并重新启用
	if err := p.App.PluginMgr.StopPlugin(id); err != nil {
		// 插件可能不在内存中（初次启动时 DB 禁用但未加载），这不是错误
		_ = err
	}
	// 置内存禁用标记：阻止 EnsureLoaded 把已禁用插件经「惰性复活」静默拉起，
	// 否则内存 running 与 DB disabled 状态不一致（禁用形同虚设）。
	if inst := p.App.PluginMgr.GetPlugin(id); inst != nil {
		inst.SetDisabled(true)
	}
	if _, err := p.App.DB.SetPluginEnabled(id, 0); err != nil {
		return services.Fail(err)
	}

	// 清理插件热键（内部注册表 + 系统全局快捷键）
	if p.App.PluginHotkeys != nil {
		accels := p.App.PluginHotkeys.UnregisterAll(id)
		if p.App.App() != nil {
			for _, accel := range accels {
				_ = p.App.App().GlobalShortcut.Unregister(accel)
			}
		}
	}

	return services.Ok(nil)
}

// KillPlugin 强制终止插件进程（进程树 + 目录内孤儿），并停止其自动重启。
// 插件管理页「停止进程」用：插件锁住目录导致更新/卸载失败时，一键结束。
//
// 窗口生命周期跟着进程走：插件窗口只是后端进程的一个视图，进程被杀掉后还留着窗口
// 就是「页面在、进程无」的空壳，用户看到的仍是插件没关。先同步 Close 再杀进程，
// 顺序与 ForceClosePluginWindow 一致（后者是 Close + 后台 Kill）——杀目录孤儿进程
// 可能耗时秒级，先关窗口体感即时。未开窗口时 Close 是 no-op；WindowClosing 钩子只发
// best-effort 的 window:closed 通知，不会反过来再终止一次进程。
// 只在这里关：插件窗口「强制关闭」按钮走 ForceClosePluginWindow（它自己已 Close），
// 且它调的是 Manager.KillPlugin，不受此处影响，也不会误关用户刚重开的窗口。
func (p *PluginService) KillPlugin(id string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	if p.App.PluginWindowMgr != nil {
		p.App.PluginWindowMgr.Close(id)
	}
	if err := p.App.PluginMgr.KillPlugin(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// executePluginCommand 内部调用插件命令（供热键回调使用）
func (p *PluginService) executePluginCommand(pluginID, commandID string) {
	start := time.Now()
	result, err := p.App.PluginMgr.ExecuteCommand(pluginID, commandID, nil)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	p.recordPluginExecLog(pluginID, commandID, TriggerHotkey, start, result, err)
	if err != nil {
		logger.E("插件 %s 命令 %s 执行失败，耗时=%v: %v", pluginID, commandID, time.Since(start).Round(time.Millisecond), err)
	} else if result != nil {
		logger.I("插件 %s 命令 %s 执行成功，耗时=%v", pluginID, commandID, time.Since(start).Round(time.Millisecond))
	}
}

// hotkeyStringToAccel 将 "Ctrl+Shift+T" 转为 Wails Accelerator 格式 "Ctrl+Shift+T"
// Wails 的 Accelerator 格式与标准表示法一致
func hotkeyStringToAccel(hotkey string) string {
	parts := strings.Split(hotkey, "+")
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "win", "super", "cmd":
			parts[i] = "Super"
		default:
			// 非修饰键统一小写，确保 "Ctrl+T" 和 "Ctrl+t" 被视为同一热键
			parts[i] = strings.ToLower(p)
		}
	}
	return strings.Join(parts, "+")
}

// UninstallPlugin 卸载插件。keepData=true 时只删插件本体与注册记录，
// 保留私有数据 / 使用记录 / 执行日志——重装同 ID 插件可接回原数据
// （换机器迁移、临时排查问题重装时不至于把插件里的积累一并抹掉）。
func (p *PluginService) UninstallPlugin(id string, keepData bool) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	// 注：不在此处单独调用 UnloadPlugin(id)。Manager.UninstallPlugin 内部会先记录进程
	// PID、再 UnloadPlugin（停进程并同步等待其真正退出）后才删目录。若这里先 UnloadPlugin，
	// 实例已被移出 m.plugins，Manager 端拿不到 PID → 无法等待进程退出 → Windows 上残留
	// 的 exe 句柄会让目录删除报 "Access is denied"。
	if err := p.App.PluginMgr.UninstallPlugin(id); err != nil {
		return services.Fail(err)
	}
	// 清理热键（内部注册表 + 系统全局快捷键）
	if p.App.PluginHotkeys != nil {
		accels := p.App.PluginHotkeys.UnregisterAll(id)
		if p.App.App() != nil {
			for _, accel := range accels {
				_ = p.App.App().GlobalShortcut.Unregister(accel)
			}
		}
	}
	if keepData {
		// 保留数据：只清注册行与文件本体。usage_frecency 里的「最近使用」记录
		// 也一并保留——重装后命令面板排序接得上旧习惯，正是保留数据的目的之一。
		if err := p.App.DB.DeletePlugin(id); err != nil {
			return services.Fail(err)
		}
		return services.Ok(nil)
	}
	// 清理数据库痕迹（记录 / 私有数据 / 使用记录 / 执行日志）
	if err := p.App.DB.PurgePlugin(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
