package services

import "sync/atomic"

// WindowFlags 主窗口与各浮窗的「可见 / 模式」标志。
//
// 此前 main 包持有 4 个独立的包级 atomic.Bool，逐个以裸指针注入 AppService，
// services 侧直接改写 main 的全局变量——跨包共享裸指针，归属与生命周期都不清晰，
// 每个使用点还得先判 nil（注入前调用就会静默失效）。
// 收成单一对象后由 main 持有同一实例并显式交给 AppService，读写入口唯一。
//
// ⚠️ atomic.Bool 不可复制：本结构体一律以指针传递，不要取副本或按值嵌入。
type WindowFlags struct {
	Main      atomic.Bool // 主窗口当前可见
	Clipboard atomic.Bool // 剪贴板浮窗打开中
	Palette   atomic.Bool // 命令面板打开中
	Note      atomic.Bool // 快捷笔记浮窗打开中
	Winmgr    atomic.Bool // 窗口管理浮层打开中
}
