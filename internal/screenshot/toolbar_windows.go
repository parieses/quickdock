//go:build windows

package screenshot

import (
	"math"
	"strconv"
)

// Snipaste 风格的截图工具条：框选完成后浮在选区下方（空间不足翻到上方），
// 左边缘对齐选区左边缘。
//
// 关键点是它和框选共用同一张 DIB、同一次 UpdateLayeredWindow 提交——
// 工具条与它的弹出面板都不是第二个窗口，所以「工具条出现」这个动作本身也不会闪。

type toolID uint8

const (
	toolNone toolID = iota
	toolRect
	toolEllipse
	toolArrow
	toolLine
	toolMosaic
	toolText
)

type iconID uint8

const (
	iconRect iconID = iota
	iconEllipse
	iconArrow
	iconLine
	iconMosaic
	iconText
	iconFontSize
	iconUndo
	iconRedo
	iconCopy
	iconSave
	iconPin
	iconClose
)

type tbKind uint8

const (
	tbTool tbKind = iota
	tbSeparator
	tbColor
	tbWidth
	tbFontSize
	tbUndo
	tbRedo
	tbCopy
	tbSave
	tbPin
	tbClose
)

type tbItem struct {
	kind tbKind
	tool toolID
	icon iconID
}

const (
	tbBtnSize = 30 // 按钮边长
	tbPadX    = 5
	tbPadY    = 5
	tbSepW    = 11 // 分隔格宽度
	tbHeight  = tbBtnSize + tbPadY*2
	tbRadius  = 6
	tbGap     = 8  // 工具条与选区/面板的间距
	tbIcon    = 16 // 图标绘制边长
	tbCorner  = 4  // 按钮高亮时的圆角
	tbEdge    = 1  // 工具条描边宽度
)

var tbItems = []tbItem{
	{kind: tbTool, tool: toolRect, icon: iconRect},
	{kind: tbTool, tool: toolEllipse, icon: iconEllipse},
	{kind: tbTool, tool: toolArrow, icon: iconArrow},
	{kind: tbTool, tool: toolLine, icon: iconLine},
	{kind: tbTool, tool: toolMosaic, icon: iconMosaic},
	{kind: tbTool, tool: toolText, icon: iconText},
	{kind: tbSeparator},
	{kind: tbColor},
	{kind: tbWidth},
	{kind: tbFontSize},
	{kind: tbSeparator},
	{kind: tbUndo, icon: iconUndo},
	{kind: tbRedo, icon: iconRedo},
	{kind: tbSeparator},
	{kind: tbCopy, icon: iconCopy},
	{kind: tbSave, icon: iconSave},
	{kind: tbPin, icon: iconPin},
	{kind: tbClose, icon: iconClose},
}

// 调色板。col 是 BGR 顺序（见 paint_windows.go）。
var (
	colToolbarBg     = col{0xF7, 0xF7, 0xF7} // #F7F7F7
	colToolbarBorder = col{0xD2, 0xD2, 0xD2} // #D2D2D2
	colSeparator     = col{0xDC, 0xDC, 0xDC} // #DCDCDC
	colIcon          = col{0x3A, 0x3A, 0x3A} // #3A3A3A
	colIconDisabled  = col{0xC2, 0xC2, 0xC2} // #C2C2C2
	colHoverBg       = col{0xFB, 0xF1, 0xE5} // #E5F1FB
	colActiveBg      = col{0xF3, 0x96, 0x21} // #2196F3
	colActiveIcon    = col{0xFF, 0xFF, 0xFF} // #FFFFFF
	colHintBg        = col{0x33, 0x33, 0x33} // #333333
	colHintText      = col{0xFF, 0xFF, 0xFF} // #FFFFFF
	colSwatchBorder  = col{0xB0, 0xB0, 0xB0} // #B0B0B0
	colSnapInner     = col{0xFF, 0xFF, 0xFF} // #FFFFFF 窗口吸附高亮的内圈
	colSelectBox     = col{0xF3, 0x96, 0x21} // #2196F3 选中图形时的虚线框
	colGuideBoost    = 46                    // 十字辅助线的提亮量
)

// tbLayout 是一次布局的全部命中区（客户区坐标）。
type tbLayout struct {
	bar   Rect
	cells []Rect // 与 tbItems 一一对应
}

func toolbarWidth() int {
	w := tbPadX * 2
	for _, it := range tbItems {
		if it.kind == tbSeparator {
			w += tbSepW
		} else {
			w += tbBtnSize
		}
	}
	return w
}

// layoutToolbar 按选区位置摆放工具条。
//
// 优先级：选区下方 → 选区上方 → 选区内部底边 → 选区内部顶边（选区几乎占满
// 整屏时的兜底）。水平方向左对齐选区左边缘，与 Snipaste / 微信截图一致。
func (o *Overlay) layoutToolbar(sel Rect) tbLayout {
	w, h := toolbarWidth(), tbHeight

	x := sel.X
	y := sel.Y + sel.H + tbGap
	if y+h > o.bounds.H {
		y = sel.Y - tbGap - h
	}
	if y < 0 {
		y = sel.Y + sel.H - h - tbGap
	}
	if y < 0 {
		y = sel.Y + tbGap
	}

	x = clampInt(x, 0, maxInt(0, o.bounds.W-w))
	y = clampInt(y, 0, maxInt(0, o.bounds.H-h))

	lay := tbLayout{bar: Rect{X: x, Y: y, W: w, H: h}, cells: make([]Rect, len(tbItems))}
	cx, cy := x+tbPadX, y+tbPadY
	for i, it := range tbItems {
		cw := tbBtnSize
		if it.kind == tbSeparator {
			cw = tbSepW
		}
		lay.cells[i] = Rect{X: cx, Y: cy, W: cw, H: tbBtnSize}
		cx += cw
	}
	return lay
}

// hit 返回落在工具条按钮上的条目下标；分隔格与空白返回 -1。
func (l tbLayout) hit(x, y int) int {
	if !l.bar.Contains(x, y) {
		return -1
	}
	for i, c := range l.cells {
		if tbItems[i].kind == tbSeparator {
			continue
		}
		if c.Contains(x, y) {
			return i
		}
	}
	return -1
}

// ===== 弹出面板（颜色 / 线宽）=====

type panelID uint8

const (
	panelNone panelID = iota
	panelColor
	panelWidth
	panelFontSize
)

const (
	panelPad   = 8
	swatchSize = 24
	swatchGap  = 6
	swatchRad  = 4

	widthPanelW  = 120
	widthRowH    = 22
	widthRowGap  = 6
	widthRowPadX = 12

	fontRowH     = 24 // 字号面板每行高度
	fontRowLabel = 13 // 行标签「A 16」的像素高度（固定，不随档位放大）
)

// panelOf 返回条目对应的弹出面板；无面板的条目返回 panelNone。
func panelOf(k tbKind) panelID {
	switch k {
	case tbColor:
		return panelColor
	case tbWidth:
		return panelWidth
	case tbFontSize:
		return panelFontSize
	}
	return panelNone
}

func panelSize(p panelID) (int, int) {
	switch p {
	case panelColor:
		w := panelPad*2 + len(annotColors)*swatchSize + (len(annotColors)-1)*swatchGap
		return w, panelPad*2 + swatchSize
	case panelWidth:
		h := panelPad*2 + len(annotWidths)*widthRowH + (len(annotWidths)-1)*widthRowGap
		return widthPanelW, h
	case panelFontSize:
		h := panelPad*2 + len(annotFontSizes)*fontRowH + (len(annotFontSizes)-1)*widthRowGap
		return widthPanelW, h
	}
	return 0, 0
}

// panelRect 返回面板在客户区中的位置。优先贴在工具条上方（与选区之间），
// 上方放不下则翻到工具条下方。
func (o *Overlay) panelRect() Rect {
	if o.panel == panelNone || o.phase != phaseAdjusting || o.sel.Empty() {
		return Rect{}
	}
	w, h := panelSize(o.panel)
	if w == 0 || h == 0 {
		return Rect{}
	}
	bar := o.layoutToolbar(o.sel).bar
	x := bar.X
	y := bar.Y - tbGap - h
	if y < 0 {
		y = bar.Y + bar.H + tbGap
	}
	x = clampInt(x, 0, maxInt(0, o.bounds.W-w))
	y = clampInt(y, 0, maxInt(0, o.bounds.H-h))
	return Rect{X: x, Y: y, W: w, H: h}
}

func colorSwatchRect(pr Rect, i int) Rect {
	return Rect{
		X: pr.X + panelPad + i*(swatchSize+swatchGap),
		Y: pr.Y + panelPad,
		W: swatchSize,
		H: swatchSize,
	}
}

func widthRowRect(pr Rect, i int) Rect {
	return Rect{
		X: pr.X + panelPad,
		Y: pr.Y + panelPad + i*(widthRowH+widthRowGap),
		W: pr.W - panelPad*2,
		H: widthRowH,
	}
}

func fontRowRect(pr Rect, i int) Rect {
	return Rect{
		X: pr.X + panelPad,
		Y: pr.Y + panelPad + i*(fontRowH+widthRowGap),
		W: pr.W - panelPad*2,
		H: fontRowH,
	}
}

// ===== 绘制 =====

// toolbarEnabled 报告某一项当前是否可用（决定图标是否灰显）。
func (o *Overlay) toolbarEnabled(i int) bool {
	switch tbItems[i].kind {
	case tbUndo:
		return len(o.history) > 0
	case tbRedo:
		return len(o.future) > 0
	}
	return true
}

func (o *Overlay) drawToolbar() {
	if o.phase != phaseAdjusting || o.sel.Empty() {
		return
	}
	lay := o.layoutToolbar(o.sel)

	// 刻意不带 clip：工具条在选区之外，带选区裁剪会被整条裁掉。
	p := &painter{pix: o.dibPix, w: o.bounds.W, h: o.bounds.H}

	p.fillRoundRect(lay.bar, tbRadius, colToolbarBg)
	p.strokeRoundRect(lay.bar, tbRadius, tbEdge, colToolbarBorder)

	iconOff := (tbBtnSize - tbIcon) / 2
	for i, it := range tbItems {
		cell := lay.cells[i]
		if it.kind == tbSeparator {
			sx := cell.X + cell.W/2
			p.vline(sx, cell.Y+7, cell.Y+cell.H-8, colSeparator)
			continue
		}

		active := it.kind == tbTool && o.activeTool == it.tool
		hover := o.hoverIdx == i
		enabled := o.toolbarEnabled(i)

		inner := Rect{X: cell.X + 1, Y: cell.Y + 1, W: cell.W - 2, H: cell.H - 2}

		// 颜色 / 线宽 / 字号这三个按钮的图示各自不同，单独处理。
		switch it.kind {
		case tbColor:
			hl := hover || o.panel == panelColor
			if hl {
				p.fillRoundRect(inner, tbCorner, colHoverBg)
			}
			o.drawColorButton(p, cell)
			continue
		case tbWidth:
			hl := hover || o.panel == panelWidth
			if hl {
				p.fillRoundRect(inner, tbCorner, colHoverBg)
			}
			o.drawWidthButton(p, cell)
			continue
		case tbFontSize:
			// 字号不像颜色/线宽那样能用「当前值本身」当图示（44px 的字塞不进
			// 30px 按钮），所以按钮固定画一个 A，当前档位在面板里高亮。
			if hover || o.panel == panelFontSize {
				p.fillRoundRect(inner, tbCorner, colHoverBg)
			}
			drawIcon(p, cell.X+iconOff, cell.Y+iconOff, iconFontSize, colIcon)
			continue
		}

		switch {
		case active:
			p.fillRoundRect(inner, tbCorner, colActiveBg)
		case hover && enabled:
			p.fillRoundRect(inner, tbCorner, colHoverBg)
		}

		ic := colIcon
		switch {
		case active:
			ic = colActiveIcon
		case !enabled:
			ic = colIconDisabled
		}
		drawIcon(p, cell.X+iconOff, cell.Y+iconOff, it.icon, ic)
	}
}

// drawColorButton 画「当前颜色」按钮：一个带边框的实心圆。
// 圆点本身可能就是白色，所以描边不能省。
func (o *Overlay) drawColorButton(p *painter, cell Rect) {
	cx, cy := cell.X+cell.W/2, cell.Y+cell.H/2
	p.ring(cx, cy, 9, 2, colSwatchBorder, o.curColor)
}

// drawWidthButton 用一条当前粗细的横线表示线宽。
func (o *Overlay) drawWidthButton(p *painter, cell Rect) {
	th := clampInt(o.curWidth, 1, 10)
	cy := cell.Y + cell.H/2
	p.line(cell.X+7, cy, cell.X+cell.W-8, cy, th, colIcon)
}

// drawPanel 画面板。
func (o *Overlay) drawPanel() {
	pr := o.panelRect()
	if pr.Empty() {
		return
	}
	p := &painter{pix: o.dibPix, w: o.bounds.W, h: o.bounds.H}
	p.fillRoundRect(pr, tbRadius, colToolbarBg)
	p.strokeRoundRect(pr, tbRadius, tbEdge, colToolbarBorder)

	switch o.panel {
	case panelColor:
		for i, c := range annotColors {
			r := colorSwatchRect(pr, i)
			p.fillRoundRect(r, swatchRad, c)
			p.strokeRoundRect(r, swatchRad, 1, colSwatchBorder)
			if c == o.curColor {
				out := Rect{X: r.X - 3, Y: r.Y - 3, W: r.W + 6, H: r.H + 6}
				p.strokeRoundRect(out, swatchRad+2, 2, colActiveBg)
			}
		}
	case panelWidth:
		for i, wd := range annotWidths {
			r := widthRowRect(pr, i)
			if wd == o.curWidth {
				p.fillRoundRect(r, swatchRad, colHoverBg)
			}
			cy := r.Y + r.H/2
			p.line(r.X+widthRowPadX, cy, r.X+r.W-widthRowPadX, cy, wd, colIcon)
		}
	case panelFontSize:
		for i, fs := range annotFontSizes {
			r := fontRowRect(pr, i)
			if fs == o.curFontSize {
				p.fillRoundRect(r, swatchRad, colHoverBg)
			}
			// 标签统一用 fontRowLabel 的高度画：档位最高到 44px，按真实
			// 字号画会撑破行高；数字本身已经说清大小，不需要视觉预告。
			box := Rect{X: r.X + widthRowPadX, Y: r.Y, W: r.W - widthRowPadX*2, H: r.H}
			o.drawTextVCentered(p, "A "+strconv.Itoa(fs), box, fontRowLabel, colIcon)
		}
	}
}

// drawIcon 在 (ox, oy) 处画一个 16×16 的图标。
// 内部另建一个原点平移过的 painter，好让各图标函数用 0..15 的局部坐标书写。
func drawIcon(p *painter, ox, oy int, id iconID, c col) {
	ip := &painter{pix: p.pix, w: p.w, h: p.h, dx: p.dx + ox, dy: p.dy + oy}

	switch id {
	case iconRect:
		ip.strokeRect(Rect{1, 3, 14, 10}, 2, c)

	case iconEllipse:
		ip.strokeEllipse(Rect{1, 3, 14, 10}, 2, c)

	case iconArrow:
		// 这里不能复用 painter.arrow：它的箭头长度/宽度是按标注线宽推的，
		// 在 16px 图标里会撑出绘制区被裁掉。图标用手调的固定几何。
		ip.line(2, 13, 9, 6, 2, c)
		ip.fillTriangle(pt{14, 1}, pt{12, 9}, pt{6, 3}, c)

	case iconLine:
		ip.line(2, 13, 13, 2, 2, c)

	case iconMosaic:
		for gy := 0; gy < 3; gy++ {
			for gx := 0; gx < 3; gx++ {
				ip.fillRect(Rect{X: gx * 6, Y: gy * 6, W: 4, H: 4}, c)
			}
		}

	case iconText:
		// 一个「T」：顶横 + 中竖。
		ip.fillRect(Rect{X: 2, Y: 2, W: 12, H: 2}, c)
		ip.fillRect(Rect{X: 7, Y: 2, W: 2, H: 12}, c)

	case iconFontSize:
		// 一个「A」：两条斜边 + 中横。与 iconText 的 T 同为文字类图标，
		// 靠「有斜边」区分。
		ip.line(8, 3, 3, 13, 2, c)
		ip.line(8, 3, 13, 13, 2, c)
		ip.line(5, 9, 11, 9, 2, c)

	case iconUndo:
		ip.arc(9, 8, 5, false, c)
		ip.fillTriangle(pt{1, 9}, pt{7, 5}, pt{7, 13}, c)

	case iconRedo:
		ip.arc(6, 8, 5, true, c)
		ip.fillTriangle(pt{14, 9}, pt{8, 5}, pt{8, 13}, c)

	case iconCopy:
		ip.strokeRect(Rect{5, 1, 10, 10}, 2, c)
		ip.strokeRect(Rect{1, 5, 10, 10}, 2, c)

	case iconSave:
		ip.fillRect(Rect{X: 7, Y: 1, W: 2, H: 6}, c)
		ip.fillTriangle(pt{8, 12}, pt{3, 6}, pt{13, 6}, c)
		ip.fillRect(Rect{X: 2, Y: 14, W: 12, H: 2}, c)

	case iconPin:
		// 图钉：帽 + 身 + 尖。
		ip.fillRect(Rect{X: 4, Y: 1, W: 8, H: 4}, c)
		ip.fillRect(Rect{X: 7, Y: 4, W: 2, H: 7}, c)
		ip.fillTriangle(pt{7, 11}, pt{9, 11}, pt{8, 15}, c)

	case iconClose:
		ip.line(3, 3, 12, 12, 2, c)
		ip.line(12, 3, 3, 12, 2, c)
	}
}

// arc 画一段上半圆弧，线宽固定 2px（图标专用）。
// mirror 为 false 时弧从左向右扫（撤销的尾巴）；为 true 时左右镜像（重做）。
func (p *painter) arc(cx, cy, r float64, mirror bool, c col) {
	const steps = 28
	px, py := 0, 0
	for i := 0; i <= steps; i++ {
		t := math.Pi * float64(i) / float64(steps)
		dx := r * math.Cos(t)
		if mirror {
			dx = -dx
		}
		x := int(math.Round(cx + dx))
		y := int(math.Round(cy - r*math.Sin(t)))
		if i > 0 {
			p.line(px, py, x, y, 2, c)
		}
		px, py = x, y
	}
}
