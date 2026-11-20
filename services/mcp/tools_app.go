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
		// 三个数分开报，跟 MCPStatus 面板同一口径：agent 工具单独算，不混进对外工具数。
		// 只报一个「工具数」时，模型会把它当成宿主的全部能力——被等级门挡住的高危工具
		// 就变成了「这个宿主没有」，用户在被要求「执行命令」时找不到入口也不知为什么。
		cat := mcpsrv.Catalog()
		exposed, total := 0, 0
		for _, t := range cat {
			if t.AgentOnly {
				continue
			}
			total++
			if t.Exposed {
				exposed++
			}
		}
		return map[string]any{
			"app":        "QuickDock",
			"version":    s.version,
			"platform":   runtime.GOOS + "/" + runtime.GOARCH,
			"dataDir":    platform.DefaultDataDir(),
			"mcpRunning": mcpsrv.Default.Running(),
			"mcpUrl":     mcpsrv.Default.Endpoint(),
			// tools=当前开放（客户端 tools/list 真正能看到并调用的），
			// toolsTotal=已注册总数，toolsBlocked=被等级/单工具开关挡住的。
			"tools":        exposed,
			"toolsTotal":   total,
			"toolsBlocked": total - exposed,
		}, nil
	}, mcpsrv.LevelRead, "app_info", "QuickDock 基本信息：版本、平台、数据目录、MCP 服务地址，以及工具数量（tools=当前开放数、toolsTotal=已注册总数、toolsBlocked=被危险等级或单工具开关挡住的数量）", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		return unwrap(s.Port.ListListeningPorts())
	}, mcpsrv.LevelRead, "port_list", "列出本机正在监听的端口与对应进程", nil, nil)

	// ---- 通知渠道（前提是已在「设置 → 通知」配置过至少一个渠道）----

	r.add(func(args map[string]any) (any, error) {
		title := mcpsrv.Arg(args, "title")
		if title == "" {
			return nil, errors.New("缺少参数 title（消息标题）")
		}
		res := s.App.SendWebhookMessage(title, mcpsrv.Arg(args, "body"), mcpsrv.Arg(args, "channel"))
		return unwrap(res)
	}, mcpsrv.LevelWrite, "robot_notify", "通过已启用的通知渠道发送一条消息。内置 kind：dingtalk / wecom / feishu / serverchan / pushplus / telegram；扩展 kind：bark / discord / slack / ntfy / gotify / custom（在「设置 → 通知」里添加，可用 GetExtraNotifyChannels 查询已配置渠道）。未配置任何启用渠道时会返回明确错误。channel 留空则发给所有已启用渠道，指定则只发该渠道", []string{"title"},
		map[string]any{
			"title":   str("消息标题，必填"),
			"body":    str("消息正文，可留空"),
			"channel": str("可选。指定渠道：内置/扩展 kind，或扩展渠道的 id（GetExtraNotifyChannels 返回）；用扩展渠道的 kind 则发往该 kind 的全部渠道。留空则发给所有已启用渠道"),
		})

	// ---- 邮箱验证码（只读；不暴露账号密码配置、外连收信与清空）----

	r.add(func(args map[string]any) (any, error) {
		if s.Mailbox == nil {
			return nil, errors.New("邮箱服务未就绪")
		}
		return unwrap(s.Mailbox.GetCodes())
	}, mcpsrv.LevelRead, "mail_codes", "列出最近收到的邮箱验证码（已从邮件正文解析）。用户说「刚才的验证码是多少」时用；不会触发收信，只读本地已缓存的邮件", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.Mailbox == nil {
			return nil, errors.New("邮箱服务未就绪")
		}
		id := mcpsrv.Arg(args, "id")
		if id == "" {
			return nil, errors.New("缺少参数 id（用 mail_codes 获取）")
		}
		return unwrap(s.Mailbox.GetMessage(id))
	}, mcpsrv.LevelRead, "mail_read", "按 id 读一封已缓存邮件的正文（mail_codes 返回 id）。不联网、不触发收信", []string{"id"},
		map[string]any{"id": str("邮件 id，来自 mail_codes")})

	// ---- 密码库：只暴露状态与随机密码生成，明文读取/写入/导出刻意不给 AI ----

	r.add(func(args map[string]any) (any, error) {
		if s.Credentials == nil {
			return nil, errors.New("密码库服务未就绪")
		}
		return unwrap(s.Credentials.Status())
	}, mcpsrv.LevelRead, "credentials_status", "查看密码库是否已解锁、条目数、扩展是否启用。只报状态，不返回任何密码明文", nil, nil)

	r.add(func(args map[string]any) (any, error) {
		if s.Credentials == nil {
			return nil, errors.New("密码库服务未就绪")
		}
		// ArgBool 没有默认值重载：未传时三个开关都要是 true，故显式补齐。
		opt := func(key string, def bool) bool {
			v, ok := args[key].(bool)
			if !ok {
				return def
			}
			return v
		}
		return unwrap(s.Credentials.GeneratePassword(
			mcpsrv.ArgInt(args, "length", 16),
			opt("digits", true),
			opt("symbols", true),
			opt("upper", true)))
	}, mcpsrv.LevelRead, "password_generate", "生成一个随机密码（不接触密码库里已有的条目）。用户让你帮忙想个强密码时用", nil,
		map[string]any{
			"length":  mcpsrv.Int("长度，默认 16"),
			"digits":  mcpsrv.Bool("是否含数字，默认 true"),
			"symbols": mcpsrv.Bool("是否含符号，默认 true"),
			"upper":   mcpsrv.Bool("是否含大写字母，默认 true"),
		})
}
