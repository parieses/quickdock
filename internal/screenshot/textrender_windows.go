//go:build windows

package screenshot

import (
	"syscall"
	"unsafe"
)

// 文本标注的绘制。
//
// 为什么不用「直接用 Overlay 的 hMemDC 画」：drawShape 的目标画笔可能是
// 覆盖层整屏 DIB（上屏路径），也可能是裁剪出来的结果位图（导出路径）——
// 后者是一块普通 Go 字节切片，没有对应的 DC。为此这里维护一块**离屏 scratch
// DIB**，绘制流程统一为：
//
//	把目标区域现有像素拷进 scratch → 在 scratch 上用 GDI 画字 → 拷回目标
//
// 由于 scratch 的初始像素就是目标像素，SetBkMode(TRANSPARENT) 下背景能正确
// 透出，两条路径共用同一份代码，不需要各写一遍。
//
// 字体走 pixel 高度（CreateFontW 的 lfHeight 传负值即「字符高度」），
// 与宿主 permonitorv2 DPI 感知一致，不涉及 pt→px 换算。

// textScratch 是复用的离屏 32bpp top-down DIB。
type textScratch struct {
	dc   uintptr
	bmp  uintptr
	old  uintptr
	pix  []byte
	w, h int
}

// scratchAlign 把尺寸向上取整到 64 的倍数，避免连续几次不同尺寸的文本
// 反复重建 DIB（重建 = 删对象 + 建对象，比多占几百 KB 贵）。
const scratchAlign = 64

// ensure 保证 scratch 至少有 w×h 的可用空间。
func (s *textScratch) ensure(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	if s.dc == 0 {
		dc, _, _ := procCreateCompatibleDC.Call(0)
		if dc == 0 {
			return false
		}
		s.dc = dc
	}
	if s.bmp != 0 && s.w >= w && s.h >= h {
		return true
	}

	if s.bmp != 0 {
		procSelectObject.Call(s.dc, s.old)
		procDeleteObject.Call(s.bmp)
		s.bmp, s.old = 0, 0
	}

	gw := ((w + scratchAlign - 1) / scratchAlign) * scratchAlign
	gh := ((h + scratchAlign - 1) / scratchAlign) * scratchAlign
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(gw),
			Height:      -int32(gh), // top-down
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}
	var pixels unsafe.Pointer
	hb, _, _ := procCreateDIBSection.Call(
		s.dc, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&pixels)), 0, 0,
	)
	if hb == 0 || pixels == nil {
		return false
	}
	s.bmp = hb
	s.w, s.h = gw, gh
	s.pix = unsafe.Slice((*byte)(pixels), gw*gh*4)
	s.old, _, _ = procSelectObject.Call(s.dc, hb)
	return true
}

func (s *textScratch) release() {
	if s.bmp != 0 {
		procSelectObject.Call(s.dc, s.old)
		procDeleteObject.Call(s.bmp)
	}
	if s.dc != 0 {
		procDeleteDC.Call(s.dc)
	}
	s.dc, s.bmp, s.old, s.pix, s.w, s.h = 0, 0, 0, nil, 0, 0
}

// textHeightFor 把线宽档位映射成文字像素高度。
//
// 字号已独立成档位（annotFontSizes / Overlay.curFontSize），这里只作为
// shape.fontSize 缺失时的兜底，不再参与正常路径。
func textHeightFor(width int) int {
	if width < 1 {
		width = 1
	}
	return 12 + width*3
}

// fontFor 返回指定像素高度的字体句柄（按高度缓存，避免每帧 CreateFontW）。
func (o *Overlay) fontFor(height int) uintptr {
	if o.fontCache == nil {
		o.fontCache = make(map[int]uintptr, 4)
	}
	if h, ok := o.fontCache[height]; ok {
		return h
	}
	face, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	// DEFAULT_CHARSET 让中英混排都落到雅黑；ANTIALIASED_QUALITY 而非
	// CLEARTYPE_QUALITY——后者依赖不透明背景做次像素抗锯齿，画进带 alpha
	// 通道的 DIB 会在字缘留下彩边。
	h, _, _ := procCreateFontW.Call(
		iptr(-height), 0, 0, 0, 400, 0, 0, 0,
		1 /*DEFAULT_CHARSET*/, 0, 0, 4 /*ANTIALIASED_QUALITY*/, 0,
		uintptr(unsafe.Pointer(face)),
	)
	o.fontCache[height] = h
	return h
}

// measureText 返回文本在指定像素高度下的外接尺寸。
// 用 Overlay 的 hMemDC 度量（只临时选入字体，不动已选中的位图）。
func (o *Overlay) measureText(u16 []uint16, cch, height int) (int, int) {
	if o.hMemDC == 0 || cch <= 0 {
		return 0, 0
	}
	hf := o.fontFor(height)
	if hf == 0 {
		return 0, 0
	}
	prev, _, _ := procSelectObject.Call(o.hMemDC, hf)
	var sz sizeStruct
	procGetTextExtentPoint32W.Call(
		o.hMemDC,
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(cch),
		uintptr(unsafe.Pointer(&sz)),
	)
	procSelectObject.Call(o.hMemDC, prev)
	return int(sz.CX), int(sz.CY)
}

// textSize 返回一段文本在指定像素高度下的外接尺寸（覆盖层客户区像素）。
func (o *Overlay) textSize(txt string, height int) (int, int) {
	u16, err := syscall.UTF16FromString(txt)
	if err != nil || len(u16) <= 1 {
		return 0, 0
	}
	return o.measureText(u16, len(u16)-1, height)
}

// drawText 把 txt 以左上角对齐画到画笔 p 的客户区坐标 (x, y) 处。
func (o *Overlay) drawText(p *painter, txt string, x, y, height int, c col) {
	if txt == "" || height <= 0 {
		return
	}
	u16, err := syscall.UTF16FromString(txt)
	if err != nil || len(u16) <= 1 {
		return
	}
	cch := len(u16) - 1

	tw, th := o.measureText(u16, cch, height)
	if tw <= 0 || th <= 0 {
		return
	}
	hf := o.fontFor(height)
	if hf == 0 {
		return
	}

	// 客户区坐标系下的文本矩形，先被画笔的裁剪区削一次（标注不许画出选区）。
	box := Rect{X: x, Y: y, W: tw, H: th}
	if !p.clip.Empty() {
		box = box.intersect(p.clip)
	}
	if box.Empty() {
		return
	}

	// 再削一次目标位图边界。bx/by 是文本左上角在位图坐标系下的位置。
	bx, by := x+p.dx, y+p.dy
	x0 := maxInt(box.X+p.dx, 0)
	y0 := maxInt(box.Y+p.dy, 0)
	x1 := minInt(box.X+box.W+p.dx, p.w)
	y1 := minInt(box.Y+box.H+p.dy, p.h)
	w, h := x1-x0, y1-y0
	if w <= 0 || h <= 0 {
		return
	}
	if !o.scratch.ensure(w, h) {
		return
	}

	// 目标像素 → scratch（保证背景透出）
	stride := o.scratch.w * 4
	for yy := 0; yy < h; yy++ {
		src := ((y0+yy)*p.w + x0) * 4
		dst := yy * stride
		copy(o.scratch.pix[dst:dst+w*4], p.pix[src:src+w*4])
	}

	// GDI 画字。偏移 (x0-bx, y0-by) 即被裁掉的那部分（左/上越界量）。
	prevFont, _, _ := procSelectObject.Call(o.scratch.dc, hf)
	procSetBkMode.Call(o.scratch.dc, dtTransparent)
	procSetTextColor.Call(o.scratch.dc, colorRef(c))
	procTextOutW.Call(
		o.scratch.dc,
		iptr(x0-bx), iptr(y0-by),
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(cch),
	)
	procSelectObject.Call(o.scratch.dc, prevFont)

	// scratch → 目标
	for yy := 0; yy < h; yy++ {
		src := yy * stride
		dst := ((y0+yy)*p.w + x0) * 4
		copy(p.pix[dst:dst+w*4], o.scratch.pix[src:src+w*4])
	}

	// GDI 会往 alpha 字节写不确定值（常见是 0）。覆盖层用
	// UpdateLayeredWindow(ULW_ALPHA) 提交，alpha=0 会让那块变成透明洞。
	for yy := y0; yy < y1; yy++ {
		off := (yy*p.w+x0)<<2 | 3
		for xx := x0; xx < x1; xx++ {
			p.pix[off] = 0xFF
			off += 4
		}
	}
}

// drawTextVCentered 在 box 内垂直居中画一行文本，左侧对齐 box.X。
// 供工具条的「字号」面板把每档字号标成「A 12」这样的行标签。
func (o *Overlay) drawTextVCentered(p *painter, txt string, box Rect, height int, c col) {
	u16, err := syscall.UTF16FromString(txt)
	if err != nil || len(u16) <= 1 {
		return
	}
	_, th := o.measureText(u16, len(u16)-1, height)
	o.drawText(p, txt, box.X, box.Y+maxInt(0, (box.H-th)/2), height, c)
}

// textHeight 返回文字标注实际使用的像素高度。
// 容错：字号是后来才从线宽档位里独立出来的，历史数据没有 fontSize 字段。
func textHeight(s *shape) int {
	if s.fontSize > 0 {
		return s.fontSize
	}
	return textHeightFor(s.width)
}

// textBounds 返回文字标注在覆盖层客户区坐标下的外接矩形。
// 与 drawText 走同一个 measureText，所以判定范围与实际落笔范围一致。
func (o *Overlay) textBounds(s *shape) Rect {
	u16, err := syscall.UTF16FromString(s.text)
	if err != nil || len(u16) <= 1 {
		return Rect{}
	}
	tw, th := o.measureText(u16, len(u16)-1, textHeight(s))
	if tw <= 0 || th <= 0 {
		return Rect{}
	}
	return Rect{X: s.x0, Y: s.y0, W: tw, H: th}
}

// hitTextAt 返回点 (x, y) 命中的文字标注下标；从后往前找（后画的盖在上面）。
//
// 判定范围比字面外扩 hitTextPad：文字的笔画很细，要求精确点在笔画上会很难点中，
// 而想改文字的人本来就知道自己点的是哪一条。
func (o *Overlay) hitTextAt(x, y int) int {
	const hitTextPad = 3
	for i := len(o.shapes) - 1; i >= 0; i-- {
		if o.shapes[i].kind != shapeText {
			continue
		}
		r := o.textBounds(&o.shapes[i])
		if r.Empty() {
			continue
		}
		r = Rect{X: r.X - hitTextPad, Y: r.Y - hitTextPad, W: r.W + hitTextPad*2, H: r.H + hitTextPad*2}
		if r.Contains(x, y) {
			return i
		}
	}
	return -1
}
