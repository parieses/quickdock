package sshx

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// fakeSink 记录所有收到的帧，并可模拟消费端变慢。
type fakeSink struct {
	mu     sync.Mutex
	frames []sinkFrame
	delay  time.Duration
}

type sinkFrame struct {
	id   string
	data []byte
}

func (f *fakeSink) Send(id string, data []byte) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	// 必须拷贝：Batcher 的约定是 Send 返回后底层数组可能会被复用。
	cp := make([]byte, len(data))
	copy(cp, data)

	f.mu.Lock()
	f.frames = append(f.frames, sinkFrame{id: id, data: cp})
	f.mu.Unlock()
	return nil
}

func (f *fakeSink) snapshot() []sinkFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sinkFrame, len(f.frames))
	copy(out, f.frames)
	return out
}

// drain 把收到的所有帧按会话拼接起来。
func (f *fakeSink) drain(id string) []byte {
	var out bytes.Buffer
	for _, fr := range f.snapshot() {
		if fr.id == id {
			out.Write(fr.data)
		}
	}
	return out.Bytes()
}

// TestBatcherAggregatesSmallWrites 守住合批的核心价值：
// 一次 tick 内的多个小包必须合并成少数几帧，否则满速输出会把前端主线程灌满。
func TestBatcherAggregatesSmallWrites(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, 5*time.Millisecond)
	defer b.Close()

	var want bytes.Buffer
	for i := 0; i < 200; i++ {
		chunk := []byte{byte('a' + i%26)}
		want.Write(chunk)
		b.Push("s1", chunk)
	}

	waitUntil(t, "数据发出", func() bool {
		return len(sink.drain("s1")) == want.Len()
	})

	frames := sink.snapshot()
	if len(frames) >= 200 {
		t.Fatalf("没有合批：200 个包发了 %d 帧", len(frames))
	}
	if got := sink.drain("s1"); !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("数据不一致: got=%q want=%q", got, want.Bytes())
	}
}

// TestBatcherSplitsLargeFrame 单帧不得超过 MaxFrameBytes。
func TestBatcherSplitsLargeFrame(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, time.Hour) // 关掉自动 flush，只用显式 Flush
	defer b.Close()

	total := MaxFrameBytes*3 + 1234
	b.Push("s1", bytes.Repeat([]byte("x"), total))
	b.Flush("s1")

	frames := sink.snapshot()
	if len(frames) != 4 {
		t.Fatalf("应切成 4 帧，实际 %d 帧", len(frames))
	}
	for i, fr := range frames {
		if len(fr.data) > MaxFrameBytes {
			t.Fatalf("第 %d 帧超限: %d 字节", i, len(fr.data))
		}
	}
	if n := len(sink.drain("s1")); n != total {
		t.Fatalf("切帧后总字节数不对: got=%d want=%d", n, total)
	}
}

// TestBatcherFlushIsImmediate Flush 不等 ticker，会话断开时必须靠它把尾巴发出去。
func TestBatcherFlushIsImmediate(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, time.Hour)
	defer b.Close()

	b.Push("s1", []byte("tail"))
	if len(sink.snapshot()) != 0 {
		t.Fatal("Flush 之前不该发出数据")
	}

	b.Flush("s1")
	if got := sink.drain("s1"); string(got) != "tail" {
		t.Fatalf("Flush 应立刻发送，实际 %q", got)
	}

	// 重复 Flush 同一个会话不应重复发送。
	b.Flush("s1")
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("重复 Flush 不应重复发送，帧数=%d", n)
	}
}

// TestBatcherCloseFlushesPending 退出前必须把积压冲出去。
func TestBatcherCloseFlushesPending(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, time.Hour)

	b.Push("s1", []byte("bye"))
	b.Close()

	if got := sink.drain("s1"); string(got) != "bye" {
		t.Fatalf("Close 应冲刷积压，实际 %q", got)
	}

	// 关闭后再 Push 不能 panic、也不能再发。
	b.Push("s1", []byte("late"))
	if n := len(sink.snapshot()); n != 1 {
		t.Fatalf("Close 后不应再发送，帧数=%d", n)
	}
}

// TestBatcherDropsBacklogWhenSinkStalls 消费端严重滞后时丢旧留新，而不是把内存吃光。
//
// 终端只需要「当前屏幕状态」，旧字节已经没有显示价值。这里比的是：
// 丢掉积压后必须还能收到最新写入的那段，否则用户看到的是完全过时的画面。
func TestBatcherDropsBacklogWhenSinkStalls(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, time.Hour) // 不让 ticker 自动清空
	defer b.Close()

	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < 10; i++ { // 累计 10MB，超过 8MB 上限
		b.Push("s1", chunk)
	}
	b.Push("s1", []byte("LATEST"))
	b.Flush("s1")

	if b.DroppedBytes() == 0 {
		t.Fatal("应在积压超限时记下丢弃字节数")
	}
	got := sink.drain("s1")
	if !bytes.HasSuffix(got, []byte("LATEST")) {
		t.Fatalf("必须保留最新数据，实际尾部: %q", tail(got, 20))
	}
	if len(got) > maxBacklogPerSession+len("LATEST") {
		t.Fatalf("积压未被裁剪，发送了 %d 字节", len(got))
	}
}

// TestBatcherSeparatesSessions 不同会话的数据不能串。
func TestBatcherSeparatesSessions(t *testing.T) {
	sink := &fakeSink{}
	b := NewBatcher(sink, 5*time.Millisecond)
	defer b.Close()

	b.Push("a", []byte("AAA"))
	b.Push("b", []byte("BBB"))
	b.FlushAll()

	if got := string(sink.drain("a")); got != "AAA" {
		t.Fatalf("会话 a 收到 %q", got)
	}
	if got := string(sink.drain("b")); got != "BBB" {
		t.Fatalf("会话 b 收到 %q", got)
	}
}

// TestBatcherNilSinkDoesNotPanic 引擎可以在没有出口的情况下跑（例如纯 Exec 场景）。
func TestBatcherNilSinkDoesNotPanic(t *testing.T) {
	b := NewBatcher(nil, time.Millisecond)
	b.Push("s1", []byte("data"))
	b.Flush("s1")
	b.Close()
}

// waitUntil 轮询等待条件成立，超时即失败。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时等待: %s", what)
}

func tail(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}
