// Package aitools 是内置 AI 专用的工具集，不属于 MCP 对外能力。
//
// 分界很重要：MCP（services/mcp + internal/mcp）对外提供的是「QuickDock 这个软件
// 本身能干什么」——环境管理、快捷项、笔记、待办、剪贴板、诊断、定时、监控、插件。
// 本包提供的是另一类东西：抓网页、列目录、看 git 差异、改文件、跑命令。
// 它们的意义来自「AI 和你在某个目录里一起干活」，而不是「这个软件能配置什么」，
// 外部客户端本来就有自己的文件与终端工具，再暴露一遍只会让模型在两套同名能力
// 之间反复挑。
//
// 因此这里注册进 mcpsrv.Agent（另一个注册表），而不是 mcpsrv.Global。
// 两个注册表共用等级门与单工具开关：面板里关掉 file_edit，内置 AI 也就改不了文件。
//
// 目录里的文件分工：register.go 装配、web.go 抓网页、dir.go 列目录、
// git.go 看仓库状态、code.go 改文件与跑命令。
package aitools

import (
	"context"

	"quickdock/internal/db"
	envengine "quickdock/internal/env"
	mcpsrv "quickdock/internal/mcp"
)

// Deps agent 工具允许接触的外部状态。
//
// 用两个具体依赖而不是 *services.AppService：工具只需要「库里的快捷项」和
// 「托管运行时的 bin 目录」，拿到整个 AppService 就意味着它们随时可以去调
// 窗口、插件宿主、AI 门面——那才是让这个包和宿主耦合的东西。
type Deps struct {
	DB  *db.Database
	Env *envengine.Manager
}

// Register 把全部 agent 工具注册进 mcpsrv.Agent。由 AIService 构造时调用。
func Register(d Deps) {
	registerWeb(d)
	registerDir(d)
	registerGit(d)
	registerCode(d)
}

// registrar 单个域的注册入口：把域写进每个 Tool.Category，并统一补 InputSchema。
//
// Handler 收的是 (Deps, args) 而不是闭包里直接捕获 Deps：注册处与实现处之间
// 只靠这一个签名对接，实现函数因此都是可单测的纯函数（传一个空 Deps 就能跑）。
type registrar struct {
	d        Deps
	category string
}

func (r registrar) add(run func(Deps, map[string]any) (any, error), level int, name, desc string, required []string, props map[string]any) {
	mcpsrv.Agent.Add(mcpsrv.Tool{
		Name:        name,
		Description: desc,
		InputSchema: mcpsrv.Schema("", required, props),
		Level:       level,
		Category:    r.category,
		Handler:     func(args map[string]any) (any, error) { return run(r.d, args) },
	})
}

// addCtx 注册一个需要 ctx 的工具（走 Tool.HandlerCtx，CallAgentContext 会自动优先用它）。
//
// 只有写类工具用它：快照（A3）要知道「哪个会话的哪一次调用在改这个文件」，
// 这个信息只能在 ctx 里。其余工具继续用 add——给一个不需要 ctx 的函数加参数，
// 只会让 10 个实现里 7 个写着 `_ context.Context`。
func (r registrar) addCtx(run func(context.Context, Deps, map[string]any) (any, error), level int, name, desc string, required []string, props map[string]any) {
	mcpsrv.Agent.Add(mcpsrv.Tool{
		Name:        name,
		Description: desc,
		InputSchema: mcpsrv.Schema("", required, props),
		Level:       level,
		Category:    r.category,
		HandlerCtx:  func(ctx context.Context, args map[string]any) (any, error) { return run(ctx, r.d, args) },
	})
}
