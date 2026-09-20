//go:build windows

package screenshot

// 标注图形：只存矢量参数（类型 / 两端点 / 颜色 / 线宽），不存像素。
//
// 这样做的好处是撤销栈的内存占用与操作次数无关：撤销 = 数组末尾弹出，
// 重做 = 反向；每帧从选区原图重放全部图形，任何一步都能被完整重建。
// 马赛克也从原始截图取样而不是取当前画布，所以叠两层不会越糊越失真。

type shapeKind uint8

const (
	shapeRect shapeKind = iota
	shapeEllipse
	shapeArrow
	shapeLine
	shapeMosaic
	shapeText
)

type shape struct {
	kind   shapeKind
	x0, y0 int
	x1, y1 int
	width  int
	color  col
	// text 仅 shapeText 使用，锚点为 (x0, y0) 左上角；fontSize 是文字像素高度，
	// 由工具条的「字号」档位决定（见 annotFontSizes），与线宽无关。
	text     string
	fontSize int
}

const (
	mosaicBlock    = 9 // 马赛克块边长（像素）
	annotMinExtent = 3 // 小于此尺寸的图形视为误触丢弃
)

// colAnnot 是标注默认色（BGR 顺序的 #E53935，Snipaste 默认红）。
var colAnnot = col{0x35, 0x39, 0xE5}

// annotColors 是调色板可选色，顺序即工具条面板中的排列顺序。
var annotColors = []col{
	{0x35, 0x39, 0xE5}, // #E53935 红
	{0x00, 0x8C, 0xFB}, // #FB8C00 橙
	{0x35, 0xD8, 0xFD}, // #FDD835 黄
	{0x47, 0xA0, 0x43}, // #43A047 绿
	{0xD4, 0xBC, 0x00}, // #00BCD4 青
	{0xE5, 0x88, 0x1E}, // #1E88E5 蓝
	{0xAA, 0x24, 0x8E}, // #8E24AA 紫
	{0xFF, 0xFF, 0xFF}, // #FFFFFF 白
	{0x21, 0x21, 0x21}, // #212121 近黑
}

// annotWidths 是线宽档位（像素），第一档最细。
//
// 会话默认取第一档：截图标注绝大多数是「圈一下 / 指一下」，
// 细线不遮内容；要粗线再点一下面板就行，反过来（默认粗线）的纠正成本更高。
var annotWidths = []int{2, 4, 6, 8}

// annotFontSizes 是文字标注可选的字号（像素高度）。
var annotFontSizes = []int{12, 16, 20, 26, 34, 44}

// annotFontSize 是默认字号（像素高度）。
const annotFontSize = 16

// shapeKindOf 把工具映射成图形类型；非绘制工具返回 shapeRect 且调用方不会用它。
func shapeKindOf(t toolID) shapeKind {
	switch t {
	case toolEllipse:
		return shapeEllipse
	case toolArrow:
		return shapeArrow
	case toolLine:
		return shapeLine
	case toolMosaic:
		return shapeMosaic
	case toolText:
		return shapeText
	default:
		return shapeRect
	}
}

// meaningful 报告这次拖拽是否够得上一笔有效图形（挡住误触产生的零点图形）。
func (s *shape) meaningful() bool {
	dx := absInt(s.x1 - s.x0)
	dy := absInt(s.y1 - s.y0)
	switch s.kind {
	case shapeMosaic:
		return dx >= mosaicBlock && dy >= mosaicBlock
	case shapeRect, shapeEllipse:
		return dx >= annotMinExtent && dy >= annotMinExtent
	default: // 线 / 箭头：只要求有长度
		return dx >= annotMinExtent || dy >= annotMinExtent
	}
}

// drawShape 把图形 s 画到 p 上。
func (o *Overlay) drawShape(p *painter, s *shape) {
	switch s.kind {
	case shapeRect:
		r := normRect(s.x0, s.y0, s.x1, s.y1)
		if r.W < annotMinExtent || r.H < annotMinExtent {
			return
		}
		p.strokeRect(r, s.width, s.color)

	case shapeEllipse:
		r := normRect(s.x0, s.y0, s.x1, s.y1)
		if r.W < annotMinExtent || r.H < annotMinExtent {
			return
		}
		p.strokeEllipse(r, s.width, s.color)

	case shapeLine:
		if absInt(s.x1-s.x0) < annotMinExtent && absInt(s.y1-s.y0) < annotMinExtent {
			return
		}
		p.line(s.x0, s.y0, s.x1, s.y1, s.width, s.color)

	case shapeArrow:
		if absInt(s.x1-s.x0) < annotMinExtent && absInt(s.y1-s.y0) < annotMinExtent {
			return
		}
		p.arrow(s.x0, s.y0, s.x1, s.y1, s.width, s.color)

	case shapeMosaic:
		r := normRect(s.x0, s.y0, s.x1, s.y1)
		if r.W < mosaicBlock || r.H < mosaicBlock {
			return
		}
		o.paintMosaic(p, r)

	case shapeText:
		o.drawText(p, s.text, s.x0, s.y0, textHeight(s), s.color)
	}
}

// paintMosaic 在 r 范围内铺马赛克：按 mosaicBlock 网格取原始截图的块均值回填。
//
// 采样源恒为 o.src（抓屏原图）而不是当前画布，因此同一区域叠两层不会越糊越失真；
// 代价是重放时要多算一遍块均值——块数少，可忽略。
func (o *Overlay) paintMosaic(p *painter, r Rect) {
	if o.src == nil {
		return
	}
	r = r.intersect(Rect{X: 0, Y: 0, W: o.bounds.W, H: o.bounds.H})
	if r.Empty() {
		return
	}
	sw := o.bounds.W

	for by := r.Y; by < r.Y+r.H; by += mosaicBlock {
		for bx := r.X; bx < r.X+r.W; bx += mosaicBlock {
			x1 := minInt(bx+mosaicBlock, r.X+r.W)
			y1 := minInt(by+mosaicBlock, r.Y+r.H)

			var sr, sg, sb, n int
			for y := by; y < y1; y++ {
				row := y * sw * 4
				for x := bx; x < x1; x++ {
					off := row + x*4
					sb += int(o.src.Pix[off+0])
					sg += int(o.src.Pix[off+1])
					sr += int(o.src.Pix[off+2])
					n++
				}
			}
			if n == 0 {
				continue
			}
			avg := col{byte(sb / n), byte(sg / n), byte(sr / n)}
			for y := by; y < y1; y++ {
				for x := bx; x < x1; x++ {
					p.px(x, y, avg)
				}
			}
		}
	}
}
