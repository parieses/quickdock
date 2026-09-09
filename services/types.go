package services

// HotkeyConfig 热键配置（前端用）
type HotkeyConfig struct {
	Modifiers int    `json:"modifiers"`
	VK        int    `json:"vk"`
	Label     string `json:"label"`
}
