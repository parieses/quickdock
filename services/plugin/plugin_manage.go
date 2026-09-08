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
	if err := p.App.DB.SetPluginEnabled(id, 1); err != nil {
		// 数据库更新失败：回滚已加载的插件进程
		_ = p.App.PluginMgr.StopPlugin(id)
		return services.Fail(err)
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
	if err := p.App.DB.SetPluginEnabled(id, 0); err != nil {
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
func (p *PluginService) KillPlugin(id string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
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
	p.recordPluginExecLog(pluginID, commandID, "hotkey", start, result, err)
	if err != nil {
		logger.E("插件 %s 命令 %s 执行失败: %v", pluginID, commandID, err)
	} else if result != nil {
		logger.I("插件 %s 命令 %s 执行成功", pluginID, commandID)
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

func (p *PluginService) UninstallPlugin(id string) *services.ApiResult {
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	p.App.PluginMgr.UnloadPlugin(id)
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
	// 清理数据库记录和数据
	if err := p.App.DB.DeletePlugin(id); err != nil {
		return services.Fail(err)
	}
	if err := p.App.DB.CleanPluginData(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
