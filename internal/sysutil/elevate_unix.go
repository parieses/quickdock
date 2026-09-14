//go:build darwin || linux

package sysutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// IsElevated 当前进程是否以 root 运行（非 root 写 /etc/hosts 必被拒）。
func IsElevated() bool { return os.Geteuid() == 0 }

// RunElevated 以管理员身份启动 exe 并等待其结束。
//   - macOS：走 osascript 的 `with administrator privileges`，系统原生弹授权框，无需额外依赖；
//   - Linux：走 pkexec（polkit）；未安装则返回 ErrElevationUnsupported，
//     由调用方退化为「提示用户以 sudo 运行或手动改 /etc/hosts」。
func RunElevated(exe string, args []string, timeout time.Duration) error {
	var name string
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		name = "osascript"
		// 整条命令交给 shell 执行，故先做 POSIX 引用，再作为 AppleScript 字符串字面量转义。
		cmdline := shellJoin(append([]string{exe}, args...))
		argv = []string{"-e", "do shell script " + appleScriptQuote(cmdline) + " with administrator privileges"}
	default:
		p, err := exec.LookPath("pkexec")
		if err != nil {
			return fmt.Errorf("%w：未找到 pkexec（polkit）", ErrElevationUnsupported)
		}
		name = p
		argv = append([]string{exe}, args...)
	}

	cmd := exec.Command(name, argv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("提权启动失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// macOS 用户取消时 osascript 退出码 1 且 stderr 带 "User canceled."；
			// pkexec 被拒/取消固定返回 126。
			if ee.ExitCode() == 126 || strings.Contains(stderr.String(), "User cancel") {
				return ErrElevationCancelled
			}
			return fmt.Errorf("提权子进程失败（退出码 %d）", ee.ExitCode())
		}
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("提权进程在 %s 内未结束", timeout)
	}
}

// shellJoin 把参数拼成一条 POSIX shell 命令行：每段用单引号包裹，参数内部的单引号
// 按 POSIX 惯例替换为 引号闭合 + 反斜杠转义单引号 + 重新开引号 的四字符序列。
func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, "'"+strings.ReplaceAll(p, "'", `'\''`)+"'")
	}
	return strings.Join(out, " ")
}

// appleScriptQuote 把内容转义成 AppleScript 的双引号字符串字面量（转义 \ 与 "）。
func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
