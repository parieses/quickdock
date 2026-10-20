package sshx

import "sync"

// RingBuffer 固定容量的滚动缓冲，用于「关掉窗口再打开，画面还在」。
//
// 为什么需要它：终端会话常驻在 Go 侧，但前端窗口关闭时页面状态会全部丢弃。
// 重开窗口只能靠把最近一段输出重新喂给 xterm 来恢复画面，而全量缓存一个
// 长时间运行的会话（`tail -f` 挂一整天）会无限吃内存，所以必须滚动丢弃最老的部分。
//
// 为什么不和 Batcher 合并：职责完全不同。Batcher 管「尽快送达」，容量以毫秒计；
// Ring 管「最后留下什么」，容量以屏幕可读性计。
type RingBuffer struct {
	mu      sync.Mutex
	buf     []byte
	size    int
	dropped bool // 已经因为容量不足丢过数据
}

// NewRingBuffer 创建容量为 size 字节的滚动缓冲。size <= 0 时返回 nil 行为的安全对象（不保留任何数据）。
func NewRingBuffer(size int) *RingBuffer {
	if size < 0 {
		size = 0
	}
	return &RingBuffer{
		buf:  make([]byte, 0, size),
		size: size,
	}
}

// Write 写入数据，超出容量的部分覆盖最老的数据。
//
// 永远返回 len(p), nil：调用方是 SSH 的写路径，短写会被当成错误处理，
// 返回少了会中断整条数据流。
func (r *RingBuffer) Write(p []byte) (int, error) {
	if r == nil || r.size == 0 || len(p) == 0 {
		return len(p), nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 单次写入就超过总容量：只保留尾部，前面的没有意义。
	if len(p) >= r.size {
		r.buf = append(r.buf[:0], p[len(p)-r.size:]...)
		r.dropped = true
		return len(p), nil
	}

	if overflow := len(r.buf) + len(p) - r.size; overflow > 0 {
		// 用 copy 原地收缩，避免 buf[overflow:] 让切片起点漂移、
		// 后续 append 时不断重新分配。
		copy(r.buf, r.buf[overflow:])
		r.buf = r.buf[:len(r.buf)-overflow]
		r.dropped = true
	}
	r.buf = append(r.buf, p...)
	return len(p), nil
}

// Bytes 返回当前内容的副本。
//
// 必须拷贝：调用方（前端回放）在另一个 goroutine 里序列化它，
// 直接返回底层切片会和读循环的写入构成数据竞争。
func (r *RingBuffer) Bytes() []byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// Len 当前保留的字节数。
func (r *RingBuffer) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

// Truncated 是否已经丢弃过早期数据。
//
// 前端可以据此提示「上方内容已滚动丢弃」——不说的话，用户会以为
// 命令输出就是这么短，去排查一个根本不存在的 bug。
func (r *RingBuffer) Truncated() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
