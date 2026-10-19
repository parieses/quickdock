package screenshot

import "testing"

// Close 是 captureRect 接管 DIB 内存后的核心风险点：漏调 = 一次 33MB（4K）泄漏，
// 多调 = 野指针。这里把两条性质固定住。
func TestBitmapClose(t *testing.T) {
	t.Run("普通 Bitmap 是空操作", func(t *testing.T) {
		b := &Bitmap{W: 1, H: 1, Pix: make([]byte, 4)}
		b.Close()
		if b.Pix == nil {
			t.Fatal("没有外部内存的 Bitmap 不该被 Close 清空 Pix")
		}
	})

	t.Run("release 只执行一次", func(t *testing.T) {
		n := 0
		b := &Bitmap{W: 1, H: 1, Pix: make([]byte, 4), release: func() { n++ }}
		b.Close()
		b.Close()
		if n != 1 {
			t.Fatalf("release 执行了 %d 次，期望 1 次", n)
		}
		if b.Pix != nil {
			t.Fatalf("Close 后 Pix 应置 nil，避免悬空指针")
		}
	})

	t.Run("nil 接收者不 panic", func(t *testing.T) {
		var b *Bitmap
		b.Close() // 调用方在错误路径上直接 Close(可能为 nil 的 src)，必须安全
	})
}
