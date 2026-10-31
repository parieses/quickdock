package ai

// 钩子配置的 bound 出入口。实现在 services/ai/tools/hooks.go——
// 钩子的执行方是工具层（file_edit / file_write / run_command 跑它们），
// 这里只负责把「读配置 / 批准目录」暴露给设置界面。
//
// 为什么不直接把 tools 包的类型透给前端：bound 方法的返回值会进 codegen，
// 引用别的包的导出类型会让前端绑定跟着那个包的结构走；这里复制一份轻量
// struct，字段对得上即可，两边各自演进时不互相牵连。

import (
	"quickdock/internal/env"
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

// AIGetGitBranch 返回某个工作目录所在的 git 分支（bound）。
//
// 用途：AI 聊天输入框下方实时显示「当前在哪条分支」，让用户确认 AI 即将改动的
// 是不是自己以为的那条分支。非仓库 / 没装 git 一律返回 repo=false，不报错——
// 这只是一个锦上添花的提示，不该因为没 git 就弹错误。
func (a *AIService) AIGetGitBranch(cwd string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	// 把宿主的 Env 透给 gitBin：GUI 进程 PATH 未必有 git，靠它回落到便携版 git。
	var env *env.Manager
	if a.App != nil {
		env = a.App.Env
	}
	branch, isRepo, err := aitools.GitBranch(aitools.Deps{Env: env}, cwd)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(map[string]any{"repo": isRepo, "branch": branch})
}
