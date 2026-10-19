package screenshot

import (
	"testing"
	"time"
)

// TestDimHalf 校验「按 50% 亮度写入底图」的两条不变量：
//   - 每个颜色通道独立减半（不能出现绿色最低位溢进蓝色最高位）；
//   - alpha 一律写成不透明，否则 UpdateLayeredWindow 会留下透明洞。
func TestDimHalf(t *testing.T) {
	src := []byte{
		0x00, 0x01, 0xFF, 0x00, // B=0   G=1   R=255 A=0
		0xFF, 0xFF, 0xFF, 0xFF, // B=255 G=255 R=255 A=255
	}
	dst := make([]byte, len(src))

	dimHalf(src, dst)

	want := []byte{
		0x00, 0x00, 0x7F, 0xFF,
		0x7F, 0x7F, 0x7F, 0xFF,
	}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("dst[%d] = %#02x, want %#02x（整个 dst=% x）", i, dst[i], want[i], dst)
		}
	}
	// 源数据必须原样保留：选区还原要用它，被就地改写会让选区内也跟着变暗。
	if src[2] != 0xFF || src[3] != 0x00 {
		t.Fatalf("dimHalf 改写了源像素：% x", src)
	}
}

// TestDimHalfOddLen 覆盖长度不是 4 的倍数（不可能来自抓屏，但不应越界崩溃）。
func TestDimHalfOddLen(t *testing.T) {
	src := []byte{0x10, 0x20, 0x30}
	dst := make([]byte, len(src))
	dimHalf(src, dst) // 只走退化路径，不 panic 即可
}

// TestDimHalfInto 保证双写版本与单写版本逐字节一致——它是首帧省掉整屏拷贝的
// 依据，两个目标只要有一个写歪了，屏幕上就会有一半暗一半亮。
func TestDimHalfInto(t *testing.T) {
	src := make([]byte, 4*257) // 257 个像素：奇数长度，顺带压住 word 视图的退化分支
	for i := range src {
		src[i] = byte(i * 7)
	}
	want := make([]byte, len(src))
	dimHalf(src, want)

	a := make([]byte, len(src))
	b := make([]byte, len(src))
	dimHalfInto(src, a, b)

	for i := range want {
		if a[i] != want[i] || b[i] != want[i] {
			t.Fatalf("下标 %d：a=%#02x b=%#02x want=%#02x", i, a[i], b[i], want[i])
		}
	}
}

// TestDimHalfIntoShortDst 目标缓冲偏短时不能越界（正常路径靠尺寸校验保证一致，
// 这里锁住退化行为，避免以后改逻辑时踩到）。
func TestDimHalfIntoShortDst(t *testing.T) {
	src := make([]byte, 64)
	a := make([]byte, 32)
	b := make([]byte, 16)
	dimHalfInto(src, a, b) // 不 panic 即可
}

// TestEnsureOpaque 校验抓屏后补 alpha 的行为。
func TestEnsureOpaque(t *testing.T) {
	b := &Bitmap{W: 2, H: 1, Pix: []byte{1, 2, 3, 0, 4, 5, 6, 7}}
	b.ensureOpaque()

	if got := []byte{b.Pix[3], b.Pix[7]}; got[0] != 0xFF || got[1] != 0xFF {
		t.Fatalf("alpha = %v, want [255 255]", got)
	}
	if b.Pix[0] != 1 || b.Pix[6] != 6 {
		t.Fatalf("颜色通道被改写：% x", b.Pix)
	}
}

// BenchmarkDimHalf1080p 是「按 F1 后预合成暗化」这一段的本体。
//
// 改造前是「整帧 copy 1.8ms + 逐字节循环 5.3ms」；现在是一趟 32 位字循环。
func BenchmarkDimHalf1080p(b *testing.B) {
	n := 1920 * 1080 * 4
	src := make([]byte, n)
	dst := make([]byte, n)
	b.SetBytes(int64(n))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dimHalf(src, dst)
	}
}

// TestFrameWait 锁定合帧预算：会话首帧不等、预算内排队、超预算立刻画。
func TestFrameWait(t *testing.T) {
	now := time.Now()

	if got := frameWait(time.Time{}, now); got != 0 {
		t.Fatalf("首帧 frameWait = %v, want 0", got)
	}
	if got := frameWait(now, now); got != frameBudget {
		t.Fatalf("刚画完 frameWait = %v, want %v", got, frameBudget)
	}
	if got := frameWait(now.Add(-2*time.Millisecond), now); got != frameBudget-2*time.Millisecond {
		t.Fatalf("预算内 frameWait = %v, want %v", got, frameBudget-2*time.Millisecond)
	}
	if got := frameWait(now.Add(-20*time.Millisecond), now); got != 0 {
		t.Fatalf("超预算 frameWait = %v, want 0", got)
	}
	// 时间倒流（系统时间被改）时不能返回负数，否则会被 SetTimer 当成「尽快」。
	if got := frameWait(now.Add(time.Hour), now); got != frameBudget {
		t.Fatalf("时钟回拨 frameWait = %v, want %v", got, frameBudget)
	}
}
