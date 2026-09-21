package services

import (
	"fmt"
	"sync"

	"quickdock/internal/logger"
	"quickdock/internal/winmgr"
)

// ===== 窗口管理浮层（Ctrl+Alt+W）=====
//
// 交互（一步到位）：按下热键 → 先记录当时的前台窗口为「排版目标」→ 显示浮层
// （9 种布局模板）→ 点模板里的某个格子 → 整块模板生效：目标窗口进这一格，
// 其余格子自动用同屏其他可见窗口按 Z 序填充（不够则留空）。
//
// 没有「选模板 → 再进一页把窗口分配到格子」的第二页，也不需要手动勾选窗口：
// 一次点击 = 一次整屏整理。
//
// 目标窗口必须在浮层被创建 / Show() 之前捕获——浮层一旦存在并获得焦点，
// GetForegroundWindow 就指向 QuickDock 自身，那时再取已经拿不到用户想排的窗口了。
// 捕获与显示的顺序由 tray.go 的 handleWinmgrFloatHotkey 保证。

// tilingTarget 本次浮层会话的排版目标窗口（浮层显示前捕获）。
var (
	tilingTargetMu    sync.Mutex
	tilingTargetHWND  uintptr
	tilingTargetTitle string
)

// CaptureTilingTarget 记录当前前台窗口为本次排版的「目标窗口」。
//
// 由托盘热键回调在「取窗口 + 显示浮层」之前调用。winmgr.ForegroundInfo 内部已处理
// 「前台是 QuickDock 自己」的情况（沿 Z 序向下找最近的第三方窗口），所以这里拿到的
// 是用户真正想排的那个窗口；真的一个都找不到时才置空。
func (a *AppService) CaptureTilingTarget() {
	info, err := winmgr.ForegroundInfo()
	tilingTargetMu.Lock()
	defer tilingTargetMu.Unlock()
	if err != nil || !winmgr.WindowAlive(info.HWND) {
		// 捕获失败就清掉上一次的目标：宁可提示用户重新激活窗口，
		// 也不能拿一个过期/已关闭的窗口去排版（用户会看到「点了没反应」）。
		tilingTargetHWND, tilingTargetTitle = 0, ""
		logger.W("QuickDock: 未捕获到排版目标窗口: %v", err)
		return
	}
	tilingTargetHWND, tilingTargetTitle = info.HWND, info.Title
	logger.I("QuickDock: 排版目标窗口 = %q (hwnd=%d)", info.Title, info.HWND)
}

// tilingTargetInfo 排版浮层启动时需要的上下文（ApiResult.data）。
//
// ScreenW/ScreenH 是目标屏工作区的物理尺寸：浮层按这个比例画模板缩略图，
// 保证「缩略图里的半屏」就是屏幕上真实的半屏。Peers 是同屏可参与填充的窗口数。
type tilingTargetInfo struct {
	HWND    uintptr `json:"HWND"`
	Title   string  `json:"Title"`
	ScreenW int     `json:"ScreenW"`
	ScreenH int     `json:"ScreenH"`
	Peers   int     `json:"Peers"`
}

// GetTilingTarget 返回本次捕获的排版目标窗口 + 排版作用范围，供浮层显示「将要移动哪个窗口」、
// 按目标屏比例绘制模板缩略图，并提前告知「还有几个窗口会被一起排」。
//
// 未捕获到（按热键时没有可用的第三方前台窗口）时 HWND=0，前端据此提示用户先激活目标窗口。
// 目标窗口在浮层打开期间被关掉时同样返回空：否则用户点了格子才发现没反应。
func (a *AppService) GetTilingTarget() *ApiResult {
	tilingTargetMu.Lock()
	defer tilingTargetMu.Unlock()
	if !winmgr.WindowAlive(tilingTargetHWND) {
		tilingTargetHWND, tilingTargetTitle = 0, ""
	}
	info := tilingTargetInfo{HWND: tilingTargetHWND, Title: tilingTargetTitle}
	// 浮层的排版固定作用于光标所在屏（浮层就在光标屏弹出，所见即所得）。
	if plan, err := winmgr.PlanTiling(tilingTargetHWND, true); err == nil {
		info.ScreenW, info.ScreenH, info.Peers = plan.ScreenW, plan.ScreenH, plan.Peers
	}
	return Ok(info)
}

// ApplyTiling 把一整块布局模板应用到目标屏。
//
// cells 是模板的几何（归一化矩形，相对目标屏工作区）；targetCell 是用户点中的格子下标——
// 本次捕获的目标窗口进这一格，其余格子由后端自动用同屏其他可见窗口（Z 序，最近用过在前）
// 依次填充，同屏窗口不够时后面的格子留空（不会重复塞同一个窗口）。
//
// useCursorScreen: true=按光标所在屏换算（用户正注视的显示器）；false=按目标窗口所在屏。
// 返回实际排版结果（成功窗口数 + 参与窗口标题），供浮层回显。
func (a *AppService) ApplyTiling(cells []winmgr.TilingCell, targetCell int, useCursorScreen bool) *ApiResult {
	tilingTargetMu.Lock()
	target := tilingTargetHWND
	tilingTargetMu.Unlock()

	if target == 0 {
		return Fail(fmt.Errorf("未捕获到目标窗口，请先激活要移动的窗口再按热键"))
	}
	if !winmgr.WindowAlive(target) {
		tilingTargetMu.Lock()
		tilingTargetHWND, tilingTargetTitle = 0, ""
		tilingTargetMu.Unlock()
		return Fail(fmt.Errorf("目标窗口已关闭，请重新激活要移动的窗口"))
	}

	res, err := winmgr.ApplyTiling(cells, targetCell, target, useCursorScreen)
	if err != nil {
		return Fail(err)
	}
	if res.Applied == 0 {
		return Fail(fmt.Errorf("没有窗口被排版，请确认目标窗口仍显示在屏幕上"))
	}
	return Ok(res)
}

// HideWinmgrWindow 隐藏窗口管理浮层（前端 X 按钮与贴屏完成后调用）。
//
// 前端不能用 window.close()：WebView2 顶层窗口的 window.close() 会被忽略，
// 表现为「点了 X 没反应」——所以关闭/隐藏必须走这里。
func (a *AppService) HideWinmgrWindow() {
	if a.Flags != nil {
		a.Flags.Winmgr.Store(false)
	}
	if fn := a.GetWinmgrWindow; fn != nil {
		if win := fn(); win != nil {
			win.Hide()
		}
	}
}
