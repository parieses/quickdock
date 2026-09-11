package sysutil

// ProcessInfo 进程概要信息（供 host.process.list 使用）。
type ProcessInfo struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	MemBytes int64  `json:"memBytes"` // 常驻内存（RSS），单位字节
	// CPU 百分比需要跨采样间隔统计，单次枚举无法给出稳定值，
	// 故 host.process.list 暂不返回 CPU（保持 0），避免给出误导性的瞬时值。
	CPU float64 `json:"cpu"`
}
