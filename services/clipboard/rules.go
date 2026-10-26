package clipboard

import (
	"quickdock/internal/cliprule"
	"quickdock/internal/db"
	"quickdock/services"
)

// 本文件是「剪贴板自动规则」的面向前端的门面：只做参数校验 + 调 internal/cliprule 与 DB。
// 真正的文本变换全在 internal/cliprule（纯函数、有单测），这里不重复实现任何动作逻辑。

// 总开关键名的唯一定义在宿主 services 包（捕获路径也要读它），这里直接引用避免两份字面量漂移。
const clipboardRulesEnabledKey = services.ClipboardRulesEnabledKey

// ListClipboardRules 返回全部规则（已按优先级升序）
func (a *ClipboardService) ListClipboardRules() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	list, err := a.App.DB.ListClipboardRules()
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(list)
}

// CreateClipboardRule 新建规则
func (a *ClipboardService) CreateClipboardRule(r db.ClipboardRule) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if msg := validateRule(r); msg != "" {
		return services.FailMsg(msg)
	}
	created, err := a.App.DB.CreateClipboardRule(&r)
	if err != nil {
		return services.Fail(err)
	}
	return services.Ok(created)
}

// UpdateClipboardRule 更新规则
func (a *ClipboardService) UpdateClipboardRule(r db.ClipboardRule) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if r.ID == "" {
		return services.FailMsg("规则 ID 不能为空")
	}
	if msg := validateRule(r); msg != "" {
		return services.FailMsg(msg)
	}
	if err := a.App.DB.UpdateClipboardRule(&r); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// DeleteClipboardRule 删除规则
func (a *ClipboardService) DeleteClipboardRule(id string) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.DeleteClipboardRule(id); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// SetClipboardRuleEnabled 单条规则开关
func (a *ClipboardService) SetClipboardRuleEnabled(id string, enabled bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if err := a.App.DB.SetClipboardRuleEnabled(id, enabled); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// GetClipboardRulesEnabled 读总开关（默认 false）
func (a *ClipboardService) GetClipboardRulesEnabled() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	raw, err := a.App.DB.GetSetting(clipboardRulesEnabledKey)
	if err != nil {
		return services.Ok(false)
	}
	return services.Ok(raw == "1")
}

// SetClipboardRulesEnabled 写总开关
func (a *ClipboardService) SetClipboardRulesEnabled(on bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	v := "0"
	if on {
		v = "1"
	}
	if err := a.App.DB.SetSetting(clipboardRulesEnabledKey, v); err != nil {
		return services.Fail(err)
	}
	return services.Ok(nil)
}

// TestClipboardRule 试运行：拿一段样例文本看这条规则会变成什么。
// 不落库、不碰系统剪贴板 —— 让用户在开规则之前先确认"它到底改了什么"。
func (a *ClipboardService) TestClipboardRule(r db.ClipboardRule, sample, sourceApp string) *services.ApiResult {
	out, matched, changed := cliprule.Test(r, sample, sourceApp)
	return services.Ok(map[string]any{
		"output":  out,
		"matched": matched,
		"changed": changed,
	})
}

// validateRule 校验规则可用性，返回空串表示通过。
// 只校验"必然坏"的情况（空动作、正则写错），不限制用户的匹配表达式内容。
func validateRule(r db.ClipboardRule) string {
	if r.Name == "" {
		return "规则名称不能为空"
	}
	switch r.Action {
	case cliprule.ActionTrim, cliprule.ActionRemoveBlank, cliprule.ActionDedupeLines,
		cliprule.ActionSortLines, cliprule.ActionJSONFormat, cliprule.ActionJSONMinify,
		cliprule.ActionStripANSI, cliprule.ActionCase, cliprule.ActionURLDecode:
	default:
		return "未知的动作类型: " + r.Action
	}
	switch r.MatchKind {
	case "", cliprule.MatchAll, cliprule.MatchApp:
	case cliprule.MatchRegex:
		if !cliprule.ValidRegex(r.MatchExpr) {
			return "正则表达式无效"
		}
	default:
		return "未知的匹配方式: " + r.MatchKind
	}
	return ""
}
