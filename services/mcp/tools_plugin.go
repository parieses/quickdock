package mcp

// 插件域工具：列清单、执行插件命令、带参打开插件界面、回看执行记录。
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
	"strings"

	mcpsrv "quickdock/internal/mcp"
	pluginsvc "quickdock/services/plugin"
)

func registerPluginTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		detail := strings.TrimSpace(mcpsrv.Arg(args, "detail"))
		if detail == "" {
			detail = "brief"
		}
		if detail != "brief" && detail != "full" {
			return nil, errors.New(`detail 只能是 "brief"（默认，精简）或 "full"（含权限与用量）`)
		}
		return unwrap(s.Plugin.PluginCatalog(detail))
	}, mcpsrv.LevelRead, "plugin_list",
		"列出已安装插件及其可用命令（id/名称/说明/runtime/是否禁用/命令列表；命令带 description 与 params）。调用 plugin_execute 或 plugin_open 之前先用它拿 pluginId 与 command 的准确取值。命令若有 params，它就是 input 的键与类型定义——照着填，别自己发明键名",
		nil,
		map[string]any{
			"detail": str(`"brief"（默认）：只要 id、名称、说明、runtime、命令列表（含 description/params）；"full"：附带 permissions/capabilities/用量，用于审计`),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		pluginID := strings.TrimSpace(mcpsrv.Arg(args, "pluginId"))
		commandID := strings.TrimSpace(mcpsrv.Arg(args, "command"))
		if pluginID == "" {
			return nil, errors.New("缺少参数 pluginId（用 plugin_list 获取）")
		}
		if commandID == "" {
			return nil, errors.New("缺少参数 command（用 plugin_list 查看该插件有哪些命令）")
		}
		// runtime=none 的插件没有后端：宿主执行只会返回「假成功」，命令实际由插件自己的前端处理。
		// 这里直接拒绝并指路，避免 AI 误以为调用真的生效（返回值不可用于判断执行结果）。
		if rt := s.pluginRuntime(pluginID); rt == "none" {
			return nil, fmt.Errorf("插件 %s 是纯前端插件（runtime=none），宿主无法执行它的命令；请改用 plugin_open 打开界面（它只能预填文本，结构化参数需要在界面里填写）", pluginID)
		}
		input, err := pluginInput(args["input"])
		if err != nil {
			return nil, err
		}
		// 插件声明了 params 就先拦「缺必填」：少传必填键的调用多半以一句模糊错误收场，
		// 事后分不清是宿主吞了参数还是插件出错。这里只查 required，不查未知键与类型——
		// 声明可能不全，插件也可能宽容转换，宿主不该比插件更严格。
		if params, ok := s.Plugin.PluginCommandParams(pluginID, commandID); ok {
			if err := checkRequiredParams(params, input); err != nil {
				return nil, err
			}
		}
		// 来源记为 mcp：用户回看执行历史时，必须能分清「AI 跑的」和「自己点的」。
		return unwrap(s.Plugin.ExecutePluginCommandFrom(pluginID, commandID, pluginsvc.TriggerMCP, input))
	}, mcpsrv.LevelWrite, "plugin_execute",
		"执行某个已安装插件的命令（先用 plugin_list 查插件 id 与 command id）。input 支持字符串或 JSON 对象：命令在 plugin_list 里带 params 时，按 params 的键与类型填对象（required 必须给，其余用 default）；没有 params 时传字符串会作为 text。纯前端插件（runtime=none）会被拒绝，请改用 plugin_open。返回 success 只代表宿主调用完成，若插件是异步任务，用 plugin_exec_logs 或插件的 -status 类命令确认结果",
		[]string{"pluginId", "command"},
		map[string]any{
			"pluginId": str("插件 ID，如 io.github.parieses.port-scanner"),
			"command":  str("命令 ID，来自 plugin_list 的 commands[].id"),
			"input":    mcpsrv.StrOrObj(`可选。字符串会作为 text 传给插件（等价 {"text": "..."}）；JSON 对象则原样透传。键名以 plugin_list 返回的 commands[].params 为准，如 {"target":"192.168.1.0/24","fast":true}。命令没有 params 时先只传字符串，插件报错会说明原因`),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		pluginID := strings.TrimSpace(mcpsrv.Arg(args, "pluginId"))
		commandID := strings.TrimSpace(mcpsrv.Arg(args, "command"))
		if pluginID == "" {
			return nil, errors.New("缺少参数 pluginId（用 plugin_list 获取）")
		}
		// input 只接受纯文本：plugin_open 的注入通道是插件前端的 plugin:init {text, command}
		// 契约，塞对象会被 Arg 读成空串——链路静默丢参，AI 却以为参数带进去了。
		// 结构化参数走 plugin_execute；必须看界面时先打开、再由用户在界面里填。
		var text string
		switch v := args["input"].(type) {
		case nil:
		case string:
			text = v
		default:
			return nil, fmt.Errorf("plugin_open 的 input 只能是不带结构的文本（收到 %T）；结构化参数请用 plugin_execute", args["input"])
		}
		return unwrap(s.Plugin.OpenPluginWindowWith(pluginID, commandID, text))
	}, mcpsrv.LevelWrite, "plugin_open",
		"以独立窗口打开某个插件界面，可选指定命令与预填文本（等价于用户从命令面板选中该命令）。纯前端插件（runtime=none）只能这样操作；需要用户在界面上完成、或结果只渲染在界面上的命令也用它。已禁用的插件会被拒绝",
		[]string{"pluginId"},
		map[string]any{
			"pluginId": str("插件 ID，来自 plugin_list"),
			"command":  str("可选。要预选中的命令 ID，必须是该插件真实存在的命令"),
			"input":    str("可选。预填给插件的输入文本（等同于命令面板里敲进去的内容）"),
		})

	r.add(func(args map[string]any) (any, error) {
		if s.Plugin == nil {
			return nil, errors.New("插件服务未初始化")
		}
		pluginID := strings.TrimSpace(mcpsrv.Arg(args, "pluginId"))
		return unwrap(s.Plugin.PluginExecLogs(pluginID, mcpsrv.ArgInt(args, "limit", 20)))
	}, mcpsrv.LevelRead, "plugin_exec_logs",
		"查看最近的插件命令执行记录（时间、命令、成功与否、耗时、错误、结果摘要）。plugin_execute 返回后若不确定插件是否真的做完，用它核对；省略 pluginId 则返回全部插件的记录",
		nil,
		map[string]any{
			"pluginId": str("可选。只看某个插件的记录，省略则看全部"),
			"limit":    mcpsrv.Int("可选。返回条数，默认 20，最多 50"),
		})
}

// checkRequiredParams 拦下「插件声明了必填参数却没给」的调用。
//
// 只判 required，不判未知键与类型：params 声明可能不完整（插件作者只写了主要几个键），
// 插件自己也可能把 "80" 转成 80。宿主比插件更严格，等于把能跑通的调用挡在门外。
func checkRequiredParams(params map[string]interface{}, input map[string]interface{}) error {
	rawReq, ok := params["required"].([]interface{})
	if !ok || len(rawReq) == 0 {
		return nil
	}
	props, _ := params["properties"].(map[string]interface{})
	var missing []string
	for _, item := range rawReq {
		key, ok := item.(string)
		if !ok {
			continue
		}
		if v, exists := input[key]; exists && !emptyParamValue(v) {
			continue
		}
		// 把类型与说明一并带上，模型一次就能补齐，不用再来回试
		hint := key
		if p, ok := props[key].(map[string]interface{}); ok {
			t, _ := p["type"].(string)
			d, _ := p["description"].(string)
			hint = fmt.Sprintf("%s（%s）：%s", key, t, d)
		}
		missing = append(missing, hint)
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("input 缺少必填参数：%s。请把它们放进 input 对象再调用（键名与类型由插件的 params 定义）",
		strings.Join(missing, "；"))
}

// emptyParamValue 空字符串与 null 视为「没给」：声明 required 的键传空串，
// 插件侧通常等同于缺省，提前拦下比让它带着空值跑更省排查时间。
func emptyParamValue(v interface{}) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// pluginInput 归一化 plugin_execute 的 input 参数。
//
// 三种形态：
//   - 缺省 / null       → 空 map（插件按自己的默认值走）
//   - 字符串            → {"text": 值}，与命令面板传参的老约定一致
//   - JSON 对象         → 原样透传（插件的 input 契约本来就是任意 JSON，
//     前端 usePluginHost 也是这么传的，MCP 侧此前只取 text 是把字段丢了）
//
// 其他类型（数字、数组）明确报错：静默转成字符串会让模型以为参数生效了。
func pluginInput(raw any) (map[string]interface{}, error) {
	switch v := raw.(type) {
	case nil:
		return map[string]interface{}{}, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]interface{}{}, nil
		}
		return map[string]interface{}{"text": v}, nil
	case map[string]interface{}:
		return v, nil
	default:
		return nil, fmt.Errorf(`input 必须是字符串或 JSON 对象，收到 %T；字符串会作为 text 传给插件，对象则原样透传`, raw)
	}
}
