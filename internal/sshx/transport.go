package sshx

import (
	"sync"
	"sync/atomic"
	"time"
)

// 终端数据的合批传输。
//
// 为什么必须合批：SSH 读循环拿到的粒度由服务器决定，一次可能只有几十字节
// （终端每刷新一个 ANSI 序列就发一包）。如果逐包过 Wails 的 Event（JSON 序列化
// + base64 + 主线程派发），满速输出时每秒会触发上万次 emit，前端主线程被灌满，
// 进而触发 main.ts 的心跳看门狗把整个页面 reload 掉——终端内容直接从视觉上消失。
//
// 合批后按帧走：16ms 一帧、单帧上限 64KB 原始字节（base64 后约 87KB）。
// 实测真机 htop 约 3.6KB/s、满速 cat 大文件约 1.2MB/s，这个节奏有 10 倍以上余量。

const (
	// DefaultFlushInterval 合批间隔。
	//
	// 16ms 对齐 60fps 的帧时间：比这更快，前端的渲染速度也跟不上，只是白做序列化；
	// 比这更慢，按键回显和命令输出的延迟就能被用户感知到。
	DefaultFlushInterval = 16 * time.Millisecond

	// MaxFrameBytes 单帧的原始字节上限。
	//
	// 不是性能考虑，是稳定考虑：base64 会膨胀 33%、再加 JSON 封装，一帧上桥约 88KB。
	// 再大就会让单次序列化和主线程处理时间变长，反而更容易触发看门狗。
	MaxFrameBytes = 64 << 10

	// maxBacklogPerSession 单个会话允许积压的上限。
	//
	// 超过说明消费端（前端或 Event 通道）已经严重跟不上，此时继续攒只会把内存吃光。
	// 处理方式是丢掉旧积压、保留最新——终端展示的是「当前屏幕状态」，
	// 旧数据本身已经没有显示价值，丢掉比卡死好。
	maxBacklogPerSession = 8 << 20
)

// Sink 是合批后的数据出口。
//
// 实现在门面层（当前是 Wails Event）。将来若实测发现 Event 通道不够用，
// 换成本地 WebSocket 只需要换一个 Sink 实现，引擎和会话层不用动。
//
// 约定：Send 必须在返回前完成对 data 的拷贝或消费——Batcher 会在下一次 flush
// 时复用这块内存。
type Sink interface {
	Send(sessionID string, data []byte) error
}

// Batcher 把高频小包聚合成低频大帧。
//
// 一个 Batcher 服务全进程所有会话（而不是每个会话一个），这样只有一个
// ticker goroutine，也不会出现多个 ticker 同时争抢 Event 通道。
type Batcher struct {
	sink     Sink
	interval time.Duration

	mu      sync.Mutex
	pending map[string]*pendingBuf
	closed  bool

	done chan struct{}
	wg   sync.WaitGroup

	sentFrames atomic.Int64
	dropped    atomic.Int64
}

type pendingBuf struct {
	data []byte
}

// NewBatcher 创建合批器并启动后台 flush 循环。
// interval <= 0 时使用 DefaultFlushInterval。
func NewBatcher(sink Sink, interval time.Duration) *Batcher {
	if interval <= 0 {
		interval = DefaultFlushInterval
	}
	b := &Batcher{
		sink:     sink,
		interval: interval,
		pending:  make(map[string]*pendingBuf),
		done:     make(chan struct{}),
	}
	b.wg.Add(1)
	go b.loop()
	return b
}

// Push 把原始字节放入待发队列。
//
// 这个调用必须足够快且永不阻塞：它跑在 SSH 的读循环里，一旦阻塞，
// 服务器侧的窗口就会填满，远端命令会卡在写阻塞上，表现为「终端假死」。
func (b *Batcher) Push(sessionID string, p []byte) {
	if len(p) == 0 {
		return
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	pb := b.pending[sessionID]
	if pb == nil {
		pb = &pendingBuf{}
		b.pending[sessionID] = pb
	}
	if len(pb.data)+len(p) > maxBacklogPerSession {
		// 消费端严重滞后：丢掉旧积压，只保留最新的这段。
		b.dropped.Add(int64(len(pb.data)))
		pb.data = pb.data[:0]
	}
	pb.data = append(pb.data, p...)
	b.mu.Unlock()
}

// Flush 立即发送某个会话的积压数据（会话断开/关闭时调用，避免最后几帧丢掉）。
func (b *Batcher) Flush(sessionID string) {
	b.mu.Lock()
	pb := b.pending[sessionID]
	if pb == nil {
		b.mu.Unlock()
		return
	}
	delete(b.pending, sessionID)
	data := pb.data
	b.mu.Unlock()
	b.send(sessionID, data)
}

// FlushAll 发送所有会话的积压。
func (b *Batcher) FlushAll() {
	b.mu.Lock()
	batch := b.pending
	b.pending = make(map[string]*pendingBuf, len(batch))
	b.mu.Unlock()

	for id, pb := range batch {
		b.send(id, pb.data)
	}
}

// Close 停止后台循环，并把剩余数据冲刷出去。
func (b *Batcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()

	close(b.done)
	b.wg.Wait()
	b.FlushAll()
}

// DroppedBytes 因积压超限被丢弃的字节数（诊断用）。
func (b *Batcher) DroppedBytes() int64 { return b.dropped.Load() }

// Frames 已发送的帧数（诊断用）。
func (b *Batcher) Frames() int64 { return b.sentFrames.Load() }

func (b *Batcher) loop() {
	defer b.wg.Done()
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.FlushAll()
		case <-b.done:
			return
		}
	}
}

// send 按 MaxFrameBytes 切帧后交给 Sink。
//
// 切帧只能在字节边界做，绝不能按字符切：终端数据里既可能有 UTF-8 多字节序列，
// 也可能有被拆在两包之间的 ANSI escape 序列。前端必须把它当作连续字节流
// （Uint8Array 喂给 term.write），而不是每帧独立 decode 成字符串——
// 后者会在帧边界把中文劈成半个字符，显示为一堆替换符。
func (b *Batcher) send(sessionID string, data []byte) {
	if b.sink == nil {
		return
	}
	for len(data) > 0 {
		n := len(data)
		if n > MaxFrameBytes {
			n = MaxFrameBytes
		}
		// 忽略 Send 错误：唯一的失败场景是应用正在退出、Event 通道已关闭，
		// 此时重试没有意义，也不该让合批循环退出。
		_ = b.sink.Send(sessionID, data[:n])
		b.sentFrames.Add(1)
		data = data[n:]
	}
}
