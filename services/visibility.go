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

	// AIPage 主窗口内的 AI 对话页是否在前台。
	//
	// 它不是窗口标志，但和上面几个是同一类「用户现在能看到什么」的状态，
	// 放在一起可以避免再多一个跨包注入的裸指针。
	//
	// 为什么需要：Main 只说明「窗口在屏幕上」，说明不了「窗口里停在哪一页」。
	// 人停在 AI 页看着回答一个字一个字出来，此时窗口可能因为用户去点了别的
	// 应用而失焦——按 MainWindowActive 判断就会往通知中心塞一条「AI 回答已完成」，
	// 而他刚刚就看着这句话生成完。通知的价值在于替用户盯着他看不到的地方。
	AIPage atomic.Bool
}

// MainWindowActive 报告主窗口是否「正被用户盯着」（可见且有焦点）。
//
// 用途：决定某些提醒要不要发。通知的价值在于替用户盯着他看不到的地方；
// 窗口就在眼前、还往通知中心塞一条，只会把真正需要看的信息淹掉。
func (a *AppService) MainWindowActive() bool {
	if a.Flags != nil && !a.Flags.Main.Load() {
		return false
	}
	w := a.MainWindow
	if w == nil {
		return false
	}
	return w.IsFocused()
}

// AIPageVisible 报告 AI 对话页是否在前台（由前端 SetAIPageVisible 维护）。
func (a *AppService) AIPageVisible() bool {
	return a.Flags != nil && a.Flags.AIPage.Load()
}

// SetAIPageVisible 由前端在进入/离开 AI 对话页时调用（KeepAlive 的 activated/deactivated）。
//
// 不返回错误：这只是一个提示位，设置失败（Flags 未注入）也不该打扰调用方。
func (a *AppService) SetAIPageVisible(visible bool) *ApiResult {
	if a.Flags != nil {
		a.Flags.AIPage.Store(visible)
	}
	return Ok(nil)
}
