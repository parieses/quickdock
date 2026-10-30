package ai

// 钩子配置的 bound 出入口。实现在 services/ai/tools/hooks.go——
// 钩子的执行方是工具层（file_edit / file_write / run_command 跑它们），
// 这里只负责把「读配置 / 批准目录」暴露给设置界面。
//
// 为什么不直接把 tools 包的类型透给前端：bound 方法的返回值会进 codegen，
// 引用别的包的导出类型会让前端绑定跟着那个包的结构走；这里复制一份轻量
// struct，字段对得上即可，两边各自演进时不互相牵连。

import (
	"quickdock/services"
	aitools "quickdock/services/ai/tools"
)

// aiHooksView 一个目录的钩子情况（对应 tools.HooksInfo）。
type aiHooksView struct {
	Found         bool     `json:"found"`
	Path          string   `json:"path"`
	AfterEdit     []string `json:"afterEdit"`
	BeforeCommand []string `json:"beforeCommand"`
	Approved      bool     `json:"approved"`
	Error         string   `json:"error"`
}

// AIGetHooks 读一个工作目录的钩子配置与批准状态（bound）。
func (a *AIService) AIGetHooks(cwd string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	info := aitools.HooksFor(a.App.DB, cwd)
	return services.Ok(info)
}

// AISetHooksApproved 批准 / 撤销一个目录的钩子（bound）。
//
// 批准没有批量入口是刻意的：hooks.json 是随仓库走的文件，clone 下来就带着，
// 「全部信任」等于把任意仓库的脚本变成 AI 改文件时的自动执行项。
func (a *AIService) AISetHooksApproved(cwd string, on bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := aitools.SetHooksApproved(a.App.DB, cwd, on); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}
