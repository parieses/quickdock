//go:build windows

package screenshot

import (
	"fmt"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// 取色器（Snipaste 式）：截图覆盖层里跟着光标走的一张信息卡。
//
// 【为什么画进主 DIB，而不是再开一个分层窗口】
// 覆盖层刻意只有一张 32 位 DIB、一次 UpdateLayeredWindow 提交（见 overlay_windows.go
// 文件头）。再开一个窗口就要单独抢焦点、单独处理鼠标穿透、单独做一次提交，还会
// 和工具条的弹出面板抢层级。画进同一张 DIB 的代价只是把卡片矩形登记进 footprint，
// 由现成的脏矩形机制负责「上一帧的卡片位置」的还原。
//
// 【为什么从 o.src 取色，而不是 GetPixel】
// 屏幕此刻已经被这张暗化遮罩盖住了，GetPixel 取到的是遮罩本身（或干脆是透明黑）。
// o.src 是按下 F1 那一刻抓下的原始整屏位图，且是未暗化的 top-down BGRA，直接按
// 客户区坐标索引就是光标下那一颗真实像素——顺带天然支持多显示器与 DPI 缩放。
//
// 卡片只是 UI，不接收鼠标：窗口整片都是客户区（htClient），点在上面依旧走
// 框选/标注逻辑，与 Snipaste 的取色面板行为一致。

const (
	pickerGridN     = 9 // 放大镜采样 9×9 颗原始像素
	pickerPad       = 8 // 卡片内边距
	pickerGap       = 9 // 放大镜与右侧文本间距
	pickerTextW     = 180
	pickerLineH     = 17 // 右侧文本行高
	pickerHintH     = 14 // 底部快捷键提示行高
	pickerRadius    = 5
	pickerOffset    = 18 // 卡片与光标的距离，避免压住光标热点
	pickerFontH     = 12
	pickerHintFontH = 11
)

// pickerCells 是滚轮可调的放大镜档位（每颗原始像素显示成几像素见方）。
// 9×9 固定、格子变大 → 视觉上放大倍率变高，但采样范围不变，取色点始终是中心格。
var pickerCells = [...]int{5, 7, 9, 11}

var (
	colPickerBg     = col{0x1E, 0x1E, 0x1E} // #1E1E1E 比尺寸提示更深，压得住任何底图
	colPickerBorder = col{0x8A, 0x8A, 0x8A} // #8A8A8A
	colPickerText   = col{0xFF, 0xFF, 0xFF}
	colPickerDim    = col{0xB4, 0xB4, 0xB4}
	colPickerOOB    = col{0x40, 0x40, 0x40} // 采样越界（屏幕边缘）时填的格子
	colBlack        = col{0x00, 0x00, 0x00}
	colWhite        = col{0xFF, 0xFF, 0xFF}
)

// pickerCell 返回当前放大镜格子边长。索引越界时退回默认档，避免滚轮把状态写坏。
func (o *Overlay) pickerCell() int {
	i := o.pickerZoomIdx
	if i < 0 || i >= len(pickerCells) {
		i = 1
	}
	return pickerCells[i]
}

// colorCardSize 返回卡片宽高。倍率改变时视口跟着变，卡片尺寸也随之变，
// 位置计算与 footprint 都调同一个函数，不会出现「算位置用旧尺寸」的错位。
func (o *Overlay) colorCardSize() (int, int) {
	view := pickerGridN * o.pickerCell()
	body := maxInt(view, pickerLineH*4)
	w := pickerPad*2 + view + pickerGap + pickerTextW
	h := pickerPad*2 + body + 6 + 1 + 6 + pickerHintH
	return w, h
}

// colorCardVisible 判断这一帧要不要画取色卡。
//
// 三种情况不画：鼠标还没进来（首帧）、正在输入文字标注、光标压在工具条或
// 弹出面板上。最后一条不是怕挡住按钮那么简单——卡片跟着光标跑，鼠标移到
// 工具条上时卡片会盖掉刚要看的按钮，比没有卡片更糟。
func (o *Overlay) colorCardVisible() bool {
	if !o.pickerShow || !o.mouseIn || o.src == nil || o.src.Empty() || o.hMemDC == 0 {
		return false
	}
	if o.ti.active {
		return false
	}
	x, y := o.mouseX, o.mouseY
	if x < 0 || y < 0 || x >= o.bounds.W || y >= o.bounds.H {
		return false
	}
	if o.phase == phaseAdjusting {
		if bar := o.layoutToolbar(o.sel).bar; !bar.Empty() && bar.Contains(x, y) {
			return false
		}
		if pr := o.panelRect(); !pr.Empty() && pr.Contains(x, y) {
			return false
		}
	}
	return true
}

// colorCardRect 挑一个不越界、也不压住工具条的位置。
//
// 光标右下角优先；右侧或下方放不下就翻到另一侧。四个候选都跟工具条/面板相交时
// 干脆不画——宁可这一帧没有卡片，也别盖住用户正在操作的按钮。
func (o *Overlay) colorCardRect() Rect {
	if !o.colorCardVisible() {
		return Rect{}
	}
	w, h := o.colorCardSize()
	mx, my := o.mouseX, o.mouseY

	bar := Rect{}
	pr := Rect{}
	if o.phase == phaseAdjusting {
		bar = o.layoutToolbar(o.sel).bar
		pr = o.panelRect()
	}

	cands := [][2]int{
		{mx + pickerOffset, my + pickerOffset},
		{mx + pickerOffset, my - h - pickerOffset},
		{mx - w - pickerOffset, my + pickerOffset},
		{mx - w - pickerOffset, my - h - pickerOffset},
	}
	for _, c := range cands {
		x := clampInt(c[0], 0, maxInt(0, o.bounds.W-w))
		y := clampInt(c[1], 0, maxInt(0, o.bounds.H-h))
		r := Rect{X: x, Y: y, W: w, H: h}
		if !bar.Empty() && rectsOverlap(r, bar) {
			continue
		}
		if !pr.Empty() && rectsOverlap(r, pr) {
			continue
		}
		return r
	}
	return Rect{}
}

func rectsOverlap(a, b Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// pixelRGB 读原始截图里的一颗像素，返回 R/G/B（不是 col 的 BGR 顺序）。
func (o *Overlay) pixelRGB(x, y int) (r, g, b byte, ok bool) {
	if o.src == nil || o.src.Empty() || x < 0 || y < 0 || x >= o.src.W || y >= o.src.H {
		return 0, 0, 0, false
	}
	off := (y*o.src.W + x) * 4
	if off+3 >= len(o.src.Pix) {
		return 0, 0, 0, false
	}
	p := o.src.Pix[off:]
	return p[2], p[1], p[0], true
}

// pixelCol 同上，但直接给画笔用的 BGR 顺序。
func (o *Overlay) pixelCol(x, y int) (col, bool) {
	r, g, b, ok := o.pixelRGB(x, y)
	return col{b, g, r}, ok
}

// drawColorCard 画整张取色卡。由 redraw 在所有 UI 之后调用，天然处在最上层。
func (o *Overlay) drawColorCard() {
	box := o.colorCardRect()
	// 记下「这一帧画了卡片」。下一帧若卡片被隐藏（鼠标移到工具条、按 P），
	// onMouseMove 靠这个标记排帧，把旧位置还原掉，否则会留一张不动的残影。
	o.pickerDrawn = !box.Empty()
	if box.Empty() {
		return
	}
	r, g, b, ok := o.pixelRGB(o.mouseX, o.mouseY)
	if !ok {
		return
	}
	hex := fmt.Sprintf("#%02X%02X%02X", r, g, b)
	p := o.screenPainter()

	p.fillRoundRect(box, pickerRadius, colPickerBg)
	p.strokeRoundRect(box, pickerRadius, 1, colPickerBorder)

	cell := o.pickerCell()
	view := pickerGridN * cell
	vx, vy := box.X+pickerPad, box.Y+pickerPad

	// 放大镜：每格就是一颗原始像素，不插值。取色要的是「这颗像素到底是多少」，
	// 插值会把边界抹成第三种颜色，看着漂亮但读数不对。
	half := pickerGridN / 2
	for gy := 0; gy < pickerGridN; gy++ {
		for gx := 0; gx < pickerGridN; gx++ {
			c, valid := o.pixelCol(o.mouseX+gx-half, o.mouseY+gy-half)
			if !valid {
				c = colPickerOOB
			}
			p.fillRect(Rect{X: vx + gx*cell, Y: vy + gy*cell, W: cell, H: cell}, c)
		}
	}
	p.strokeRect(Rect{X: vx - 1, Y: vy - 1, W: view + 2, H: view + 2}, 1, colPickerBorder)

	// 中心格双层描边：纯色底上白框或黑框总有一个会消失，两层一起才永远看得见。
	cx, cy := vx+half*cell, vy+half*cell
	p.strokeRect(Rect{X: cx - 1, Y: cy - 1, W: cell + 2, H: cell + 2}, 1, colBlack)
	p.strokeRect(Rect{X: cx, Y: cy, W: cell, H: cell}, 1, colWhite)

	// 右侧四行：色块 + HEX、RGB 分量、虚拟桌面坐标、复制反馈。
	tx := vx + view + pickerGap
	ty := box.Y + pickerPad
	sw := 14
	p.fillRect(Rect{X: tx, Y: ty + 2, W: sw, H: sw}, col{b, g, r})
	p.strokeRect(Rect{X: tx, Y: ty + 2, W: sw, H: sw}, 1, colPickerBorder)
	o.drawText(p, hex, tx+sw+7, ty, pickerFontH, colPickerText)
	o.drawText(p, fmt.Sprintf("RGB  %d, %d, %d", r, g, b), tx, ty+pickerLineH, pickerFontH, colPickerText)
	o.drawText(p, fmt.Sprintf("X %d   Y %d", o.bounds.X+o.mouseX, o.bounds.Y+o.mouseY),
		tx, ty+pickerLineH*2, pickerFontH, colPickerDim)
	// 第四行固定放快捷键，复制反馈放底部提示行：两处都不会因为「刚复制过」
	// 而把另一条提示挤没，也不需要定时器去清「已复制」状态。
	o.drawText(p, "C 复制 · Shift+C RGB", tx, ty+pickerLineH*3, pickerFontH, colPickerDim)

	// 底部提示横跨整张卡片（放大镜下方那一行也是空的，正好利用）。
	// 文案刻意压在卡片宽度内：drawText 只按位图边界裁剪，写超了会画到卡片外面。
	hintY := box.Y + box.H - pickerPad - pickerHintH
	p.hline(box.X+pickerPad, box.X+box.W-pickerPad-1, hintY-5, colPickerBorder)
	hint := "滚轮缩放 · P 隐藏取色器"
	if o.pickerCopied != "" {
		hint = "已复制 " + o.pickerCopied + " · P 隐藏"
	}
	o.drawText(p, hint, box.X+pickerPad, hintY, pickerHintFontH, colPickerDim)
}

// zoomPicker 滚轮调放大镜档位。方向按 Windows 惯例：向前滚 = 放大。
func (o *Overlay) zoomPicker(delta int) {
	if delta == 0 {
		return
	}
	next := o.pickerZoomIdx
	if delta > 0 {
		next++
	} else {
		next--
	}
	next = clampInt(next, 0, len(pickerCells)-1)
	if next == o.pickerZoomIdx {
		return
	}
	o.pickerZoomIdx = next
	o.requestFrame()
}

// copyColorAtCursor 把光标下的颜色写进系统剪贴板。
//
// 用 platform.SetClipboardText 而不是自己开剪贴板：那条路带打开重试与 owner
// 探测，和 SSH 终端刚修过的问题同源（见 services/clipboard/clipboard.go）。
// 写入的文本会被 QuickDock 自己的剪贴板监听捕获，自动进历史，可以连续取多个色。
//
// 刻意复制完不结束会话：取色常常是「看一眼这个色、再挪到旁边看另一个」，
// 每复制一次就退出截图反而打断。Esc 仍是唯一的退出动作。
func (o *Overlay) copyColorAtCursor(asRGB bool) {
	r, g, b, ok := o.pixelRGB(o.mouseX, o.mouseY)
	if !ok {
		return
	}
	text := fmt.Sprintf("#%02X%02X%02X", r, g, b)
	if asRGB {
		text = fmt.Sprintf("rgb(%d, %d, %d)", r, g, b)
	}
	if err := platform.SetClipboardText(o.hwnd, text); err != nil {
		logger.W("[screenshot] 复制颜色失败: %v", err)
		return
	}
	o.pickerCopied = text
	logger.I("[screenshot] 已复制颜色 %s @ (%d,%d)", text,
		o.bounds.X+o.mouseX, o.bounds.Y+o.mouseY)
	// 立刻刷新，让卡片上的「已复制」反馈跟着这一下按键出现。
	o.redraw()
}
