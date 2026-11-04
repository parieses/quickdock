//go:build windows

package screenshot

import "os"

// 选区静态图层：把「选区原图 + 已提交的标注图形」预先合成成一张与选区同尺寸的
// 位图，逐帧只把脏区从它贴回 DIB。
//
// 原来的 redraw 每帧都要做两件随选区面积线性放大的事：
//   - restoreRegion(sel)：把整个选区从抓屏原图回填一遍（全屏选区时就是 33MB）；
//   - 重放 o.shapes 全部图形，其中马赛克每帧重新算块均值、文字每帧走一遍 GDI。
// 加上 footprint 把整个选区登记成「本帧可能被写到的区域」，脏矩形恒等于选区，
// 大选区必然判 full → 又一次整屏 copy + 整屏提交。三笔钱都是每帧付。
//
// 图层化之后：
//   - 静态内容只在 shapes / sel / 编辑态变化时重建一次；
//   - 每帧还原 = 脏区从 base 拷一次 + 脏区∩选区从 layer 拷一次；
//   - 每帧只重画 draft 和框线、工具条、取色卡这些真正会动的东西；
//   - footprint 只需登记选区的 4 条边框窄带，脏区不再跟着选区面积涨。
//
// 正确性靠一条不变式兜住：**图层一旦变化，本帧强制整帧重绘**。因为静态内容变化时
// 「旧版本留在屏上的哪些像素需要擦掉」没法靠当前帧的落笔矩形安全枚举，整帧重画
// 是唯一不需要额外维护旧版脏区的做法。落笔/撤销/拖动图形是低频操作，为它付一帧
// 整屏是划算的；高频的纯鼠标移动、取色卡跟随都走在局部路径上。
//
// QD_SHOT_NOLAYER=1 可退回原来的「每帧全量重放」路径，用于对比或排查残影。

var noLayerPaint = os.Getenv("QD_SHOT_NOLAYER") == "1"

func layerPaintEnabled() bool { return !noLayerPaint }

// shapesEqual 比较两份图形表。shape 的字段全是可比较类型（含 string 与 col 数组），
// 直接用 == 即可；不引入 reflect，也不必在每个改图形的地方手动置脏标记——漏一处
// 就会画出旧图，深比较是最稳的判据，图形通常只有几十条。
func shapesEqual(a, b []shape) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ensureLayer 保证图层与当前静态状态一致，返回「本帧是否重建了图层」。
// 调用方必须把返回值并入 full 判定，否则会留下上一版图形的残影。
func (o *Overlay) ensureLayer() bool {
	o.layerChanged = false
	if noLayerPaint {
		return false
	}
	editing := o.editingIndex()
	if o.sel.Empty() {
		// 从「有图层」变成「没有选区」也要整帧：上一版图层还印在屏上。
		had := o.layerValid
		o.layerValid = false
		o.layerSel = Rect{}
		o.layerShapes = nil
		o.layerEditing = editing
		return had
	}
	if o.layerValid && o.layerSel == o.sel && o.layerEditing == editing &&
		shapesEqual(o.layerShapes, o.shapes) {
		return false
	}
	o.buildLayer(editing)
	o.layerChanged = true
	return true
}

// buildLayer 合成选区静态内容。坐标约定与 renderSelection 完全一致：
// painter 用 dx/dy 把客户区坐标平移进这张裁剪位图，clip 限制在选区内，
// 因此上屏与导出共用同一份 drawShape，不需要第二套绘制代码。
func (o *Overlay) buildLayer(editing int) {
	sel := o.sel
	if o.src == nil {
		o.layerValid = false
		return
	}
	n := sel.W * sel.H * 4
	if len(o.layer) != n {
		o.layer = make([]byte, n)
	}
	rowBytes := sel.W * 4
	for y := 0; y < sel.H; y++ {
		srcOff := ((sel.Y+y)*o.bounds.W + sel.X) * 4
		copy(o.layer[y*rowBytes:(y+1)*rowBytes], o.src.Pix[srcOff:srcOff+rowBytes])
	}
	p := &painter{pix: o.layer, w: sel.W, h: sel.H, dx: -sel.X, dy: -sel.Y, clip: sel}
	for i := range o.shapes {
		if i == editing {
			continue // 正在重新编辑的文字由 drawTextPreview 实时画，避免重影
		}
		o.drawShape(p, &o.shapes[i])
	}
	o.layerSel = sel
	o.layerShapes = o.cloneShapes()
	o.layerEditing = editing
	o.layerValid = true
}

// blitLayer 把 r 与客户区选区的交集从图层贴回整屏 DIB。
// 调用方应先 restoreRect(r) 把 r 还原成暗化底图，再由这里覆盖选区内部。
func (o *Overlay) blitLayer(r Rect) {
	if !o.layerValid || len(o.layer) == 0 || len(o.dibPix) == 0 {
		return
	}
	r = r.intersect(o.layerSel).intersect(Rect{X: 0, Y: 0, W: o.bounds.W, H: o.bounds.H})
	if r.Empty() {
		return
	}
	rowBytes := r.W * 4
	for y := 0; y < r.H; y++ {
		ly := r.Y + y - o.layerSel.Y
		lx := r.X - o.layerSel.X
		src := (ly*o.layerSel.W + lx) * 4
		dst := ((r.Y+y)*o.bounds.W + r.X) * 4
		copy(o.dibPix[dst:dst+rowBytes], o.layer[src:src+rowBytes])
	}
}

// dropLayer 丢掉图层与其指纹（保留缓冲区交给下次 buildLayer 复用）。
func (o *Overlay) dropLayer() {
	o.layerValid = false
	o.layerSel = Rect{}
	o.layerShapes = nil
	o.layerEditing = -1
	o.layerChanged = false
}
