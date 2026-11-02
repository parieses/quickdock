package aitools

// 命令执行的实时输出通道。
//
// 为什么要有这条通道：run_command 原来是「等命令结束，把捕获缓冲里的内容解码后
// 一次性回填进卡片」。pip install / npm install / go build 这类命令要跑几十秒到
// 几分钟，这期间界面上只有一句「正在执行」——用户分不清它是在下载依赖、编译，
// 还是已经卡死，于是去点停止，把一次本来正常的安装打断。
//
// 通道只挂在 ctx 上（WithCmdProgress），不新增工具参数、不动注册表：
// 没挂回调的调用方（单测、后台执行、子 agent）自然退回「跑完一次性给结果」，
// 行为与改动前完全一致。
//
// 事件为什么拆成 (add, tail) 两段而不是一段文本：
//   - add 是已经遇到换行的完整行，只能往后追加；
//   - tail 是还没换行的当前行，下一次事件整段替换它。
//
// pip 的下载进度条用 \r 反复重写同一行（"  45%|████     | 1.2/2.7MB"）。
// 如果一律当追加处理，一张卡片几秒钟就会被刷满几十行几乎一样的进度，
// 反而看不出到底走到哪了；拆出 tail 之后，那一行在原地跳，行数与终端里一致。

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/platform"
)

const (
	// cmdStreamMinGap 两次推送之间的最小间隔。
	//
	// 取 120ms 而不是「每行都推」：npm install 一秒能吐几百行，逐行 push 会把
	// SSE 连接和前端渲染同时压住，而这种频率人眼也不需要——屏幕一帧才 16ms。
	cmdStreamMinGap = 120 * time.Millisecond

	// cmdStreamMaxPush 一条命令最多实时推给前端多少字节。
	//
	// 超了就不再推（命令照常跑完，最终结果仍由 tool_result 给全）：实时输出的
	// 作用是「让人看见它在动」，不是日志归档。真要看完整输出应该重定向到文件。
	cmdStreamMaxPush = 64 << 10
)

// CmdProgressFn 实时输出回调：callID 是工具调用 id（事件靠它找到卡片），
// add 是新增的完整行，tail 是当前未完成行（每次整体替换）。
type CmdProgressFn func(callID, add, tail string)

type cmdProgressKey struct{}

// WithCmdProgress 把实时输出回调挂到 ctx 上。
//
// 用 ctx 而不是给工具加参数：工具签名与注册表由 internal/mcp 统一管理，
// 为一个纯展示用的旁路信息改签名，会让所有工具都背上一个它们不用的参数。
// ctx 本来就是「这次调用额外的事实」的通道（工具调用 id、作业收集器都在这）。
func WithCmdProgress(ctx context.Context, fn CmdProgressFn) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, cmdProgressKey{}, fn)
}

// CmdProgressFrom 取出 ctx 上的实时输出回调；没挂返回 nil。
func CmdProgressFrom(ctx context.Context) CmdProgressFn {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(cmdProgressKey{}).(CmdProgressFn)
	return fn
}

// cmdStream 把两路输出（stdout / stderr）合并成一条按终端语义渲染的字节流。
//
// 两路共用一个实例：终端里 stdout 与 stderr 是交织的，分成两个缓冲各推一份，
// 卡片上的顺序就会变成「先所有 stdout 再所有 stderr」——pip 的字节数与百分比
// 恰恰分别走在两路上，错开之后就是一份读不懂的日志。
//
// 并发：os/exec 为每路管道开一个 goroutine 复制，所以 Write 会被并发调用，
// 内部一律持锁。写入顺序即到达顺序，与终端一致。
type cmdStream struct {
	mu   sync.Mutex
	emit func(add, tail string)

	line []byte   // 当前行（原始字节，未解码）
	add  []string // 待推的完整行（已解码）
	cr   bool     // 上一个字节是 \r：下一个字节若是常规字符即「回车覆盖」

	lastTail string    // 上次推送的当前行，用于「没变化就不推」
	lastAt   time.Time // 上次推送时间（节流）
	sent     int       // 已推字节数（上限控制）
	stopped  bool      // 已到上限，不再推
}

func newCmdStream(emit func(add, tail string)) *cmdStream {
	return &cmdStream{emit: emit}
}

// Write 实现 io.Writer：只做分词与节流，不改变调用方拿到的字节数——
// 返回短写会让 os/exec 以为管道出错并提前掐断命令。
func (s *cmdStream) Write(p []byte) (int, error) {
	if s == nil || s.emit == nil {
		return len(p), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		switch b {
		case '\r':
			// 先不下结论：\r\n 是换行，孤立的 \r 是「回到行首重写」。
			// 两者只能等下一个字节到达时才分得清，所以这里只记标记。
			s.cr = true
		case '\n':
			s.cr = false
			s.commitLineLocked()
		default:
			if s.cr {
				s.cr = false
				s.line = s.line[:0] // 回车覆盖：新内容从行首重写
			}
			s.line = append(s.line, b)
		}
	}
	s.maybeFlushLocked()
	return len(p), nil
}

// commitLineLocked 把当前行并入待推行（调用方须持锁）。
func (s *cmdStream) commitLineLocked() {
	s.add = append(s.add, decodeCmdLine(s.line))
	s.line = s.line[:0]
}

// maybeFlushLocked 按节流窗口决定要不要推（调用方须持锁）。
func (s *cmdStream) maybeFlushLocked() {
	if s.stopped || (len(s.add) == 0 && len(s.line) == 0) {
		return
	}
	// 输出稀疏时（每行隔好几秒）每次写都超过窗口，于是立即推；
	// 输出密集时按 120ms 合并，一帧的粒度。
	if !s.lastAt.IsZero() && time.Since(s.lastAt) < cmdStreamMinGap {
		return
	}
	s.flushLocked()
}

// flushLocked 立即推一次（调用方须持锁）。
func (s *cmdStream) flushLocked() {
	if s.stopped || s.emit == nil {
		return
	}
	add := strings.Join(s.add, "\n")
	s.add = s.add[:0]
	if add != "" {
		add += "\n"
	}
	tail := decodeCmdLine(s.line)
	// 只有 tail 没变化就别推：进度条没动的时候不该产生事件。
	if add == "" && tail == s.lastTail {
		return
	}
	if s.sent+len(add)+len(tail) > cmdStreamMaxPush {
		s.stopped = true
		s.emit("\n……（实时输出已超过 "+strconv.Itoa(cmdStreamMaxPush>>10)+
			"KB，停止实时刷新；命令结束后显示完整结果）\n", "")
		return
	}
	s.sent += len(add) + len(tail)
	s.lastTail = tail
	s.lastAt = time.Now()
	s.emit(add, tail)
}

// Close 把剩下的内容推完。命令结束后必须调用，否则最后一行（通常还没换行）
// 会一直留在缓冲里，直到 tool_result 把整块输出覆盖上去——那一段就白跑了。
func (s *cmdStream) Close() {
	if s == nil || s.emit == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || (len(s.add) == 0 && len(s.line) == 0) {
		return
	}
	s.flushLocked()
}

// decodeCmdLine 按控制台代码页解一行。
//
// 与控制台一致按行解（而不是整段）：cmd.exe 自己的提示是 OEM 页（中文系统 936），
// 而 node / go / pip 写出来的是 UTF-8，整段按 GBK 解会把后者变成乱码。
// 行尾恰好被 Write 切在多字节序列中间时，DecodeConsoleText 会削掉那几个残渣
// 而不是留一串 U+FFFD。
func decodeCmdLine(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return platform.DecodeConsoleText(string(b))
}
