package sysutil

// PortInfo 监听端口占用信息（跨平台共用数据结构）。
type PortInfo struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	PID      int    `json:"pid"`
	Process  string `json:"process"`
	Path     string `json:"path"` // 占用进程的映像完整路径（Windows 下经 Get-CimInstance 取得，可能为空）
	Self     bool   `json:"self"` // 是否为 QuickDock 自身占用的端口（如内置 HTTP 静态服务），用于在端口全景中高亮辨识
}
