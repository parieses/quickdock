package mcp

// 插件域工具：列清单、执行插件命令。
//
// 这组工具是「一层壳」：能力边界完全由插件自己声明，宿主只负责把调用转过去
// 并挡掉不该发生的调用（如 runtime=none 的纯前端插件跑后端命令）。
//
// 刻意不提供 enable / disable / uninstall：插件安装状态是用户的决定。
// AI 关掉一个插件，命令面板当场失灵，而用户不知道是谁干的；卸载更是不可逆。
// 这些动作留在插件管理页由人点。

import (
	"errors"
	"fmt"

	mcpsrv "quickdock/internal/mcp"
)

func registerPluginTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		return unwrap(s.Plugin.ListPlugins())
	}, mcpsrv.LevelRead, "plugin_list", "列出已安装插件及其命令、后端运行时（native/goja/none）。让 AI 知道能进一步调用哪个命令", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		pluginID := mcpsrv.Arg(args, "pluginId")
		commandID := mcpsrv.Arg(args, "command")
		if pluginID == "" {
			return nil, errors.New("缺少参数 pluginId（用 plugin_list 获取）")
		}
		if commandID == "" {
			return nil, errors.New("缺少参数 command（用 plugin_list 查看该插件有哪些命令）")
		}
		// runtime=none 的插件没有后端：宿主执行只会返回「假成功」，命令实际由插件自己的前端处理。
		// 这里直接拒绝，避免 AI 误以为调用真的生效（返回值不可用于判断执行结果）。
		if rt := s.pluginRuntime(pluginID); rt == "none" {
			return nil, fmt.Errorf("插件 %s 是纯前端插件（runtime=none），命令由插件界面自行处理，无法经 MCP 调用", pluginID)
		}
		input := map[string]interface{}{}
		if txt := mcpsrv.Arg(args, "input"); txt != "" {
			input["text"] = txt
		}
		return unwrap(s.Plugin.ExecutePluginCommand(pluginID, commandID, input))
	}, mcpsrv.LevelWrite, "plugin_execute", "执行某个已安装插件的命令（先用 plugin_list 查插件 id 与 command id）。runtime=none 的纯前端插件无法经 MCP 调用，会被直接拒绝", []string{"pluginId", "command"},
		map[string]any{
			"pluginId": str("插件 ID，如 io.github.parieses.port-scanner"),
			"command":  str("命令 ID，来自 plugin_list 的 commands"),
			"input":    str("可选：传给插件的输入文本"),
		})
}
