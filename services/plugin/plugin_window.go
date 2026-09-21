package plugin

// ===== 插件窗口管理 =====

// SetPendingPluginInit 保存待注入的初始文本和命令（从命令面板跨窗口传递），带插件 id 归属。
// 归属随 init 一起记录，避免独立窗口/内联在快速连开时跨插件错配。
func (p *PluginService) SetPendingPluginInit(pluginID, text, command string) {
	p.pendingInitTextMu.Lock()
	defer p.pendingInitTextMu.Unlock()
	p.pendingInitPlugin = pluginID
	p.pendingInitText = text
	p.pendingInitCommand = command
}

// GetAndClearPendingPluginInit 取出并清除待注入的初始文本和命令。
// 仅当归属插件与传入的 pluginID 匹配时才取用并清除；不匹配则返回空、保留待注入数据，
// 供正确的插件窗口日后消费。
func (p *PluginService) GetAndClearPendingPluginInit(pluginID string) (text, command string) {
	p.pendingInitTextMu.Lock()
	defer p.pendingInitTextMu.Unlock()
	if p.pendingInitPlugin != pluginID {
		return "", ""
	}
	text = p.pendingInitText
	command = p.pendingInitCommand
	p.pendingInitPlugin = ""
	p.pendingInitText = ""
	p.pendingInitCommand = ""
	return
}

// ShowPluginWindow 显示插件窗口。
// 在命令面板模式下（PaletteMode=true）使用面板浮层（任务栏隐藏，贴合启动器「随手一开」心智）；
// 在主窗口/插件管理页等常规入口则使用独立窗口（任务栏可见、可最小化召回，避免「打开后点别的就丢」）。
func (p *PluginService) ShowPluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	title := pluginID
	if p.App.PluginMgr != nil {
		if inst := p.App.PluginMgr.GetPlugin(pluginID); inst != nil {
			title = inst.Manifest.Name
		}
	}
	if p.App.Flags.Palette.Load() {
		p.App.PluginWindowMgr.ShowInPanel(pluginID, title)
	} else {
		p.App.PluginWindowMgr.ShowAsWindow(pluginID, title)
	}
}

// HidePluginWindow 隐藏指定插件的窗口
func (p *PluginService) HidePluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	p.App.PluginWindowMgr.Hide(pluginID)
}

// ClosePluginWindow 关闭并销毁插件窗口（插件窗口标题栏的关闭按钮）。
//
// 与 HidePluginWindow 的分工：Hide 保活复用（隐藏 + 10 分钟延迟回收）；Close 立即销毁
// 窗口并停插件进程。用户点 X 是明确不要了，没有复用价值，不该让该窗口的 WebView2
// renderer（实测 60~130 MB）继续常驻到超时。
func (p *PluginService) ClosePluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	p.App.PluginWindowMgr.Close(pluginID)
}

// MinimizePluginWindow 最小化指定插件的窗口
func (p *PluginService) MinimizePluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	p.App.PluginWindowMgr.Minimize(pluginID)
}

// ToggleMaximizePluginWindow 切换指定插件的窗口最大化/还原
func (p *PluginService) ToggleMaximizePluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	p.App.PluginWindowMgr.ToggleMaximize(pluginID)
}
