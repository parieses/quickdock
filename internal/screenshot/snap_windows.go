//go:build windows

package screenshot

import (
	"syscall"
	"unsafe"
)

// 窗口吸附：鼠标悬停时高亮光标下的窗口并显示它的尺寸，单击即选中整个窗口。
//
// 为什么不能直接用 WindowFromPoint：
// 覆盖层铺满整个虚拟桌面、又是 topmost，WindowFromPoint 永远只会返回覆盖层自己。
// 因此这里从 z 序顶端开始逐个顶层窗口试探（GetTopWindow + GW_HWNDNEXT），
// 跳过自身窗口、不可见窗口、被 DWM 隐去的窗口以及点击穿透窗口，
// 取第一个包含光标点的——这与 Win32 的命中测试语义一致，因为 z 序本身就是从顶到底。
//
// 坐标系：本文件的入参出参都是**虚拟桌面屏幕坐标**（物理像素）。
// 覆盖层客户区坐标 = 屏幕坐标 - bounds 原点，换算在调用方做。

const (
	// snapMinExtent：小于此尺寸的目标不吸附（滚动条、分隔线之类）。
	snapMinExtent = 16
	// snapElementDepth：元素级吸附（按住 Ctrl）最多下钻的层数，
	// 防止某些自绘控件里出现环形父子关系时死循环。
	snapElementDepth = 8
)

// ownWindow 报告 hwnd 是否属于 QuickDock 自己（覆盖层 / 文字输入窗 / 贴图钉屏）。
//
// 必须排除：覆盖层恒在最上层，不排除就什么都吸不到；钉图窗口是「已经贴在屏幕上的
// 内容」，吸附它没有意义（但截图本身会把它拍进去，这是有意的——它是桌面的一部分）。
func (o *Overlay) ownWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return true
	}
	if hwnd == o.hwnd || (o.ti.hwnd != 0 && hwnd == o.ti.hwnd) {
		return true
	}
	if _, ok := pinRegistry.Load(hwnd); ok {
		return true
	}
	return false
}

// desktopClass 报告窗口类是否属于「桌面」本身。
// 吸附到它们等于吸附整个屏幕，没有信息量，直接排除。
func desktopClass(name string) bool {
	switch name {
	case "Progman", "WorkerW": // 桌面与壁纸宿主
		return true
	}
	return false
}

// windowClass 取窗口类名。
func windowClass(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetClassNameW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

// windowRect 取窗口在屏幕坐标下的矩形（含非客户区）。
//
// 宿主已在 wails.exe.manifest 声明 permonitorv2 DPI 感知，GetWindowRect 返回的就是
// 物理像素，与抓屏、覆盖层坐标系一致，无需再换算。
func windowRect(hwnd uintptr) (Rect, bool) {
	var r rectStruct
	if ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return Rect{}, false
	}
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 0 || h <= 0 {
		return Rect{}, false
	}
	return Rect{X: int(r.Left), Y: int(r.Top), W: w, H: h}, true
}

// isCloaked 报告窗口是否被 DWM 隐去。
//
// 典型场景：已最小化的 UWP 应用、位于别的虚拟桌面上的窗口——它们的
// IsWindowVisible 仍然返回 true，但屏幕上什么都没有，吸上去会得到一个空框。
func isCloaked(hwnd uintptr) bool {
	var cloaked int32
	ret, _, _ := procDwmGetWindowAttribute.Call(
		hwnd,
		dwmwaCloaked,
		uintptr(unsafe.Pointer(&cloaked)),
		unsafe.Sizeof(cloaked),
	)
	return ret == 0 && cloaked != 0
}

// usableTopWindow 报告一个顶层窗口是否值得作为吸附目标。
// 只做「便宜且不看内容」的判定；窗口矩形与类名在真正可能的候选上才取
// （见 topWindowAt 的顺序），否则每次鼠标移动都要为上百个窗口各取一次类名字符串。
func usableTopWindow(hwnd uintptr) bool {
	if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
		return false
	}
	v, _, _ := procGetWindowLongPtrW.Call(hwnd, iptr(gwlpExStyle))
	return v&wsExTransparent == 0
}

// topWindowAt 按 z 序自顶向下找第一个包含点 (sx, sy) 的顶层窗口；无则返回 0。
//
// 检查顺序按「代价从低到高」排：可见性 / 扩展样式 → 窗口矩形 → DWM 隐去 →
// 类名。鼠标移动是个高频路径，只有先过前几关的少数候选才值得去取类名（要分配字符串）
// 和问 DWM（跨进程调用）。
func (o *Overlay) topWindowAt(sx, sy int) uintptr {
	hwnd, _, _ := procGetTopWindow.Call(0)
	for hwnd != 0 {
		// 先取下一个：下面的分支会 continue，统一在这里推进 z 序游标。
		next, _, _ := procGetWindow.Call(hwnd, gwHwndNext)

		if !o.ownWindow(hwnd) && usableTopWindow(hwnd) {
			if r, ok := windowRect(hwnd); ok && r.Contains(sx, sy) &&
				!isCloaked(hwnd) && !desktopClass(windowClass(hwnd)) {
				return hwnd
			}
		}
		hwnd = next
	}
	return 0
}

// packPoint 按值传递 Win32 POINT（两个 LONG 打包进一个 64 位参数）。
// 不能复用 makeLong：那个按 16 位截断，这里要保留完整的 32 位有符号语义。
func packPoint(x, y int) uintptr {
	return uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32
}

// childWindowAt 返回 root 内 (sx, sy) 处最深的子窗口（元素级吸附）。
//
// 用 ChildWindowFromPointEx 而不是自己遍历子窗口：它内部就会跳过隐藏/透明子窗口，
// 与系统命中测试完全一致。逐层下钻直到返回自身（到底了）或换算出界。
func childWindowAt(root uintptr, sx, sy int) uintptr {
	cur := root
	px, py := sx, sy
	for depth := 0; depth < snapElementDepth; depth++ {
		var p pointStruct
		p.X, p.Y = int32(px), int32(py)
		procScreenToClient.Call(cur, uintptr(unsafe.Pointer(&p)))

		child, _, _ := procChildWindowFromPointEx.Call(
			cur,
			packPoint(int(p.X), int(p.Y)),
			cwpSkipInvisible|cwpSkipTransparent,
		)
		if child == 0 || child == cur {
			break
		}
		// 下钻后的坐标要重新换算，否则下一层的命中点会偏。
		r, ok := windowRect(child)
		if !ok || !r.Contains(sx, sy) {
			break
		}
		cur = child
	}
	return cur
}

// snapTargetAt 找出屏幕点 (sx, sy) 处应当吸附的矩形。
//
// element 为 true 时下钻到光标处的子控件（按住 Ctrl），否则停在顶层窗口——
// 默认整窗，因为「圈下整个窗口」是最常见的诉求，元素级是补充。
func (o *Overlay) snapTargetAt(sx, sy int, element bool) (Rect, bool) {
	base := o.topWindowAt(sx, sy)
	if base == 0 {
		return Rect{}, false
	}
	hwnd := base
	if element {
		if child := childWindowAt(base, sx, sy); child != 0 {
			hwnd = child
		}
	}

	r, ok := windowRect(hwnd)
	if !ok {
		return Rect{}, false
	}
	// 裁到覆盖层可见范围：部分在屏外的窗口只吸附可见部分
	// （否则选中后选区会超出一屏，用户既看不到也调不了）。
	r = r.intersect(o.bounds)
	if r.Empty() || r.W < snapMinExtent || r.H < snapMinExtent {
		return Rect{}, false
	}
	return r, true
}

// drawSnapHighlight 画光标下窗口的高亮框与尺寸提示。
//
// 双色描边（外深蓝 + 内白）是为了在深色桌面与亮色窗口上都分得清边界；
// 尺寸提示复用选区那套（drawSizeHint），位置与样式因此和框选完全一致。
func (o *Overlay) drawSnapHighlight() {
	r := o.snap
	if r.Empty() {
		return
	}
	p := o.screenPainter()
	p.strokeRect(r, 2, colSelBorder)
	inner := Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}
	if !inner.Empty() {
		p.strokeRect(inner, 1, colSnapInner)
	}
	o.drawSizeHint(r)
}
