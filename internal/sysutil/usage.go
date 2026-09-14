package sysutil

// ProcStat 单个进程的资源快照，供「按进程树聚合服务占用」使用。
type ProcStat struct {
	PID       int
	ParentPID int
	MemBytes  int64
	// CPUSeconds 进程累计消耗的 CPU 时间（内核态 + 用户态），
	// 单次采样只能得到累计值，占用率需由调用方对两次采样求差。
	CPUSeconds float64
}

// 进程快照：SnapshotProcs 一次性采集本机全部进程的 PID / 父 PID / 常驻内存 / 累计 CPU 时间。
// 按平台分文件实现，见 usage_windows.go / usage_linux.go / usage_darwin.go。
//
// 为什么取全量快照而不是按 PID 单查：服务的资源占用必须按进程树聚合——
// nginx 的实际负载在 worker 子进程上，Ollama 加载模型后的内存大头在 runner 子进程上，
// 只统计主进程会严重低估。有了整张进程表，才能从主 PID 向下收敛出完整子树。
//
// 各平台实现均无子进程副作用（Windows 走 Toolhelp API、Linux 读 /proc、darwin 调一次 ps）。
// 调用方应对结果做短时缓存：环境页每 3 秒轮询一次，逐运行时各自枚举一遍整机进程表是浪费。
