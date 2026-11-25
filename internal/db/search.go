package db

// Ctrl+K（命令面板）搜索排除设置。
//
// 两级开关：
//
//	全局类别  设置页里的三个开关，关掉后整类实体不再进搜索结果（本项目最常见
//	          的用法是导入几百条书签后把「项目」整体摘掉）
//	单条标记  scenes / collections / items / workspaces 上的 no_search 列，
//	          全局开着时用来摘掉某一条
//
// 两者都由前端在拼命令面板数据池时判断，后端只在 SearchPalette 这一条补充检索
// 路径上按同样的口径过滤（见 services/palette.go）。刻意不做成「库层默认过滤」：
// ListAllItems / ListScenes 还被资源树、MCP 工具、AI 工具使用，那些场景要的是
// 全量数据，过滤掉会变成「明明在库里却打不开」。

const (
	settingSearchExcludeWorkspaces = "search.exclude.workspaces"
	settingSearchExcludeScenes     = "search.exclude.scenes"
	settingSearchExcludeItems      = "search.exclude.items"
	// showHidden 是隐藏项的「逃生口」：打开后资源树/AI/MCP 仍显示被标记 no_search
	// 的项，让用户能点进去把勾选取消。没有它，勾一下就等于把数据锁进黑洞。
	settingSearchShowHidden = "search.showHidden"
)

// SearchExclusions 三类实体是否整体不进 Ctrl+K 搜索。
//
// 场景与集合共用一个开关：命令面板里它们本来就是相邻的两组结果，用户想摘掉的
// 是「侧栏那棵树」，而不是严格区分场景和集合。
type SearchExclusions struct {
	Workspaces bool `json:"workspaces"`
	Scenes     bool `json:"scenes"`
	Items      bool `json:"items"`
}

// GetSearchExclusions 读取全局排除设置（缺省全部为 false = 都参与搜索）。
func (d *Database) GetSearchExclusions() SearchExclusions {
	return SearchExclusions{
		Workspaces: d.settingBool(settingSearchExcludeWorkspaces),
		Scenes:     d.settingBool(settingSearchExcludeScenes),
		Items:      d.settingBool(settingSearchExcludeItems),
	}
}

// SetSearchExclusions 写入全局排除设置。
func (d *Database) SetSearchExclusions(p SearchExclusions) error {
	for _, kv := range []struct {
		key string
		on  bool
	}{
		{settingSearchExcludeWorkspaces, p.Workspaces},
		{settingSearchExcludeScenes, p.Scenes},
		{settingSearchExcludeItems, p.Items},
	} {
		v := ""
		if kv.on {
			v = "1"
		}
		if err := d.SetSetting(kv.key, v); err != nil {
			return err
		}
	}
	return nil
}

// GetSearchShowHidden 是否显示被标记 no_search 的项（资源树/AI/MCP）。
func (d *Database) GetSearchShowHidden() bool {
	return d.settingBool(settingSearchShowHidden)
}

// SetSearchShowHidden 设置是否显示隐藏项。
func (d *Database) SetSearchShowHidden(on bool) error {
	v := ""
	if on {
		v = "1"
	}
	return d.SetSetting(settingSearchShowHidden, v)
}

// settingBool 把 app_state 里的 "1" 读成 true；读失败一律 false
// （搜索排除是「少显示点东西」的偏好，读不到时宁可多显示也不要吞掉结果）。
func (d *Database) settingBool(key string) bool {
	v, err := d.GetSetting(key)
	if err != nil {
		return false
	}
	return v == "1" || v == "true"
}
