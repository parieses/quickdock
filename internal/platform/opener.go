package platform

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"quickdock/internal/sysutil"
)

// dangerousSchemes 拒绝直接通过 ShellExecute 触发的危险协议（存储型协议注入防护）。
var dangerousSchemes = []string{
	"javascript:", "vbscript:", "ms-powershell:", "powershell:",
	"cmd:", "ms-msdt:", "msdt:", "wscript:", "cscript:",
}

func rejectDangerous(target string) error {
	lower := strings.ToLower(strings.TrimSpace(target))
	for _, p := range dangerousSchemes {
		if strings.HasPrefix(lower, p) {
			return fmt.Errorf("拒绝危险协议: %s", p)
		}
	}
	return nil
}

// ShellOpen 使用系统默认关联程序打开软件/文件/目录/网址（等价于双击/在浏览器打开）。
// workingDir 为空时使用进程默认目录。
//
// 路由策略（优先框架 Browser，跨平台；失败或不适配时回退 ShellExecute 直连）：
//   - http/https 网址、真实存在的本地路径 → app.Browser.OpenURL / OpenFile
//   - workingDir 非空（quicklink 需要工作目录，Browser 不支持）→ 直连
//   - 自定义协议（vscode:// 等）、不存在的裸目标 → 直连，维持旧的系统关联解析行为
func ShellOpen(target, workingDir string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("打开目标为空")
	}
	if err := rejectDangerous(target); err != nil {
		return err
	}

	if strings.TrimSpace(workingDir) == "" {
		lower := strings.ToLower(target)
		switch {
		case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
			// 网址：无空格歧义，直接走框架
			return browserOpen(target, true)
		case !strings.Contains(lower, "://"):
			// 仅真实存在且不含空白的本地路径走框架；
			// 含空白路径 rundll32 的参数解析不可靠，交 ShellExecute（对空格/引号最稳）
			if _, err := os.Stat(target); err == nil && !strings.ContainsAny(target, " \t") {
				return browserOpen(target, false)
			}
		}
	}
	return shellOpenDirect(target, workingDir)
}

// browserOpen 经框架 Browser 打开（Windows= rundll32 FileProtocolHandler，
// darwin=open，linux=xdg-open）；框架未就绪或调用失败时回退直连保持可用性。
func browserOpen(target string, isURL bool) error {
	if app := application.Get(); app != nil && app.Browser != nil {
		var err error
		if isURL {
			err = app.Browser.OpenURL(target)
		} else {
			err = app.Browser.OpenFile(target)
		}
		if err == nil {
			return nil
		}
	}
	return shellOpenDirect(target, "")
}

// RevealInExplorer 在系统文件管理器中定位并选中目标路径。
// 目录 → 直接打开该目录；文件 → 高亮选中（Windows=explorer /select，mac=Finder 显示）；
// 路径不存在 → 退回打开父目录。
func RevealInExplorer(target string) error {
	target = strings.TrimSpace(strings.Trim(strings.TrimSpace(target), `"`))
	if target == "" {
		return fmt.Errorf("路径为空")
	}
	if err := rejectDangerous(target); err != nil {
		return err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		abs = target
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		parent := filepath.Dir(abs)
		if _, e := os.Stat(parent); e != nil {
			return fmt.Errorf("路径不存在: %s", target)
		}
		return ShellOpen(parent, "")
	}
	if info.IsDir() {
		return ShellOpen(abs, "")
	}
	return revealFile(abs)
}

// CommandOutcome 描述一次「等待其结束」的命令执行结果。
type CommandOutcome struct {
	Stdout   string // 已捕获的标准输出（超上限部分被丢弃）
	Stderr   string // 已捕获的标准错误输出
	ExitCode int    // 进程退出码；启动失败或超时未退出为 -1
	TimedOut bool   // 等待超时且进程仍未退出（未杀进程，输出为部分快照）
	Err      error  // 启动失败等错误；非 nil 时退出码无意义
}

// commandOutputCap 单路输出捕获上限（字节）。
// 兜底防撑爆：dir C:\ / type 大文件这类命令输出可达数 MB，而结果串最终只会
// 用前几百字——但若不设上限，全量会先整个灌进内存。
const commandOutputCap = 8 << 10

// RunCommandCapture 执行一条命令行（按 argv 拆词，避免直接交给 shell 解释导致
// 注入；需要管道/重定向等 shell 特性时显式写 cmd /c "..."），等待其结束并捕获
// stdout/stderr 与退出码。用于 ls / dir / git status 这类要拿输出的命令
// （定时任务的 command 动作走这里，输出会进执行历史与通知）。
//
// timeout 到点仍未退出时不杀进程：用户完全可能用命令启动常驻程序，杀掉过于
// 意外。此时返回 TimedOut 与已捕获的部分输出；进程由内部 Wait 协程继续回收，
// 输出缓冲带锁，超时快照与后续写入不存在数据竞争。
func RunCommandCapture(command, workingDir string, timeout time.Duration) CommandOutcome {
	command = strings.TrimSpace(command)
	argList := splitArgs(command)
	if len(argList) == 0 {
		return CommandOutcome{ExitCode: -1, Err: fmt.Errorf("命令内容为空")}
	}
	cmd := exec.Command(argList[0], argList[1:]...)
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	stdout, stderr := newCappedBuffer(commandOutputCap), newCappedBuffer(commandOutputCap)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// 隐藏子进程控制台窗口，避免定时命令弹黑框。
	sysutil.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return CommandOutcome{ExitCode: -1, Err: err}
	}
	// Wait 必须放在协程里：超时路径不能阻塞调用方，且进程结束后仍需有人回收句柄。
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return CommandOutcome{
			Stdout:   decodeConsoleOutput(stdout.String()),
			Stderr:   decodeConsoleOutput(stderr.String()),
			ExitCode: exitCodeOf(err),
		}
	case <-time.After(timeout):
		return CommandOutcome{
			Stdout:   decodeConsoleOutput(stdout.String()),
			Stderr:   decodeConsoleOutput(stderr.String()),
			ExitCode: -1,
			TimedOut: true,
		}
	}
}

// exitCodeOf 从 cmd.Wait() 的错误里取退出码；被信号杀死等取不到时为 -1。
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// newCappedBuffer 构造带上限的输出缓冲。max<=0 视为不设限（仅测试用）。
func newCappedBuffer(max int) cappedBuffer { return cappedBuffer{max: max} }

// cappedBuffer 带上限的并发安全输出缓冲。
// 并发来源：exec.Cmd 会为 stdout/stderr 各起一个管道拷贝协程，超时未 Wait 的
// 路径下这些协程仍可能短暂在写，故必须加锁；按字节封顶防止输出撑爆内存。
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 || b.buf.Len() < b.max {
		room := b.max - b.buf.Len()
		if b.max <= 0 || len(p) <= room {
			b.buf.Write(p)
		} else {
			b.buf.Write(p[:room])
		}
	}
	// 必须回报 len(p)：exec 的拷贝协程把短写当错误，会污染 Wait 的返回。
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// splitArgs 按空格拆词，支持双引号包裹保留空格。
func splitArgs(args string) []string {
	var result []string
	var current []byte
	inQuotes := false
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == ' ' && !inQuotes:
			if len(current) > 0 {
				result = append(result, string(current))
				current = current[:0]
			}
		default:
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		result = append(result, string(current))
	}
	return result
}
