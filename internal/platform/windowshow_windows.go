//go:build windows

package platform

import (
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// 浮窗「屏外预热」参数。
//
// 为什么需要：Wails 的 Hide() 除了 SW_HIDE，还会调 chromium.Hide() —— 后者是
// WebView2 的 put_IsVisible(false)，控制器会据此**释放 GPU surface**。下次 Show()
// 重新提交换算时，合成器手上没有内容，DWM 用清除色画出一帧，就是用户看到的
// 「纯白闪一下」。这一帧与 App 主题无关（深色 / 浅色下都是同一个白），也不是
// 窗口 BackgroundColour 能盖住的 —— 它发生在 WebView2 子窗口内部，画在我们的
// 客户区之上，WM_ERASEBKGND 与 DefaultBackgroundColor 都轮不到。
//
// 解法：Show 之前先把窗口挪到屏幕外，让「还原 surface + 重新合成」的那一帧落在
// 用户看不到的地方，等它画完再移回目标位置。窗口此时仍是 SW_SHOW、controller 也
// 仍是 IsVisible=true（我们没碰 chromium.Hide() 的恢复路径），Chromium 不会因为
// 「不在屏幕内」而抑制渲染，只是合成结果暂时落在屏外。移回时 surface 已有内容，
// 用户看到的是完整窗口，没有白帧。
//
// 这条路径只处理「复用唤起」的白帧。首次懒创建时页面本身还在加载，白屏由
// waitFloatingFirstPaint 的首帧门控负责，与本函数互不干扰。
const (
	// showWarmupOffscreen 是预热期间窗口所在的哨兵坐标（物理像素）。
	// 取 -32000 是 Win32 里「屏外」的惯用值：任何真实显示器的工作区都远在其右下方，
	// 即使用户把副屏摆在负坐标（如 -1920 起步）也不会落到可见区域。
	showWarmupOffscreen = -32000

	// showWarmupDelay 是「移到屏外 → 移回目标位置」之间的等待。
	// WebView2 还原 surface 并重新合成约需 2~3 帧（60Hz 下 32~48ms），取 60ms：
	// 再长用户会感到「按了热键，窗口出得慢半拍」，再短则可能移回时还没画完。
	showWarmupDelay = 60 * time.Millisecond
)

// ShowFloatingWindow 把浮窗定位到光标所在屏幕并显示，且不闪白帧。
// 取代原来「SetWindowToCursorScreen + Show + Focus」的三步调用。
func ShowFloatingWindow(win *application.WebviewWindow, winWidth, winHeight int) {
	if win == nil {
		return
	}
	SetWindowToCursorScreen(win, winWidth, winHeight)

	// 读回刚设好的**物理**目标位置：SetWindowToCursorScreen 走的是物理像素
	// （setWindowPosPhysical），而 win.Position() 是 DIP，两者不能混用，
	// 否则高 DPI / uiZoom 下窗口移回时会偏一截。
	nw := win.NativeWindow()
	if nw == nil {
		win.Show()
		win.Focus()
		return
	}
	rect := w32.GetWindowRect(w32.HWND(uintptr(nw)))
	if rect == nil {
		win.Show()
		win.Focus()
		return
	}
	x, y := int(rect.Left), int(rect.Top)

	setWindowPosPhysical(win, showWarmupOffscreen, showWarmupOffscreen)
	win.Show()
	win.Focus()
	time.AfterFunc(showWarmupDelay, func() {
		// 不判可见性：窗口若在预热期间被收起（失焦自动隐藏 / 再按一次热键），
		// 这里把位置挪回目标坐标仍然是对的 —— 下次唤起的起点就在屏幕内，
		// 不会出现「窗口停在屏外、热键越按越找不到」的状态。
		setWindowPosPhysical(win, x, y)
	})
}
