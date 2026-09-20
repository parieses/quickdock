//go:build windows

package screenshot

import "testing"

// containsRect 报告 inner 是否完全落在 outer 内。
func containsRect(outer, inner Rect) bool {
	if inner.Empty() {
		return true
	}
	return inner.X >= outer.X && inner.Y >= outer.Y &&
		inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
}

// TestMonitorWorkArea 校验 MonitorFromPoint 的 POINT 打包与 GetMonitorInfoW 取工作区。
// 这一层错了不会编译报错、只会静默拿到垃圾数据，所以单独钉一个测试。
func TestMonitorWorkArea(t *testing.T) {
	b := VirtualDesktopBounds()
	if b.Empty() {
		t.Fatalf("虚拟桌面尺寸无效")
	}

	// 取虚拟桌面中心：多显示器时它可能落在「没有屏幕覆盖」的死角，
	// MONITOR_DEFAULTTONEAREST 应当仍返回最近的一块显示器。
	hmon := monitorFromPoint(b.X+b.W/2, b.Y+b.H/2)
	if hmon == 0 {
		t.Fatalf("monitorFromPoint 返回 0（POINT 打包或调用有误）")
	}

	work, ok := monitorWork(hmon)
	if !ok {
		t.Fatalf("monitorWork 失败")
	}
	if work.Empty() {
		t.Fatalf("工作区为空")
	}
	if !containsRect(b, work) {
		t.Fatalf("工作区 %+v 不在虚拟桌面 %+v 内，坐标换算可能错位", work, b)
	}
	t.Logf("bounds=%+v work=%+v", b, work)
}

// TestLayoutToolbarFullScreen 钉住全屏截图这类「选区几乎占满屏幕」的摆放。
//
// 复现用户实机几何（双屏，左屏比主屏高/窄，虚拟桌面因此带负坐标与死角）：
//
//	bounds       = {-1920, -123, 3840, 1203}   虚拟桌面
//	主屏工作区    = { 1920,  123, 1920, 1032}   客户区坐标（屏幕 0,0 起）
//	任务栏        = 主屏底部 48px（工作区外）
//
// 改前按虚拟桌面边界避让，全屏选区会把工具条摆到客户区 (0, 全屏高-48) ——
// 换算成屏幕坐标是 (-1920, 1032)：x 属于左屏、y 已过左屏底边，那里**没有任何
// 显示器覆盖**，等于画在没有像素的地方，用户于是「操作栏看不到了」。
// 更糟的是即使落在主屏上，那条带子也正是任务栏。
func TestLayoutToolbarFullScreen(t *testing.T) {
	work := Rect{X: 1920, Y: 123, W: 1920, H: 1032} // 主屏工作区（客户区坐标）
	sel := Rect{X: 0, Y: 0, W: 3840, H: 1203}       // 全屏选区 = 整个虚拟桌面

	lay := layoutToolbarIn(work, sel)

	if !containsRect(work, lay.bar) {
		t.Fatalf("工具条 %+v 越出可用区 %+v", lay.bar, work)
	}
	// 关键回归：不能落进任务栏那条带子（工作区底边之外）。
	if lay.bar.Y+lay.bar.H > work.Y+work.H {
		t.Fatalf("工具条 %+v 压到任务栏/越出可用区底边", lay.bar)
	}
	// 不能退化到改前的死角：客户区 x 属于左屏、y 超过左屏底边。
	dead := Rect{X: 0, Y: 1155, W: toolbarWidth(), H: tbHeight}
	if lay.bar == dead {
		t.Fatalf("工具条又回到了虚拟桌面左下死角 %+v", lay.bar)
	}
	// 命中格必须跟着 bar 走，否则点不到按钮。
	for i, c := range lay.cells {
		if !containsRect(lay.bar, c) {
			t.Fatalf("第 %d 个命中格 %+v 不在工具条 %+v 内", i, c, lay.bar)
		}
	}

	// 只框住主屏（更常见的「全屏」动作）同样要落在主屏工作区内。
	// 注意客户区 y=0 并**不是**主屏顶边——虚拟桌面比主屏高出 123px（左屏更高），
	// 早先拿客户区 0 当「上方还有空间」，工具条就会被摆到主屏上方那片没有屏幕的
	// 区域（客户区 y≈75 → 屏幕 y≈-48）。
	sel = Rect{X: 1920, Y: 123, W: 1920, H: 1080} // 主屏，客户区坐标
	lay = layoutToolbarIn(work, sel)
	if !containsRect(work, lay.bar) {
		t.Fatalf("主屏全屏时工具条 %+v 越出主屏可用区 %+v", lay.bar, work)
	}
}

// TestLayoutToolbarBelowSelection 保证常规情形没被改坏：选区下方放得下就放下方、
// 左边缘对齐；下方伸进任务栏时上翻。
func TestLayoutToolbarBelowSelection(t *testing.T) {
	work := Rect{X: 0, Y: 0, W: 1920, H: 1032} // 主屏工作区（任务栏 48px）

	// 常规：选区在屏幕中间，下方有空间。
	sel := Rect{X: 100, Y: 100, W: 400, H: 300}
	lay := layoutToolbarIn(work, sel)
	if lay.bar.X != sel.X {
		t.Fatalf("应左对齐选区左边缘：bar.X=%d want=%d", lay.bar.X, sel.X)
	}
	if want := sel.Y + sel.H + tbGap; lay.bar.Y != want {
		t.Fatalf("应贴选区下方：bar.Y=%d want=%d", lay.bar.Y, want)
	}

	// 选区下沿距任务栏只有 20px —— 工具条高 40px，放下面必然压到任务栏，必须上翻。
	sel = Rect{X: 100, Y: 300, W: 400, H: 712} // 底边 y=1012，距工作区底 20px
	lay = layoutToolbarIn(work, sel)
	if lay.bar.Y+lay.bar.H > work.Y+work.H {
		t.Fatalf("工具条 %+v 压到任务栏", lay.bar)
	}
	if lay.bar.Y >= sel.Y+sel.H {
		t.Fatalf("下方放不下时应上翻到选区上方，bar=%+v sel=%+v", lay.bar, sel)
	}
}
