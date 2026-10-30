package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// 一次性命令执行（非交互）。
//
// 与 PTY 会话的区别：这里不需要终端，服务器直接跑命令、把 stdout/stderr 交回来。
// 用途是「连上机器看一眼」这类场景（查 nginx 日志、看负载、部署脚本），
// 也是在没有前端终端的情况下验证整条凭据链路的唯一手段。
var (
	// ErrExecTimeout 命令在超时/取消前没有结束，连接已被清理。
	ErrExecTimeout = errors.New("sshx: 命令执行超时")
	// ErrExecAborted 服务器在命令结束前关闭了通道，拿不到退出码。
	ErrExecAborted = errors.New("sshx: 命令被中断，服务器未返回退出状态")
	// ErrEmptyCommand 空命令。
	ErrEmptyCommand = errors.New("sshx: 命令不能为空")
)

// maxExecOutput 单条流（stdout / stderr 各自）保留的最大字节数。
//
// 必须有上限：`cat 大文件`、`yes`、误操作跑个死循环，都能瞬间把内存吃干净。
// 超出部分继续读但丢弃（不能让管道堵住，否则远端命令会卡在写阻塞上）。
const maxExecOutput = 1 << 20 // 1 MiB

// ExecResult 一次命令执行的结果。
//
// 注意 ExitCode 与 error 的分工：命令以非零码退出是正常结果（`grep` 没匹配返回 1），
// 不当作 error；error 只表示「命令根本没跑完」——超时、断连、会话建立失败。
type ExecResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	Truncated bool // 输出超过上限被截断
}

// Combined 把 stdout 与 stderr 合并，便于直接展示在单块文本框里。
func (r ExecResult) Combined() string {
	if r.Stderr == "" {
		return r.Stdout
	}
	if r.Stdout == "" {
		return r.Stderr
	}
	return r.Stdout + r.Stderr
}

// Exec 在远程执行一条命令并等待结束。
//
// ctx 取消或超时会关闭会话（远端命令随之被终止），返回带 ErrExecTimeout 的错误，
// 同时带回已经收到的部分输出，便于用户看清它卡在哪一步。
func (c *Client) Exec(ctx context.Context, cmd string) (ExecResult, error) {
	if strings.TrimSpace(cmd) == "" {
		return ExecResult{}, ErrEmptyCommand
	}
	if !c.Alive() {
		return ExecResult{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return ExecResult{}, fmt.Errorf("%w: %v", ErrExecTimeout, err)
	}

	sess, err := c.conn.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("%w: 创建会话失败: %v", ErrClosed, err)
	}
	defer sess.Close()

	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxExecOutput, maxExecOutput
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	start := time.Now()
	// 用 Start + Wait 而不是 Run：Run 是阻塞的，中途没法响应 ctx 取消。
	if err := sess.Start(cmd); err != nil {
		return ExecResult{}, fmt.Errorf("sshx: 启动命令失败: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()

	finish := func(waitErr error) (ExecResult, error) {
		res := ExecResult{
			Stdout:    stdout.String(),
			Stderr:    stderr.String(),
			Duration:  time.Since(start),
			Truncated: stdout.truncated || stderr.truncated,
		}
		var exitErr *ssh.ExitError
		switch {
		case waitErr == nil:
			res.ExitCode = 0
			return res, nil
		case errors.As(waitErr, &exitErr):
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		case errors.Is(waitErr, io.EOF):
			res.ExitCode = -1
			return res, fmt.Errorf("%w: 连接在命令结束前断开", ErrClosed)
		default:
			var missing *ssh.ExitMissingError
			if errors.As(waitErr, &missing) {
				res.ExitCode = -1
				return res, ErrExecAborted
			}
			res.ExitCode = -1
			return res, fmt.Errorf("sshx: 执行命令失败: %w", waitErr)
		}
	}

	select {
	case waitErr := <-done:
		return finish(waitErr)
	case <-ctx.Done():
		// 关闭通道，远端进程收到 HUP；不发 Signal：未分配 PTY 的会话上
		// 多数 sshd 会直接拒绝 signal 请求，白等一轮。
		_ = sess.Close()
		// 必须等 Wait 返回再读 buffer：ssh 内部仍有 goroutine 在往里写，
		// 提前读就是数据竞争。
		<-done
		res, _ := finish(nil)
		res.ExitCode = -1
		return res, fmt.Errorf("%w: %v", ErrExecTimeout, ctx.Err())
	}
}

// limitedBuffer 带容量上限的写入缓冲。
//
// 关键在「超限后仍然返回 len(p)」：ssh 的拷贝循环把短写当成错误处理，
// 返回少了会中断整条流，连后续的正常输出也收不到了。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.limit <= 0 {
		return len(p), nil
	}
	remain := l.limit - l.buf.Len()
	if remain <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		l.buf.Write(p[:remain])
		l.truncated = true
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) String() string { return l.buf.String() }
