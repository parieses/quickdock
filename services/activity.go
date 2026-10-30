package services

import (
	"runtime"
	"strings"

	"quickdock/internal/db"
)

// 操作流水（activity 表）。
//
// 这张表建好很久但从没有写入方，是长期空置的。启用它的动机很具体：
// 使用数据一直在攒（usage_frecency 记了「什么用得最多」），却没有任何地方
// 回答「我最近到底干了什么」—— 而后者才是打开工具时想先看到的。
//
// 埋点原则：
//   - 只在用户视角有意义的动作上记（打开项目、连主机、跑插件命令、触发任务），
//     不记内部状态流转。流水是给人看的，不是审计账本。
//   - 一律旁路。记不上一条动态绝不该让主流程失败，所以 NoteActivity 没有返回值。
//   - 去重与裁剪在 db 层做（AddActivity），埋点处不必关心频率。

// NoteActivity 记录一条操作流水。失败静默——埋点是旁路，不是主流程的一环。
//
// kind 取 db.Activity* 常量；refID 是被操作对象的 id（供将来做跳转）；
// name 是对象名（项目名 / 主机名 / 「插件 · 命令」），不是整句话——
// 动作词由前端按 kind 用当前语言拼，否则英文界面会露出中文。
//
// 来源（自己点的 / AI 调 MCP 工具触发的）在 db 层落成 source 列，由 activitySource
// 自动判定，埋点处不必逐处传参——漏传一处就会让时间线把 AI 的动作记成用户的。
func (a *AppService) NoteActivity(kind, refID, name string) {
	if a == nil || a.DB == nil {
		return
	}
	_ = a.DB.AddActivityFrom(kind, refID, name, activitySource())
}

// mcpFramePrefixes MCP 工具调用链上的包路径。命中任一即认为本次埋点由 AI 触发。
//
// 只要 services/mcp（工具 Handler 所在的域文件）与 internal/mcp（JSON-RPC 入口）
// 都覆盖到即可：埋点全部落在业务层，而业务层只可能被「UI 绑定方法」或「MCP 工具」
// 两条路径调进来，栈上没有这两个前缀就一定是用户自己点的。
var mcpFramePrefixes = []string{
	"quickdock/services/mcp.",
	"quickdock/internal/mcp.",
}

// activitySource 通过调用栈判断这次埋点是不是 MCP 工具调用链发起的。
//
// 为什么用栈而不是「进工具时设个包级标记、退出时清掉」：MCP 请求与界面操作是
// 并发的（每个 HTTP 请求一条 goroutine），包级标记的有效窗口内，用户自己的操作
// 会被误标成 AI 所为——而那正是最需要准确的场景。调用栈天然是 goroutine 局部的，
// 且这里所有埋点都是同步调用（工具 Handler 直接调业务方法），不存在跨 goroutine
// 丢失的问题；万一哪天异步化了，结果是退化成 ui（保守），不会误报成 mcp。
//
// 开销：runtime.Callers 抓一帧栈约几微秒，而 AddActivity 本身要走 SQLite 写入，
// 还要做 90 秒去重，这点开销可以忽略。
func activitySource() string {
	var pcs [32]uintptr
	// skip=2：跳过 runtime.Callers 自身与 activitySource
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return db.ActivitySourceUI
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		for _, prefix := range mcpFramePrefixes {
			if strings.HasPrefix(f.Function, prefix) {
				return db.ActivitySourceMCP
			}
		}
		if !more {
			break
		}
	}
	return db.ActivitySourceUI
}

// ListActivity 返回最近的操作流水（新的在前）。limit<=0 时取默认 100 条。
func (a *AppService) ListActivity(limit int) *ApiResult {
	if r := CheckDB(a.DB); r != nil {
		return r
	}
	list, err := a.DB.ListActivity(limit)
	if err != nil {
		return Fail(err)
	}
	if list == nil {
		list = []db.Activity{}
	}
	return Ok(list)
}

// ClearActivity 清空操作流水。
func (a *AppService) ClearActivity() *ApiResult {
	if r := CheckDB(a.DB); r != nil {
		return r
	}
	if err := a.DB.ClearActivity(); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}

// ClearActivityWhere 按来源与时间批量清除操作流水（「清除当前筛选结果」）。
// kind 为空 = 不限来源；sinceTs<=0 = 不限时间；都空等价于 ClearActivity。
func (a *AppService) ClearActivityWhere(kind string, sinceTs int64) *ApiResult {
	if r := CheckDB(a.DB); r != nil {
		return r
	}
	if err := a.DB.ClearActivityWhere(kind, sinceTs); err != nil {
		return Fail(err)
	}
	return Ok(nil)
}
