package aitools

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type streamRec struct{ add, tail string }

// newTestStream 建一条流并收集所有推送。
//
// 回调在 Write 里同步调用，所以测试串行读切片是安全的（真实链路里回调是
// sink.Event，只往 SSE 或事件表写，不会回头碰这条流）。
func newTestStream() (*cmdStream, *[]streamRec) {
	var got []streamRec
	s := newCmdStream(func(add, tail string) {
		got = append(got, streamRec{add: add, tail: tail})
	})
	return s, &got
}

// skipGap 把上次推送时间推到窗口之外，下一次 Write 就能立刻推。
// 直接改字段而不是 sleep：120ms 的窗口在测试里 sleep 十几次就是两秒白等。
func skipGap(s *cmdStream) {
	s.mu.Lock()
	s.lastAt = time.Now().Add(-2 * cmdStreamMinGap)
	s.mu.Unlock()
}

func TestCmdStreamSplitsCompleteLines(t *testing.T) {
	s, got := newTestStream()
	_, _ = s.Write([]byte("Collecting foo\nDownloading bar\n"))
	if len(*got) != 1 {
		t.Fatalf("期望推一次，实际 %d 次：%v", len(*got), *got)
	}
	if want := "Collecting foo\nDownloading bar\n"; (*got)[0].add != want {
		t.Errorf("add = %q，期望 %q", (*got)[0].add, want)
	}
	if (*got)[0].tail != "" {
		t.Errorf("两份都换行了，tail 应为空，实际 %q", (*got)[0].tail)
	}
}

func TestCmdStreamCarriageReturnOverwritesTail(t *testing.T) {
	s, got := newTestStream()
	_, _ = s.Write([]byte("Downloading 10%"))
	skipGap(s)
	// pip 的进度条就是这样：同一个 \r 回到行首重写整行
	_, _ = s.Write([]byte("\rDownloading 45%"))
	skipGap(s)
	_, _ = s.Write([]byte("\rDownloading 90%"))

	last := (*got)[len(*got)-1]
	if last.tail != "Downloading 90%" {
		t.Errorf("tail = %q，期望被覆盖成最后一版进度", last.tail)
	}
	// 关键：中途的百分比不能被当成新行累加进来，否则卡片上会刷出十几行一样的进度
	for _, r := range *got {
		if strings.Contains(r.add, "45%") {
			t.Errorf("进度行被当成完整行追加了：%q", r.add)
		}
	}
}

func TestCmdStreamCRLFIsNewlineNotOverwrite(t *testing.T) {
	s, got := newTestStream()
	_, _ = s.Write([]byte("done\r\nnext\r\n"))
	if len(*got) == 0 {
		t.Fatal("没有推送")
	}
	if want := "done\nnext\n"; (*got)[0].add != want {
		t.Errorf("add = %q，期望 %q（\\r\\n 不能把整行清掉）", (*got)[0].add, want)
	}
}

func TestCmdStreamCRSplitAcrossWrites(t *testing.T) {
	// 管道的切分点可能正好落在 \r 和 \n 之间：两边各来一半也必须当成换行
	s, got := newTestStream()
	_, _ = s.Write([]byte("done\r"))
	skipGap(s)
	_, _ = s.Write([]byte("\n"))
	joined := ""
	for _, r := range *got {
		joined += r.add
	}
	if !strings.Contains(joined, "done\n") {
		t.Errorf("跨 Write 的 \\r\\n 没被识别成换行，收到 %q", joined)
	}
}

func TestCmdStreamThrottlesBurstWrites(t *testing.T) {
	s, got := newTestStream()
	// 第一次没有上次时间，立即推；紧跟的 50 次写都在窗口内，应该被合并掉
	_, _ = s.Write([]byte("line 0\n"))
	for i := 1; i <= 50; i++ {
		_, _ = s.Write([]byte("line burst\n"))
	}
	if len(*got) > 3 {
		t.Errorf("120ms 窗口内推了 %d 次，节流没生效：%v", len(*got), *got)
	}
	// 但内容不能丢：窗口过后要把攒下的行一起推出去
	skipGap(s)
	_, _ = s.Write([]byte("line last\n"))
	all := ""
	for _, r := range *got {
		all += r.add + r.tail
	}
	if strings.Count(all, "line burst") != 50 {
		t.Errorf("攒下的行丢了：收到 %d 条 burst", strings.Count(all, "line burst"))
	}
}

func TestCmdStreamStopsAtCap(t *testing.T) {
	s, got := newTestStream()
	chunk := strings.Repeat("x", 8<<10) + "\n"
	for i := 0; i < 12; i++ {
		skipGap(s)
		_, _ = s.Write([]byte(chunk))
	}
	last := (*got)[len(*got)-1]
	if !strings.Contains(last.add, "实时输出已超过") {
		t.Errorf("超过上限后应提示一句，实际最后一条是 %q", last.add)
	}
	// 上限之后不再推：总推送量不该远超 64KB
	total := 0
	for _, r := range *got {
		total += len(r.add) + len(r.tail)
	}
	if total > cmdStreamMaxPush*2 {
		t.Errorf("超限后仍在推，总量 %d 字节", total)
	}
}

func TestCmdStreamCloseFlushesLastLine(t *testing.T) {
	s, got := newTestStream()
	_, _ = s.Write([]byte("Building..."))
	skipGap(s)
	// 命令结束时最后一行通常还没换行：不 flush 的话它就留在缓冲里，
	// 用户看到的是「一行都没输出」。
	s.Close()
	last := (*got)[len(*got)-1]
	if last.tail != "Building..." {
		t.Errorf("Close 没把未换行的那一行推出去，tail = %q", last.tail)
	}
}

func TestCmdStreamConcurrentWrites(t *testing.T) {
	// os/exec 为 stdout / stderr 各开一个 goroutine 复制，两路会同时写进来
	s, got := newTestStream()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_, _ = s.Write([]byte("out\n"))
			}
		}()
	}
	wg.Wait()
	s.Close()
	if len(*got) == 0 {
		t.Fatal("并发写之后一条都没推")
	}
}

func TestCmdProgressContext(t *testing.T) {
	if CmdProgressFrom(nil) != nil {
		t.Error("nil ctx 应返回 nil")
	}
	ctx := WithCmdProgress(t.Context(), func(callID, add, tail string) {})
	if CmdProgressFrom(ctx) == nil {
		t.Error("挂上的回调取不回来")
	}
	// 没挂回调的 ctx（绝大多数调用方）必须返回 nil，run_command 据此走老路径
	if CmdProgressFrom(t.Context()) != nil {
		t.Error("没挂过却取到了回调")
	}
}
