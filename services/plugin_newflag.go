package services

import (
	"encoding/json"
)

// ===== 插件「新安装 / 更新」角标 =====
//
// 角标基于事件而非时间：安装或更新插件时标记 new=true，用户首次打开（运行 / 展开）后清除。
// 不限时——很久以前安装但从未打开的插件会一直显示「新」直到被打开。
// 数据持久化在 settings 表的 plugin_new_flags（JSON: map[id]bool）。

const pluginNewFlagsKey = "plugin_new_flags"

// loadPluginNewFlags 读回 map，表缺失/数据损坏时返回空 map（不报错）。
func (a *AppService) loadPluginNewFlags() map[string]bool {
	m := make(map[string]bool)
	if a.DB == nil {
		return m
	}
	raw, err := a.DB.GetSetting(pluginNewFlagsKey)
	if err != nil || raw == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// savePluginNewFlags 写回 map，DB 未初始化则静默丢弃（下次安装会重标）。
func (a *AppService) savePluginNewFlags(m map[string]bool) {
	if a.DB == nil {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = a.DB.SetSetting(pluginNewFlagsKey, string(b))
}

// MarkPluginNew 内部方法：安装 / 更新插件后由 InstallPlugin 调用，标记该插件为「新」。
// 不导出 —— 只允许后端在写入插件记录后触发，避免被前端误调用。
func (a *AppService) MarkPluginNew(id string) {
	if id == "" {
		return
	}
	m := a.loadPluginNewFlags()
	if m[id] {
		return // 已是新状态，无需重复写盘
	}
	m[id] = true
	a.savePluginNewFlags(m)
}

// MarkPluginSeen 清除某插件的「新」角标（首次打开后由前端调用）。
func (a *AppService) MarkPluginSeen(id string) *ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	if id == "" {
		return Ok(nil)
	}
	m := a.loadPluginNewFlags()
	if !m[id] {
		return Ok(nil) // 本就没有角标，幂等
	}
	delete(m, id)
	a.savePluginNewFlags(m)
	return Ok(nil)
}

// GetPluginNewFlags 返回当前所有「新」插件的 ID 列表，供前端渲染角标。
func (a *AppService) GetPluginNewFlags() []string {
	if a.DB == nil {
		return nil
	}
	m := a.loadPluginNewFlags()
	ids := make([]string, 0, len(m))
	for id, isNew := range m {
		if isNew {
			ids = append(ids, id)
		}
	}
	return ids
}
