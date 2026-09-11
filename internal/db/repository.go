package db

// 实体类型（JSON 可序列化字段，用于 Wails 绑定）

// Workspace 工作空间
type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Storage   string `json:"storage"`
	Remark    string `json:"remark"`
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
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Favorite    int    `json:"favorite"`
	Unbound     int    `json:"unbound"`
	UsageCount  int    `json:"usageCount"`
	Sort        int    `json:"sort"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// Collection 集合
type Collection struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspaceId"`
	SceneID       string `json:"sceneId"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	DefaultToolID string `json:"defaultToolId"`
	Tool          string `json:"tool"`
	Icon          string `json:"icon"`
	Color         string `json:"color"`
	OpenStrategy  string `json:"openStrategy"`
	Favorite      int    `json:"favorite"`
	Recent        int    `json:"recent"`
	RecentAt      string `json:"recentAt"`
	Unbound       int    `json:"unbound"`
	PluginID      string `json:"pluginId"`
	UsageCount    int    `json:"usageCount"`
	Sort          int    `json:"sort"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
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
	UsageCount       int    `json:"usageCount"`
	Sort             int    `json:"sort"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
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
	// 注意：恢复时只处理 map 中**存在**的 key，因此旧备份（无 tables 字段）不会清空这些表。
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
