package services

// HotkeyConfig 热键配置（前端用）
type HotkeyConfig struct {
	Modifiers int    `json:"modifiers"`
	VK        int    `json:"vk"`
	Label     string `json:"label"`
}

// WebDAVConfig WebDAV 同步配置
type WebDAVConfig struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// BackupFile WebDAV 上的备份文件信息
type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
}
