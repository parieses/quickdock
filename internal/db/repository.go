package db

// 实体类型（JSON 可序列化字段，用于 Wails 绑定）

// Workspace 工作空间
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Storage string `json:"storage"`
	Remark  string `json:"remark"`
	// NoSearch 是否从 Ctrl+K 搜索里排除（1 = 排除）。应用只有一个工作空间时
	// 这个开关意义不大，但它与场景/集合/项目保持同一套字段，避免特例分支。
	NoSearch  int    `json:"noSearch"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Scene 场景
type Scene struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	// GroupName 分组名（侧栏按组折叠渲染）。空串=未分组。刻意叫 group_name 而非 group：
	// group 是 SQL 保留字，直接用会撞上 codebase 里大量 GROUP BY 查询。
	GroupName string `json:"groupName"`
	Icon      string `json:"icon"`
	Color     string `json:"color"`
	Favorite  int    `json:"favorite"`
	Unbound   int    `json:"unbound"`
	// NoSearch 1 = 该场景不出现在 Ctrl+K 搜索结果里（全局开关之外的单条排除）。
	NoSearch   int `json:"noSearch"`
	UsageCount int `json:"usageCount"`
	Sort       int `json:"sort"`
	// Env 场景绑定的环境服务，JSON 数组：[{"runtime":"mysql","version":"8.4.3"}]。
	// version 为空表示跟随激活版本（或首个已装版本）。空串/空数组=未绑定任何服务。
	Env       string `json:"env"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// Collection 集合
type Collection struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspaceId"`
	SceneID       string `json:"sceneId"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	GroupName     string `json:"groupName"`
	DefaultToolID string `json:"defaultToolId"`
	Tool          string `json:"tool"`
	Icon          string `json:"icon"`
	Color         string `json:"color"`
	OpenStrategy  string `json:"openStrategy"`
	Favorite      int    `json:"favorite"`
	Recent        int    `json:"recent"`
	RecentAt      string `json:"recentAt"`
	Unbound       int    `json:"unbound"`
	// NoSearch 1 = 该集合不出现在 Ctrl+K 搜索结果里。
	NoSearch   int    `json:"noSearch"`
	PluginID   string `json:"pluginId"`
	UsageCount int    `json:"usageCount"`
	Sort       int    `json:"sort"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// CollectionItem 项目
type CollectionItem struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspaceId"`
	CollectionID     string `json:"collectionId"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Value            string `json:"value"`
	WorkingDirectory string `json:"workingDirectory"`
	ToolID           string `json:"toolId"`
	Tool             string `json:"tool"`
	Args             string `json:"args"`
	Icon             string `json:"icon"`
	Color            string `json:"color"`
	Remark           string `json:"remark"`
	PluginData       string `json:"pluginData"`
	// NoSearch 1 = 该条目不出现在 Ctrl+K 搜索结果里（书签导入几百条后，
	// 逐个排除不现实，通常配合设置里的「项目不参与搜索」总开关使用）。
	NoSearch   int    `json:"noSearch"`
	UsageCount int    `json:"usageCount"`
	Sort       int    `json:"sort"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	// Env 条目绑定的运行时版本，JSON 数组 [{"runtime":"node","version":"18.20.0"}]，
	// 与 scenes.env 同形。打开该条目时把这些版本的 bin 目录前置到子进程 PATH——
	// 只影响本次打开，不改系统环境变量（那是全局激活 SetActive 的职责）。
	Env string `json:"env"`

	// iconFresh 标记「本次扫描时图标是刚从文件提取出来的」，仅供 persistExtractedIcons
	// 决定要回写哪几行，不导出（不进 JSON、不进 Wails 绑定）。
	//
	// 为什么需要它：DB 里本来就存着图标的条目，扫出来的 Icon 同样非空，若只按
	// 「Icon != ""」收集，UPDATE 的参数里 100% 是已经落过库的行（实测 4/4），
	// 靠 WHERE icon='' 兜着一条都改不到——每条只读查询却要白拿一次全局写锁、
	// 白构造一条 CASE 语句（5000 条全量池时 SQL 过百 KB、参数上万）。
	iconFresh bool
}

// OpenTool 打开工具
type OpenTool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Args      string `json:"args"`
	IsDefault int    `json:"isDefault"`
}

// SnapshotPayload 快照载荷（JSON 序列化）
//
// 核心表（workspaces..tools）用强类型承载，保持历史格式不变，旧备份文件可直接恢复。
// 其余业务表放 Tables，key 为真实表名（见 syncExtraTables），行不做类型映射 ——
// 这避免了 plugins/monitors 这类列数多且随版本演进（CREATE TABLE + ALTER 叠加）的表
// 在改动列时漏字段。恢复时按当前库实际列取交集，对未知列免疫。
type SnapshotPayload struct {
	Workspaces  []Workspace      `json:"workspaces"`
	Scenes      []Scene          `json:"scenes"`
	Collections []Collection     `json:"collections"`
	Items       []CollectionItem `json:"items"`
	Tools       []OpenTool       `json:"tools"`
	// Tables 扩展表数据，key 必须存在于 syncExtraTables。
	// 注意：恢复时只处理 map 中存在的 key，因此旧备份（无 tables 字段）不会清空这些表。
	Tables map[string][]map[string]interface{} `json:"tables,omitempty"`
}

// Snapshot 数据快照
type Snapshot struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Note      string `json:"note"`
	Payload   string `json:"payload"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
}
