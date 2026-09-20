//go:build windows

package screenshot

import "math"

// 本文件是像素级绘制原语，供工具条与标注图形共用。
//
// 为什么不用 GDI（Rectangle / Ellipse / Polygon / CreatePen / RoundRect）：
// 覆盖层用 UpdateLayeredWindow(ULW_ALPHA) 提交整帧，要求每个像素的 alpha 通道
// 正确。而 GDI 绘制 32bpp DIB 时对 alpha 字节的写入没有保证——多数函数保留原值、
// 部分函数写 0，一旦被写 0，那块区域在合成时会整片变成透明。纯字节写入彻底绕开
// 这层不确定性，也顺带免掉 GDI 对象的创建与句柄泄漏风险。
// 代价是线条没有抗锯齿，在 30px 按钮图标与 2~4px 描边的尺度下可以接受。

// col 是 BGR 三字节。覆盖层整屏不透明（暗化是预先合成进 RGB 的），
// 因此绘制原语只关心这三个字节，alpha 一律写 0xFF。
type col [3]byte

// pt 是整数点。
type pt struct{ x, y int }

// painter 把绘制操作绑定到一张 BGRA 底图的一段区域上。
//
// 坐标约定：所有绘制函数的入参都是「覆盖层客户区坐标」，painter 内部用
// (dx, dy) 平移到目标位图坐标——画进全屏 DIB 时 dx=dy=0；画进裁剪结果位图时
// dx=-选区.X、dy=-选区.Y。同一份图形绘制代码因此既能上屏也能导出。
type painter struct {
	pix    []byte
	w, h   int  // 目标位图尺寸
	dx, dy int  // 目标坐标 = 客户区坐标 + (dx, dy)
	clip   Rect // 客户区坐标系下的裁剪区；Empty 表示不裁剪
}

// ===== 基础像素操作 =====

func (p *painter) px(x, y int, c col) {
	if !p.clip.Empty() && !p.clip.Contains(x, y) {
		return
	}
	tx, ty := x+p.dx, y+p.dy
	if tx < 0 || ty < 0 || tx >= p.w || ty >= p.h {
		return
	}
	off := (ty*p.w + tx) << 2
	p.pix[off+0] = c[0]
	p.pix[off+1] = c[1]
	p.pix[off+2] = c[2]
	p.pix[off+3] = 0xFF
}

func (p *painter) hline(x0, x1, y int, c col) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	for x := x0; x <= x1; x++ {
		p.px(x, y, c)
	}
}

func (p *painter) vline(x, y0, y1 int, c col) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		p.px(x, y, c)
	}
}

func (p *painter) fillRect(r Rect, c col) {
	if r.Empty() {
		return
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			p.px(x, y, c)
		}
	}
}

// strokeRect 画矩形描边，线宽 thick，线条全部落在 r 内部。
func (p *painter) strokeRect(r Rect, thick int, c col) {
	if r.Empty() || thick <= 0 {
		return
	}
	if thick*2 > r.W {
		thick = r.W / 2
	}
	if thick*2 > r.H {
		thick = r.H / 2
	}
	if thick <= 0 {
		return
	}
	for t := 0; t < thick; t++ {
		p.hline(r.X, r.X+r.W-1, r.Y+t, c)
		p.hline(r.X, r.X+r.W-1, r.Y+r.H-1-t, c)
		p.vline(r.X+t, r.Y+t, r.Y+r.H-1-t, c)
		p.vline(r.X+r.W-1-t, r.Y+t, r.Y+r.H-1-t, c)
	}
}

// ===== 圆角矩形 =====

// dashedRect 用等长虚实段画矩形边框（1px），用于「这个图形被选中了」的提示。
//
// 用虚线而非实线：选中框会紧贴图形自己的线条，实线两者会糊成一片，
// 看起来像图形变粗了而不是被选中。
func (p *painter) dashedRect(r Rect, c col) {
	if r.W <= 1 || r.H <= 1 {
		return
	}
	const on, period = 3, 6
	x1 := r.X + r.W - 1
	y1 := r.Y + r.H - 1
	for x := 0; x < r.W; x++ {
		if x%period >= on {
			continue
		}
		p.px(r.X+x, r.Y, c)
		p.px(r.X+x, y1, c)
	}
	for y := 0; y < r.H; y++ {
		if y%period >= on {
			continue
		}
		p.px(r.X, r.Y+y, c)
		p.px(x1, r.Y+y, c)
	}
}

// roundInset 返回圆角矩形第 y 行相对左上角的内缩量。
func roundInset(y, h, radius int) int {
	if radius <= 0 || y < 0 || y >= h {
		return 0
	}
	dy := 0
	switch {
	case y < radius:
		dy = radius - 1 - y
	case y >= h-radius:
		dy = y - (h - radius)
	}
	if dy <= 0 {
		return 0
	}
	d := radius*radius - dy*dy
	if d <= 0 {
		return radius
	}
	return radius - int(math.Sqrt(float64(d)))
}

func (p *painter) fillRoundRect(r Rect, radius int, c col) {
	if r.Empty() {
		return
	}
	if radius*2 > r.W {
		radius = r.W / 2
	}
	if radius*2 > r.H {
		radius = r.H / 2
	}
	for y := 0; y < r.H; y++ {
		inset := roundInset(y, r.H, radius)
		p.hline(r.X+inset, r.X+r.W-1-inset, r.Y+y, c)
	}
}

func (p *painter) strokeRoundRect(r Rect, radius, thick int, c col) {
	if r.Empty() || thick <= 0 {
		return
	}
	if radius*2 > r.W {
		radius = r.W / 2
	}
	if radius*2 > r.H {
		radius = r.H / 2
	}
	for y := 0; y < r.H; y++ {
		inset := roundInset(y, r.H, radius)
		switch {
		case y < thick || y >= r.H-thick:
			p.hline(r.X+inset, r.X+r.W-1-inset, r.Y+y, c)
		default:
			p.hline(r.X+inset, r.X+inset+thick-1, r.Y+y, c)
			p.hline(r.X+r.W-inset-thick, r.X+r.W-1-inset, r.Y+y, c)
		}
	}
}

// ===== 线 / 椭圆 / 三角 =====

// fillCircle 画实心圆。用于工具条上的「当前颜色」色块。
func (p *painter) fillCircle(cx, cy, r int, c col) {
	if r <= 0 {
		return
	}
	for dy := -r; dy <= r; dy++ {
		dx := int(math.Sqrt(float64(r*r - dy*dy)))
		p.hline(cx-dx, cx+dx, cy+dy, c)
	}
}

// ring 画圆环：先填满外圈色，再挖掉内圈填内部色。
func (p *painter) ring(cx, cy, r, thick int, outer, inner col) {
	if r <= 0 || thick <= 0 {
		return
	}
	p.fillCircle(cx, cy, r, outer)
	if in := r - thick; in > 0 {
		p.fillCircle(cx, cy, in, inner)
	} else {
		p.fillCircle(cx, cy, r, inner)
	}
}

// line 用 Bresenham 画线，thick 为线宽（方形笔刷，以线上点为左上角原点居中）。
func (p *painter) line(x0, y0, x1, y1, thick int, c col) {
	if thick < 1 {
		thick = 1
	}
	half := (thick - 1) / 2
	plot := func(x, y int) {
		for dy := 0; dy < thick; dy++ {
			for dx := 0; dx < thick; dx++ {
				p.px(x+dx-half, y+dy-half, c)
			}
		}
	}

	dx := absInt(x1 - x0)
	dy := -absInt(y1 - y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		plot(x0, y0)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// strokeEllipse 画空心椭圆。r 是椭圆的外接矩形。
//
// 用参数方程采样而非中点算法：标注场景里椭圆数量少、尺寸小，采样实现短且不会
// 在长轴/短轴切换处出错；采样步长按周长估计，保证每段不超过 1px。
func (p *painter) strokeEllipse(r Rect, thick int, c col) {
	if r.Empty() {
		return
	}
	cx := float64(r.X) + float64(r.W-1)/2
	cy := float64(r.Y) + float64(r.H-1)/2
	rx := float64(r.W-1) / 2
	ry := float64(r.H-1) / 2
	if rx < 1 || ry < 1 {
		p.fillRect(r, c)
		return
	}

	steps := int(2 * math.Pi * math.Max(rx, ry))
	if steps < 32 {
		steps = 32
	}
	if steps > 2048 {
		steps = 2048
	}

	px, py := 0, 0
	for i := 0; i <= steps; i++ {
		t := 2 * math.Pi * float64(i) / float64(steps)
		x := int(math.Round(cx + rx*math.Cos(t)))
		y := int(math.Round(cy + ry*math.Sin(t)))
		if i > 0 {
			p.line(px, py, x, y, thick, c)
		}
		px, py = x, y
	}
}

// fillTriangle 用边函数填充实心三角形。
func (p *painter) fillTriangle(a, b, c pt, cr col) {
	minX := minInt(a.x, minInt(b.x, c.x))
	maxX := maxInt(a.x, maxInt(b.x, c.x))
	minY := minInt(a.y, minInt(b.y, c.y))
	maxY := maxInt(a.y, maxInt(b.y, c.y))
	if edgeFn(a, b, c) == 0 {
		return
	}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			q := pt{x, y}
			w0 := edgeFn(b, c, q)
			w1 := edgeFn(c, a, q)
			w2 := edgeFn(a, b, q)
			if (w0 >= 0 && w1 >= 0 && w2 >= 0) || (w0 <= 0 && w1 <= 0 && w2 <= 0) {
				p.px(x, y, cr)
			}
		}
	}
}

func edgeFn(a, b, c pt) int {
	return (b.x-a.x)*(c.y-a.y) - (b.y-a.y)*(c.x-a.x)
}

// arrow 画带箭头的直线：主线在箭头根部收住，头部是实心三角。
func (p *painter) arrow(x0, y0, x1, y1, thick int, c col) {
	if thick < 1 {
		thick = 1
	}
	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	d := math.Hypot(dx, dy)
	if d < 1 {
		return
	}
	ux, uy := dx/d, dy/d

	head := float64(thick*4 + 7) // 箭头长度
	half := float64(thick*2 + 4) // 箭头半宽
	if head > d {
		head = d
		half = math.Max(half*(head/d), 2)
	}

	bx := float64(x1) - ux*head
	by := float64(y1) - uy*head
	// 与主线垂直的方向
	px, py := -uy, ux

	// 主线画到箭头根部稍前一点，避免线头从三角底边戳出来。
	p.line(x0, y0, int(math.Round(bx)), int(math.Round(by)), thick, c)
	p.fillTriangle(
		pt{x1, y1},
		pt{int(math.Round(bx + px*half)), int(math.Round(by + py*half))},
		pt{int(math.Round(bx - px*half)), int(math.Round(by - py*half))},
		c,
	)
}

// boost 把 (x, y) 处像素整体提亮 d（用于十字对齐辅助线），保留原色相。
func (p *painter) boost(x, y, d int) {
	if !p.clip.Empty() && !p.clip.Contains(x, y) {
		return
	}
	tx, ty := x+p.dx, y+p.dy
	if tx < 0 || ty < 0 || tx >= p.w || ty >= p.h {
		return
	}
	off := (ty*p.w + tx) << 2
	for i := 0; i < 3; i++ {
		v := int(p.pix[off+i]) + d
		if v > 255 {
			v = 255
		}
		p.pix[off+i] = byte(v)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// normRect 用两个对角点构造规范化矩形（保证 W/H 非负）。
func normRect(x0, y0, x1, y1 int) Rect {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}
