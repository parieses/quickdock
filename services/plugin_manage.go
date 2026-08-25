package services

import (
	"strings"
	"time"

	"quickdock/internal/logger"
)

func (a *AppService) EnablePlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// 先加载插件：成功后再更新数据库，避免「库已启用但插件未加载」的状态不一致
	manifest, err := a.PluginMgr.ReloadPlugin(id)
	if err != nil {
		return Fail(err)
	}
	if err := a.DB.SetPluginEnabled(id, 1); err != nil {
		// 数据库更新失败：回滚已加载的插件进程
		_ = a.PluginMgr.StopPlugin(id)
		return Fail(err)
	}

	// 注册插件声明的热键：先清理旧的热键避免自冲突
	if a.PluginHotkeys != nil && manifest != nil {
		// 先注销该插件之前注册的所有热键（系统级 + 内部注册表）
		if a.app != nil {
			for _, accel := range a.PluginHotkeys.GetPluginAccels(id) {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
		a.PluginHotkeys.UnregisterAll(id)

		// 重新注册
		for _, cmd := range manifest.Commands {
			if cmd.Hotkey == "" {
				continue
			}
			accel := hotkeyStringToAccel(cmd.Hotkey)
			if err := a.PluginHotkeys.Register(accel, id, cmd.ID); err != nil {
				logger.W("插件 %s 热键 %s 注册失败: %v", id, accel, err)
			} else if a.app != nil {
				_ = a.app.GlobalShortcut.Register(accel, func() {
					a.executePluginCommand(id, cmd.ID)
				})
			}
		}
	}

	return Ok(manifest)
}

func (a *AppService) DisablePlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	// StopPlugin 停止进程但保留在列表中，禁用后仍然能看到并重新启用
	if err := a.PluginMgr.StopPlugin(id); err != nil {
		// 插件可能不在内存中（初次启动时 DB 禁用但未加载），这不是错误
		_ = err
	}
	if err := a.DB.SetPluginEnabled(id, 0); err != nil {
		return Fail(err)
	}

	// 清理插件热键（内部注册表 + 系统全局快捷键）
	if a.PluginHotkeys != nil {
		accels := a.PluginHotkeys.UnregisterAll(id)
		if a.app != nil {
			for _, accel := range accels {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
	}

	return Ok(nil)
}

// executePluginCommand 内部调用插件命令（供热键回调使用）
func (a *AppService) executePluginCommand(pluginID, commandID string) {
	start := time.Now()
	result, err := a.PluginMgr.ExecuteCommand(pluginID, commandID, nil)
	// 记录执行日志（5.2：忽略错误，不影响主流程）
	a.recordPluginExecLog(pluginID, commandID, "hotkey", start, result, err)
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

func (a *AppService) UninstallPlugin(id string) *ApiResult {
	if a.PluginMgr == nil {
		return FailMsg("plugin manager not initialized")
	}
	a.PluginMgr.UnloadPlugin(id)
	if err := a.PluginMgr.UninstallPlugin(id); err != nil {
		return Fail(err)
	}
	// 清理热键（内部注册表 + 系统全局快捷键）
	if a.PluginHotkeys != nil {
		accels := a.PluginHotkeys.UnregisterAll(id)
		if a.app != nil {
			for _, accel := range accels {
				_ = a.app.GlobalShortcut.Unregister(accel)
			}
		}
	}
	// 清理数据库记录和数据
	if err := a.DB.DeletePlugin(id); err != nil {
		return Fail(err)
	}
	if err := a.DB.CleanPluginData(id); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
