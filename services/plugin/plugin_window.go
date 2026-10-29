package plugin

import (
	"fmt"
	"strings"
	"time"

	"quickdock/services"
)

// ===== 插件窗口管理 =====

// pendingInitTTL 待注入 init 的存活时长，超过即视为过期残留：丢弃而非返回。
// 防「槽位已设置但目标窗口在加载前被销毁 / 注入失败」后，陈旧 init 被该插件
// 日后某次 iframe onLoad 无意消费——表现为插件莫名带着上次分离时的输入。
// 正常链路（SetPendingPluginInit → ShowPluginWindow → 新窗口 onLoad）秒级完成，
// 60s 对慢机器（插件进程冷启动 + 前端页拉取）仍留有余量。
const pendingInitTTL = 60 * time.Second

// SetPendingPluginInit 保存待注入的初始文本和命令（从命令面板跨窗口传递），带插件 id 归属。
// 归属随 init 一起记录，避免独立窗口/内联在快速连开时跨插件错配。
func (p *PluginService) SetPendingPluginInit(pluginID, text, command string) {
	p.pendingInitTextMu.Lock()
	defer p.pendingInitTextMu.Unlock()
	p.pendingInitPlugin = pluginID
	p.pendingInitText = text
	p.pendingInitCommand = command
	p.pendingInitAt = time.Now()
}

// GetAndClearPendingPluginInit 取出并清除待注入的初始文本和命令。
// 仅当归属插件与传入的 pluginID 匹配时才取用并清除；不匹配则返回空、保留待注入数据，
// 供正确的插件窗口日后消费。归属匹配但超过 pendingInitTTL 未消费，按过期残留丢弃。
func (p *PluginService) GetAndClearPendingPluginInit(pluginID string) (text, command string) {
	p.pendingInitTextMu.Lock()
	defer p.pendingInitTextMu.Unlock()
	if p.pendingInitPlugin != pluginID {
		return "", ""
	}
	if p.pendingInitAt.IsZero() || time.Since(p.pendingInitAt) > pendingInitTTL {
		p.clearPendingInitLocked()
		return "", ""
	}
	text = p.pendingInitText
	command = p.pendingInitCommand
	p.clearPendingInitLocked()
	return
}

// clearPendingInitLocked 清空待注入槽位，调用方须持 pendingInitTextMu。
func (p *PluginService) clearPendingInitLocked() {
	p.pendingInitPlugin = ""
	p.pendingInitText = ""
	p.pendingInitCommand = ""
	p.pendingInitAt = time.Time{}
}

// ShowPluginWindow 显示插件窗口。
// 在命令面板模式下（PaletteMode=true）使用面板浮层（任务栏隐藏，贴合启动器「随手一开」心智）；
// 在主窗口/插件管理页等常规入口则使用独立窗口（任务栏可见、可最小化召回，避免「打开后点别的就丢」）。
func (p *PluginService) ShowPluginWindow(pluginID string) {
	p.showPluginWindow(pluginID, p.App.Flags.Palette.Load())
}

// showPluginWindow 显示插件窗口，standalone=true 时固定走独立窗口（任务栏可见）。
//
// 复用路径补 init 的逻辑集中在这里：窗口的 iframe 早已加载完，@load 不会再触发，
// pending init 无人消费——用户表现为「分离为独立窗口后数据没带过去，相当于重开了一个窗口」。
// 新建路径相反：iframe onLoad 会自行消费槽位（usePluginHost.usePendingInit），
// 这里若抢先取走，新窗口反而拿到空 init，故只处理复用路径。
func (p *PluginService) showPluginWindow(pluginID string, standalone bool) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	// 窗口标题用清单里的显示名；清单缺 name 时才退回 id（否则任务栏/Alt-Tab 会只剩 "快启坞 - "）。
	title := pluginID
	if p.App.PluginMgr != nil {
		if inst := p.App.PluginMgr.GetPlugin(pluginID); inst != nil {
			if name := strings.TrimSpace(inst.Manifest.Name); name != "" {
				title = name
			}
		}
	}
	// Show 返回 (窗口, 是否新创建)：false = 命中注册表复用已存在窗口。
	var created bool
	if standalone {
		_, created = p.App.PluginWindowMgr.ShowAsWindow(pluginID, title)
	} else {
		_, created = p.App.PluginWindowMgr.ShowInPanel(pluginID, title)
	}
	if !created {
		text, command := p.GetAndClearPendingPluginInit(pluginID)
		if text != "" || command != "" {
			p.App.PluginWindowMgr.InjectInitText(pluginID, text, command)
		}
	}
}

// OpenPluginWindowWith 经 MCP / AI 打开插件窗口，并把命令与参数交给插件前端。
//
// 这是纯前端插件（runtime=none）**唯一**能被外部驱动的路径：宿主不执行它们的命令，
// 只能把界面打开、把参数塞进去；对 native/goja 插件，它也补上了「执行完想看看结果」
// 这类只能在界面上完成的动作。
func (p *PluginService) OpenPluginWindowWith(pluginID, command, text string) *services.ApiResult {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return services.FailMsg("缺少 pluginId")
	}
	if p.App.PluginMgr == nil {
		return services.FailMsg("plugin manager not initialized")
	}
	inst := p.App.PluginMgr.GetPlugin(pluginID)
	if inst == nil {
		return services.FailMsg("插件不存在: " + pluginID)
	}
	// 显式禁用的插件不能经这条路复活：ensurePluginProcess 会吞掉 EnsureLoaded 的错误，
	// 不拦的话会开出一个「窗口在、进程无」的空壳，用户看到的是插件坏了。
	if inst.IsDisabled() {
		return services.FailMsg("插件已被禁用，请先在插件管理页启用: " + pluginID)
	}
	// 命令必须真实存在。传错的 command 若不拦，窗口照开、命令却没人处理，
	// 表现为「打开了但什么都没发生」——比直接报错难排查得多。
	if command != "" {
		found := false
		for _, c := range inst.Manifest.Commands {
			if c.ID == command {
				found = true
				break
			}
		}
		if !found {
			return services.FailMsg(fmt.Sprintf("插件 %s 没有命令 %q（可用命令见 plugin_list）", pluginID, command))
		}
	}
	p.SetPendingPluginInit(pluginID, text, command)
	// 固定独立窗口：AI 触发时用户可能停在任意界面，甚至面板是收起的；
	// 跟随 Flags.Palette 会出现「窗口开在隐匿的面板里」，任务栏与 Alt-Tab 都找不到。
	p.showPluginWindow(pluginID, true)
	return services.Ok(map[string]interface{}{"pluginId": pluginID, "command": command, "window": "standalone"})
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

// ForceClosePluginWindow 强制关闭（插件窗口标题栏「强制关闭」按钮入口）：
// 立即同步销毁窗口（用户体感即时，不等杀进程），杀进程放后台 goroutine。
//
// 为什么必须整体下沉后端：KillPlugin 全程持 m.mu 且要跑 taskkill /T + Process.Wait()
// + PID 文件写盘 + 目录锁扫描，耗时可达秒级。若前端 await KillPlugin 再关窗，窗口会
// 卡住这段时间才消失；若前端先关窗再调 KillPlugin，页面 JS 上下文已随窗口销毁、调用
// 发不出去（race）。故由后端先同步 Close（快路径），再 go KillPlugin（慢路径）。
//
// KillPlugin 语义：进程树终止 + 状态置回 registered（就绪），≠ 禁用；下次打开窗口 /
// 执行命令经 EnsureLoaded 自动拉起。
func (p *PluginService) ForceClosePluginWindow(pluginID string) {
	if p.App.PluginWindowMgr == nil {
		return
	}
	p.App.PluginWindowMgr.Close(pluginID)
	if p.App.PluginMgr == nil {
		return
	}
	go func() {
		_ = p.App.PluginMgr.KillPlugin(pluginID)
		// 杀进程失败仅忽略：多为进程已不在（曾崩溃/已退出），窗口反正已销毁
	}()
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
