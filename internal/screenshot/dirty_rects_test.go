package screenshot

import "testing"

// TestUnionRects 覆盖脏区列表的三条不变量：
//   - 空矩形不进列表；
//   - 被包住的矩形被丢掉（省一次提交）；
//   - 只是相交、谁也没包住谁的，两块都留（宁可多提交也不能漏）。
func TestUnionRects(t *testing.T) {
	last := []Rect{{X: 10, Y: 10, W: 100, H: 100}}
	cur := []Rect{
		{X: 50, Y: 50, W: 10, H: 10},   // 完全在 last 里 → 丢弃
		{X: 200, Y: 10, W: 10, H: 10},  // 不相干 → 保留
		{X: 90, Y: 90, W: 100, H: 100}, // 与 last 相交但互不包含 → 保留
		{},                             // 空 → 忽略
	}

	buf := make([]Rect, 0, 8)
	got := unionRects(buf, last, cur)

	want := []Rect{
		{X: 10, Y: 10, W: 100, H: 100},
		{X: 200, Y: 10, W: 10, H: 10},
		{X: 90, Y: 90, W: 100, H: 100},
	}
	if len(got) != len(want) {
		t.Fatalf("unionRects 得到 %d 块 %v，want %d 块", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 块 = %+v, want %+v（全部：%v）", i, got[i], want[i], got)
		}
	}
}

// TestUnionRectsAbsorb 校验「后到的矩形把先前被它包住的矩形挤掉」。
func TestUnionRectsAbsorb(t *testing.T) {
	buf := make([]Rect, 0, 4)
	got := unionRects(buf,
		[]Rect{{X: 10, Y: 10, W: 20, H: 20}},
		[]Rect{{X: 0, Y: 0, W: 100, H: 100}},
	)
	if len(got) != 1 || got[0] != (Rect{X: 0, Y: 0, W: 100, H: 100}) {
		t.Fatalf("unionRects = %v, want 只剩外层那一块", got)
	}
}

// TestUnionRectsReuse 校验复用底层数组不会把结果写乱（就地过滤最容易踩的坑）。
func TestUnionRectsReuse(t *testing.T) {
	buf := make([]Rect, 0, 8)
	buf = unionRects(buf, []Rect{{X: 0, Y: 0, W: 10, H: 10}, {X: 20, Y: 20, W: 10, H: 10}})
	got := unionRects(buf, []Rect{{X: 0, Y: 0, W: 30, H: 30}})
	if len(got) != 1 || got[0] != (Rect{X: 0, Y: 0, W: 30, H: 30}) {
		t.Fatalf("第二轮 unionRects = %v, want 只剩并集外框", got)
	}
}

func TestRectsArea(t *testing.T) {
	if got := rectsArea(nil); got != 0 {
		t.Fatalf("空列表面积 = %d, want 0", got)
	}
	got := rectsArea([]Rect{{W: 100, H: 50}, {W: 10, H: 10}, {}})
	if got != 5100 {
		t.Fatalf("面积 = %d, want 5100", got)
	}
}

// TestCoalesceRects 覆盖合并策略的两面：
//   - 该合的（新旧选框大面积重叠、选框与紧贴它的尺寸提示）合成一块，省一次提交；
//   - 不该合的（贯穿全屏的辅助线与小选框、隔着半屏的两块）必须保持独立，
//     否则会把整屏都算成脏区，比不合还慢。
func TestCoalesceRects(t *testing.T) {
	// 旧选框 1000×600，新选框向右下各长 20px：包围盒几乎等于新选框，应合并。
	oldSel := Rect{X: 0, Y: 0, W: 1000, H: 600}
	newSel := Rect{X: 0, Y: 0, W: 1020, H: 620}
	got := coalesceRects([]Rect{oldSel, newSel}, coalesceWastePercent)
	if len(got) != 1 || got[0] != (Rect{X: 0, Y: 0, W: 1020, H: 620}) {
		t.Fatalf("新旧选框应合成一块，得到 %v", got)
	}

	// 尺寸提示紧贴选框上方，合并只多搬一小条。
	hint := Rect{X: 0, Y: -22, W: 120, H: 20}
	got = coalesceRects([]Rect{newSel, hint}, coalesceWastePercent)
	if len(got) != 1 {
		t.Fatalf("选框与紧贴的提示应合并，得到 %v", got)
	}

	// 贯穿全屏的细条 + 小选框：包围盒会把整屏算进去，绝不能合。
	strip := Rect{X: 0, Y: 700, W: 1920, H: 3}
	small := Rect{X: 100, Y: 100, W: 300, H: 200}
	got = coalesceRects([]Rect{strip, small}, coalesceWastePercent)
	if len(got) != 2 {
		t.Fatalf("辅助线不应与小选框合并，得到 %v", got)
	}

	// 两条平行的辅助线（上一帧 / 这一帧）：合并只多搬几 KB，应合成一条。
	h1 := Rect{X: 0, Y: 700, W: 1920, H: 3}
	h2 := Rect{X: 0, Y: 703, W: 1920, H: 3}
	got = coalesceRects([]Rect{h1, h2}, coalesceWastePercent)
	if len(got) != 1 || got[0] != (Rect{X: 0, Y: 700, W: 1920, H: 6}) {
		t.Fatalf("平行辅助线应合并成一条，得到 %v", got)
	}
}

// TestCoalesceRectsSkipsEmpty 保证空矩形不会被当成「可以合并的伙伴」——
// 包围盒算出来是别人的尺寸，逻辑上不会错，但每次合并都白扫一遍。
func TestCoalesceRectsSkipsEmpty(t *testing.T) {
	real := Rect{X: 10, Y: 10, W: 100, H: 100}
	got := coalesceRects([]Rect{{}, real, {}}, coalesceWastePercent)
	if len(got) != 3 {
		t.Fatalf("空矩形不参与合并，应原样保留（由 unionRects 负责剔除），得到 %v", got)
	}
	if got[1] != real {
		t.Fatalf("真实矩形被改写了：%+v", got[1])
	}
}

// TestPathEstimate 锁定「整帧还是逐块」的判据方向：
// 脏区很小时必须选逐块，脏区接近全屏时必须选整帧。
func TestPathEstimate(t *testing.T) {
	const screenPx = 1920 * 1080
	full := estimateFullMs(screenPx)

	if part := estimatePartialMs(300*200, 2); part >= full {
		t.Fatalf("小选区应选逐块：逐块 %.2fms，整帧 %.2fms", part, full)
	}
	// 4 块几乎铺满全屏：逐块要多付 4 次调用开销 + 更多搬运，应选整帧。
	if part := estimatePartialMs(int(float64(screenPx)*0.95), 4); part <= full {
		t.Fatalf("全屏级脏区应选整帧：逐块 %.2fms，整帧 %.2fms", part, full)
	}
	// 同样是 70% 面积，合成 2 块比散成 8 块便宜——这是合并脏矩形的存在意义。
	a := estimatePartialMs(screenPx*7/10, 2)
	b := estimatePartialMs(screenPx*7/10, 8)
	if a >= b {
		t.Fatalf("块数少的应该更便宜：2 块 %.2fms，8 块 %.2fms", a, b)
	}
}

// TestCovers 是脏区裁剪的基础判断，方向搞反会让该丢的不丢、该留的被丢。
func TestCovers(t *testing.T) {
	outer := Rect{X: 0, Y: 0, W: 100, H: 100}
	if !covers(outer, Rect{X: 10, Y: 10, W: 10, H: 10}) {
		t.Fatal("外层应包住内层")
	}
	if covers(Rect{X: 10, Y: 10, W: 10, H: 10}, outer) {
		t.Fatal("内层不应包住外层")
	}
	// 边界相接：贴边但没越界，算包住。
	if !covers(outer, Rect{X: 90, Y: 90, W: 10, H: 10}) {
		t.Fatal("贴边应算包住")
	}
	if covers(outer, Rect{X: 90, Y: 90, W: 11, H: 10}) {
		t.Fatal("越界 1px 不应算包住")
	}
}
