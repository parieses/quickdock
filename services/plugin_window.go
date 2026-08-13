package services

// ===== 插件窗口管理 =====

// SetPendingPluginInit 保存待注入的初始文本和命令（从命令面板跨窗口传递）
func (a *AppService) SetPendingPluginInit(text, command string) {
	a.pendingInitTextMu.Lock()
	a.pendingInitText = text
	a.pendingInitCommand = command
	a.pendingInitTextMu.Unlock()
}

// GetAndClearPendingPluginInit 取出并清除待注入的初始文本和命令
func (a *AppService) GetAndClearPendingPluginInit() (text, command string) {
	a.pendingInitTextMu.Lock()
	defer a.pendingInitTextMu.Unlock()
	text = a.pendingInitText
	command = a.pendingInitCommand
	a.pendingInitText = ""
	a.pendingInitCommand = ""
	return
}

// ShowPluginWindow 在面板中显示插件窗口（任务栏隐藏）
func (a *AppService) ShowPluginWindow(pluginID string) {
	if a.PluginWindowMgr == nil {
		return
	}
	title := pluginID
	if a.PluginMgr != nil {
		if inst := a.PluginMgr.GetPlugin(pluginID); inst != nil {
			title = inst.Manifest.Name
		}
	}
	a.PluginWindowMgr.ShowInPanel(pluginID, title)
}

// HidePluginWindow 隐藏指定插件的窗口
func (a *AppService) HidePluginWindow(pluginID string) {
	if a.PluginWindowMgr == nil {
		return
	}
	a.PluginWindowMgr.Hide(pluginID)
}

// MinimizePluginWindow 最小化指定插件的窗口
func (a *AppService) MinimizePluginWindow(pluginID string) {
	if a.PluginWindowMgr == nil {
		return
	}
	a.PluginWindowMgr.Minimize(pluginID)
}

// ToggleMaximizePluginWindow 切换指定插件的窗口最大化/还原
func (a *AppService) ToggleMaximizePluginWindow(pluginID string) {
	if a.PluginWindowMgr == nil {
		return
	}
	a.PluginWindowMgr.ToggleMaximize(pluginID)
}
