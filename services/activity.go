package services

import (
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
// name 是**对象名**（项目名 / 主机名 / 「插件 · 命令」），不是整句话——
// 动作词由前端按 kind 用当前语言拼，否则英文界面会露出中文。
func (a *AppService) NoteActivity(kind, refID, name string) {
	if a == nil || a.DB == nil {
		return
	}
	_ = a.DB.AddActivity(kind, refID, name)
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
