package services

import (
	"quickdock/internal/lock"
	"quickdock/internal/logger"
)

// LockStatusView 启动锁状态视图（前端锁层初始化、托盘菜单分叉共用）。
type LockStatusView struct {
	HasPassword bool `json:"hasPassword"`
	Locked      bool `json:"locked"`
	// RetryAfterSec 失败退避剩余秒数；0 表示可以立刻尝试。
	// 由后端计时，前端只负责展示倒计时——把计时放前端等于让调用方绕过退避。
	RetryAfterSec int `json:"retryAfterSec"`
}

func lockStatusView() LockStatusView {
	sec := 0
	if wait := lock.RetryAfter(); wait > 0 {
		sec = int(wait.Seconds() + 0.999) // 向上取整：剩 0.2s 也显示 1s，避免显示 0 却仍被拒
	}
	return LockStatusView{
		HasPassword:   lock.HasPassword(),
		Locked:        lock.IsLocked(),
		RetryAfterSec: sec,
	}
}

// LockStatus 查询启动锁状态。前端每次挂载都会调一次做校准（启动遮罩依赖它）。
func (a *AppService) LockStatus() *ApiResult {
	return Ok(lockStatusView())
}

// VerifyPassword 校验启动密码，通过则解锁并通知所有窗口。
func (a *AppService) VerifyPassword(password string) *ApiResult {
	if err := lock.Verify(password); err != nil {
		return Fail(err)
	}
	return Ok(lockStatusView())
}

// SetStartupPassword 首次设置启动密码，返回只展示一次的恢复码明文。
func (a *AppService) SetStartupPassword(password string) *ApiResult {
	code, err := lock.SetPassword(password)
	if err != nil {
		return Fail(err)
	}
	// 「有没有密码」刚刚发生变化：锁定热键要立刻注册上、托盘要出现「锁定」项。
	a.refreshLockAffordances()
	return Ok(map[string]string{"recoveryCode": code})
}

// ChangeStartupPassword 校验旧密码后更换密码，旧恢复码同时作废并返回新的。
func (a *AppService) ChangeStartupPassword(oldPassword, newPassword string) *ApiResult {
	code, err := lock.ChangePassword(oldPassword, newPassword)
	if err != nil {
		return Fail(err)
	}
	return Ok(map[string]string{"recoveryCode": code})
}

// RemoveStartupPassword 校验密码后彻底移除启动锁。
func (a *AppService) RemoveStartupPassword(password string) *ApiResult {
	if err := lock.RemovePassword(password); err != nil {
		return Fail(err)
	}
	// 与设置反向：注销锁定热键、撤掉托盘的「锁定」项。
	a.refreshLockAffordances()
	return Ok(lockStatusView())
}

// RecoverStartupPassword 用一次性恢复码重置：清除密码并解锁。
func (a *AppService) RecoverStartupPassword(code string) *ApiResult {
	if err := lock.Recover(code); err != nil {
		return Fail(err)
	}
	return Ok(lockStatusView())
}

// LockNow 立即进入锁定态（托盘菜单 / 设置页 / 锁定热键）。
func (a *AppService) LockNow() *ApiResult {
	if !lock.HasPassword() {
		return FailMsg("尚未设置启动密码")
	}
	lock.Lock()
	return Ok(lockStatusView())
}

// initLockBridge 把锁状态变化翻译成窗口与界面副作用。
//
// 只注册一次（NewAppService 内）。锁定方向要额外处理「锁层盖不住」的窗口：
// 插件窗走 plugin.html、DSH 窗加载第三方页面，两者都不是本地 SPA，
// 无法共用 App.vue 的锁层，只能隐藏。解锁方向不主动打开任何窗口——
// 解锁瞬间弹一堆窗口比不弹更烦。
func (a *AppService) initLockBridge() {
	lock.Subscribe(func(locked bool) {
		if locked {
			a.hideUnlockableWindows()
		}
		// 托盘菜单按锁状态分叉，两个方向都要重建（main 包注入，未注入时跳过：
		// 托盘还没建，也就不需要刷新）。
		//
		// 必须放后台：buildTrayMenu 的「服务」子菜单要逐个运行时探测进程树与端口，
		// 已装运行时一多就是几百毫秒。它一旦挂在同步路径上，就卡在密码校验的返回
		// 之前——用户看到的就是「按了解锁，界面半天不动」。托盘晚一拍切换到完整
		// 菜单没人会注意，而锁层早 300ms 消失是实打实的。
		a.rebuildTrayAsync()
		if a.app != nil {
			// 全局广播：主题变更就是这么同步到插件窗口的，锁层要的正是同一套机制——
			// 一次解锁必须让 6 个 SPA 窗口同时消失遮罩。
			a.app.Event.Emit("lock:changed", map[string]any{"locked": locked})
		}
	})
}

// rebuildTrayAsync 后台重建托盘菜单。
//
// rebuildTrayMenu（main 包）内部用原子序号丢弃过期结果，所以并发调用安全：
// 先发起的慢请求不会把后发起的新菜单盖掉。
func (a *AppService) rebuildTrayAsync() {
	if a.RebuildTrayFn == nil {
		return
	}
	fn := a.RebuildTrayFn
	go fn()
}

// refreshLockAffordances 在「是否设过密码」变化后刷新与锁相关的入口。
//
// 热键不是每次重建托盘都能补上的：registerAllHotkeys 只在启动与 ResumeHotkeys
// 时执行，所以刚设完密码必须主动跑一次，否则锁定热键要等重启才生效。
// 托盘重建同样走后台——它带着服务状态探测，同步跑会把「点设置密码」也拖慢一拍。
func (a *AppService) refreshLockAffordances() {
	a.rebuildTrayAsync()
	if a.ResumeHotkeysFn != nil {
		a.ResumeHotkeysFn()
	}
}

// hideUnlockableWindows 隐藏锁层覆盖不到、又承载用户数据的窗口。
func (a *AppService) hideUnlockableWindows() {
	// 浮窗（剪贴板 / 命令面板 / 笔记 / 窗口排版 / SSH）由 main 包注入的回调统一收起。
	// 锁定期间这些热键被 lockBlocksHotkey 拦住、不再弹窗，所以锁定瞬间还开着的窗口
	// 必须先收掉，否则它们会带着锁层后面的真实内容留在屏幕上。
	if a.HideFloatingWindowsFn != nil {
		a.HideFloatingWindowsFn()
	}
	if a.PluginWindowMgr != nil {
		a.PluginWindowMgr.HideAll()
	}
	if a.DSH != nil {
		a.DSH.HideWindow()
	}
	logger.I("[lock] 已同步隐藏浮窗、插件窗口与 DSH 窗口")
}
