package sshx

import "testing"

func TestRingKeepsLatest(t *testing.T) {
	r := NewRingBuffer(10)
	if _, err := r.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	if got := string(r.Bytes()); got != "6789ABCDEF" {
		t.Fatalf("应保留最后 10 字节，实际 %q", got)
	}
	if !r.Truncated() {
		t.Fatal("丢过数据必须标记截断，否则前端会以为输出就这么短")
	}
	if r.Len() != 10 {
		t.Fatalf("长度应为 10，实际 %d", r.Len())
	}
}

func TestRingAccumulatesWithoutDrop(t *testing.T) {
	r := NewRingBuffer(16)
	r.Write([]byte("abc"))
	r.Write([]byte("def"))

	if got := string(r.Bytes()); got != "abcdef" {
		t.Fatalf("实际 %q", got)
	}
	if r.Truncated() {
		t.Fatal("没超容量不应标记截断")
	}
}

func TestRingSlidesPartial(t *testing.T) {
	r := NewRingBuffer(8)
	r.Write([]byte("0123456"))
	r.Write([]byte("789"))

	// 累计 10 字节，丢掉最老的 2 个。
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatalf("实际 %q", got)
	}
	if !r.Truncated() {
		t.Fatal("应标记截断")
	}
}

func TestRingBytesIsCopy(t *testing.T) {
	r := NewRingBuffer(8)
	r.Write([]byte("abc"))

	out := r.Bytes()
	out[0] = 'X'

	// 调用方（前端序列化路径）改动返回值不能影响内部状态。
	if got := string(r.Bytes()); got != "abc" {
		t.Fatalf("内部数据被外部修改: %q", got)
	}
}

func TestRingZeroSizeIsSafe(t *testing.T) {
	r := NewRingBuffer(0)
	if n, err := r.Write([]byte("anything")); n != 8 || err != nil {
		t.Fatalf("零容量也必须返回完整写入，实际 n=%d err=%v", n, err)
	}
	if r.Len() != 0 || len(r.Bytes()) != 0 {
		t.Fatal("零容量不应保留数据")
	}
	if r.Truncated() {
		t.Fatal("零容量只是不保留，不是「截断过历史」")
	}

	var nilRing *RingBuffer
	if n, _ := nilRing.Write([]byte("x")); n != 1 {
		t.Fatal("nil 缓冲也必须返回完整写入")
	}
	if len(nilRing.Bytes()) != 0 || nilRing.Truncated() {
		t.Fatal("nil 缓冲应安全返回空")
	}
}

func TestRingWriteReturnsFullLength(t *testing.T) {
	r := NewRingBuffer(4)
	p := []byte("0123456789")
	// 短写会被 ssh 的拷贝循环当成错误，进而中断整条数据流。
	if n, err := r.Write(p); n != len(p) || err != nil {
		t.Fatalf("必须返回 len(p)，实际 n=%d err=%v", n, err)
	}
	if got := string(r.Bytes()); got != "6789" {
		t.Fatalf("实际 %q", got)
	}
}
