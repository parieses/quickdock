package platform

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quickdock/internal/sysutil"
)

// StreamOptions 一条流式命令的启动参数。
type StreamOptions struct {
	// Dir 工作目录（脚本目录），空 = 进程默认。
	Dir string
	// Env 完整环境变量（调用方负责在 os.Environ 基础上合并，不是替换语义）。
	Env []string
	// OnLine 逐行回调。stdout / stderr 两路共用，stream 标明来源。
	//
	// 回调是串行的（内部持锁），调用方不必再考虑并发；但也不能在回调里
	// 阻塞太久——它直接卡住子进程的管道读取。
	OnLine func(stream, line string)
	// Timeout <= 0 表示不限时。
	//
	// 与 RunCommandCapture 的超时语义**刻意不同**：那里超时只返回部分快照、
	// 不杀进程（用户可能在用命令启动常驻程序）；这里超时必须杀——脚本带着
	// 超时上限跑，到点不停就永远占着并发闸门，webhook 也会被同一脚本堵死。
	Timeout time.Duration
}

// StreamResult 流式命令的收尾结果。
type StreamResult struct {
	ExitCode int
	Canceled bool // 被 ctx 取消（用户点了停止）
	TimedOut bool // 达到 Timeout（与 Canceled 区分：文案与重试策略不同）
	Err      error
}

// streamLineMax 单行上限（1MB）。
//
// 超长行（压缩过的 JSON、base64 甩一行几十 MB）不设上限会直接把内存吃穿；
// Scanner 遇到 ErrTooLong 会停止读取该路，后续输出丢弃——这个代价可以接受，
// 因为脚本日志的用途是「看它在干什么」，不是「完整搬运数据」。
const streamLineMax = 1 << 20

// StreamCommand 启动一条命令，把 stdout/stderr 逐行回调出去，直到退出/取消/超时。
//
// 为什么不复用 RunCommandCapture：那条路有三个反着的特性——输出封顶 8KB、
// 结果被压成单行、不支持 cmd.Env。脚本任务这三样都要反着来（完整日志、
// 保留换行、注入变量），但底层的 commandForRun / sysutil.Hide / KillProcess
// 又必须共用（尤其 Windows 下含括号的 bat 路径引用规则），故放在同一个包。
func StreamCommand(ctx context.Context, argv []string, opts StreamOptions) StreamResult {
	if len(argv) == 0 || argv[0] == "" {
		return StreamResult{ExitCode: -1, Err: fmt.Errorf("命令为空")}
	}

	parentCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := commandForRun(argv[0], argv[1:])
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	}
	// 隐藏子进程控制台窗口，否则每个脚本都会弹一个黑框。
	sysutil.Hide(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return StreamResult{ExitCode: -1, Err: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return StreamResult{ExitCode: -1, Err: err}
	}
	if err := cmd.Start(); err != nil {
		return StreamResult{ExitCode: -1, Err: err}
	}

	var (
		wg     sync.WaitGroup
		emitMu sync.Mutex
	)
	emit := func(stream, line string) {
		if opts.OnLine == nil {
			return
		}
		emitMu.Lock()
		opts.OnLine(stream, line)
		emitMu.Unlock()
	}
	wg.Add(2)
	go pumpStream(stdout, "stdout", emit, &wg)
	go pumpStream(stderr, "stderr", emit, &wg)

	// Wait 必须等两路读完再调：exec 在 Wait 里关闭管道，先 Wait 会截断尾部输出。
	done := make(chan error, 1)
	go func() {
		wg.Wait()
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return StreamResult{ExitCode: exitCodeOf(err)}
	case <-ctx.Done():
		if cmd.Process != nil {
			// Windows 走 taskkill /F 连子进程，避免 cmd /c 的孙进程残留；
			// 只杀父进程会让 python 子进程继续跑，用户以为停了其实没停。
			_ = sysutil.KillProcess(cmd.Process.Pid)
		}
		wg.Wait()
		err := <-done
		// 父 ctx 有错 = 用户主动取消；否则是我们自己加的 deadline。
		timedOut := parentCtx.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
		return StreamResult{
			ExitCode: exitCodeOf(err),
			Canceled: !timedOut,
			TimedOut: timedOut,
		}
	}
}

// pumpStream 把一路输出逐行吐出。
func pumpStream(r io.ReadCloser, stream string, emit func(string, string), wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), streamLineMax)
	for sc.Scan() {
		emit(stream, sanitizeLine(sc.Text()))
	}
	// ErrTooLong 是「行太长」而非读取失败，已经产生的输出仍然有效，不额外报错。
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		emit("stderr", "输出读取失败："+err.Error())
	}
}

// sanitizeLine 清掉一行输出里的终端控制序列。
//
// 为什么非清不可：pip / npm / 大多数 CLI 的输出带 ANSI 颜色码（ESC[32m…），
// 日志面板是纯文本渲染，直接显示会变成 `←[32m✓←[0m` 这种乱码；而进度条用
// `\r` 反复回到行首重画，按行读会得到一个每次刷新追加一行的「假日志」——
// 一个 pip install 能刷出几百行同一条进度。两种都在日志文件里留下来，事后
// 用编辑器看同样脏。
func sanitizeLine(s string) string {
	if s == "" {
		return s
	}
	// 先折叠 \r（进度条重画），再剥 ESC（颜色/光标），顺序无关但先折叠更省事：
	// 被覆盖掉的那些片段连剥都不用剥。
	if strings.IndexByte(s, '\r') >= 0 {
		s = collapseCR(s)
	}
	if strings.IndexByte(s, 0x1b) >= 0 || strings.IndexByte(s, 0x9b) >= 0 {
		s = stripEscape(s)
	}
	return strings.TrimRight(s, "\x00")
}

// collapseCR 取 \r 分段里最后一个有内容的片段。
//
// 语义近似终端的「回车覆盖」：`50%\r100%` 光标回行首写 100%，人眼看到的是
// 100%。严格还原（按列覆盖、处理 \b、保留超长残留）没有必要——日志的用途是
// 「看它跑到哪了」，多留几个字符反而更难读。
func collapseCR(s string) string {
	parts := strings.Split(s, "\r")
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(parts[i]) != "" {
			return parts[i]
		}
	}
	return ""
}

// stripEscape 删掉 ANSI 转义序列。
//
// 手写扫描而不是正则：这条路径在脚本高频输出时是热路径，且正则版要么漏
// OSC（`ESC ] 0 ; title BEL`）要么把普通文本里的 `[` 误伤。只识别真正以
// ESC（0x1b）或 8 位 CSI（0x9b）开头的序列，其余字节原样保留。
func stripEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch s[i] {
		case 0x1b:
			i = skipEscape(s, i)
		case 0x9b: // 8 位 CSI，等价于 ESC [
			i = skipCSI(s, i+1)
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// skipEscape 返回 ESC 序列结束后的下标。
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI：参数/中间字节 + 单个终止字节 0x40-0x7e
		return skipCSI(s, i+2)
	case ']': // OSC：到 BEL 或 ST（ESC \）
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	case '(', ')', '*', '+', '-', '.', '/', '#': // 字符集选择：ESC ( B 之类，3 字节
		return min(i+3, len(s))
	default:
		return min(i+2, len(s))
	}
}

func skipCSI(s string, i int) int {
	for j := i; j < len(s); j++ {
		if s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1
		}
	}
	return len(s)
}

// ResolveExeIn 在给定目录里按候选名找可执行文件，找不到返回 ""。
// 单独一个函数而不是直接用 exec.LookPath：脚本任务的解释器优先取 QuickDock
// 自己装的便携运行时目录（那里没进 PATH 也可能存在），只在探测命中后
// 才拿绝对路径去起进程，避免依赖用户 PATH 的当前状态。
//
// 候选名由调用方按平台给出（Windows 要带 .exe），本函数只做「存在且非目录」判断。
func ResolveExeIn(dir string, names ...string) string {
	if dir == "" {
		return ""
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		p := filepath.Join(dir, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
