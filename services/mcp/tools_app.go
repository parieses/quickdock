package mcp

// 宿主域工具：QuickDock 自身的信息、监听端口、对外通知通道。
//
// 与「环境管理域」刻意分开：这三者描述的是宿主进程/本机的现状，
// 而不是某个受管运行时的状态，混在一起会让「运行时」这个概念被稀释。

import (
	"errors"
	"runtime"

	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/platform"
)

func registerAppTools(s *MCPService, r *toolRegistrar) {
	str := mcpsrv.Str

	r.add(func(args map[string]any) (any, error) {
		return map[string]any{
			"app":        "QuickDock",
			"version":    s.version,
			"platform":   runtime.GOOS + "/" + runtime.GOARCH,
			"dataDir":    platform.DefaultDataDir(),
			"mcpRunning": mcpsrv.Default.Running(),
			"mcpUrl":     mcpsrv.Default.Endpoint(),
			"tools":      len(mcpsrv.List()),
		}, nil
	}, mcpsrv.LevelRead, "app_info", "QuickDock 基本信息：版本、平台、数据目录、MCP 服务地址与已开放工具数", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Port.ListListeningPorts())
	}, mcpsrv.LevelRead, "port_list", "列出本机正在监听的端口与对应进程", nil, nil)

	// ---- 机器人通知（前提是已在「设置 → 通知」配置过机器人）----

	r.add(func(args map[string]any) (any, error) {
		title := mcpsrv.Arg(args, "title")
		if title == "" {
			return nil, errors.New("缺少参数 title（消息标题）")
		}
		res := s.App.SendWebhookMessage(title, mcpsrv.Arg(args, "body"), mcpsrv.Arg(args, "channel"))
		return unwrap(res)
	}, mcpsrv.LevelWrite, "robot_notify", "通过已配置的机器人（钉钉/企业微信/飞书/ServerChan/PushPlus/Telegram）发送一条消息。前提是已在「设置 → 通知」配置过至少一个机器人，未配置时会返回明确错误。channel 留空则发给所有已配置渠道，指定则只发该渠道", []string{"title"},
		map[string]any{
			"title":   str("消息标题，必填"),
			"body":    str("消息正文，可留空"),
			"channel": str("指定渠道：dingtalk / wecom / feishu / serverchan / pushplus / telegram；留空则发给所有已配置渠道"),
		})
}
