package services

// ===== 插件窗口管理 =====

// SetPendingPluginInit 保存待注入的初始文本和命令（从命令面板跨窗口传递），带插件 id 归属。
// 归属随 init 一起记录，避免独立窗口/内联在快速连开时跨插件错配。
func (a *AppService) SetPendingPluginInit(pluginID, text, command string) {
	a.pendingInitTextMu.Lock()
	defer a.pendingInitTextMu.Unlock()
	a.pendingInitPlugin = pluginID
	a.pendingInitText = text
	a.pendingInitCommand = command
}

// GetAndClearPendingPluginInit 取出并清除待注入的初始文本和命令。
// 仅当归属插件与传入的 pluginID 匹配时才取用并清除；不匹配则返回空、保留待注入数据，
// 供正确的插件窗口日后消费。
func (a *AppService) GetAndClearPendingPluginInit(pluginID string) (text, command string) {
	a.pendingInitTextMu.Lock()
	defer a.pendingInitTextMu.Unlock()
	if a.pendingInitPlugin != pluginID {
		return "", ""
	}
	text = a.pendingInitText
	command = a.pendingInitCommand
	a.pendingInitPlugin = ""
	a.pendingInitText = ""
	a.pendingInitCommand = ""
	return
}

// ShowPluginWindow 显示插件窗口。
// 在命令面板模式下（PaletteMode=true）使用面板浮层（任务栏隐藏，贴合启动器「随手一开」心智）；
// 在主窗口/插件管理页等常规入口则使用独立窗口（任务栏可见、可最小化召回，避免「打开后点别的就丢」）。
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
	if a.PaletteMode != nil && a.PaletteMode.Load() {
		a.PluginWindowMgr.ShowInPanel(pluginID, title)
	} else {
		a.PluginWindowMgr.ShowAsWindow(pluginID, title)
	}
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
