package platform

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// shellCmd 包一层平台 shell，使重定向（1>&2）等写法可测。
// 注意 splitArgs 只认双引号，故这里也必须用双引号包裹。
func shellCmd(body string) string {
	if runtime.GOOS == "windows" {
		return `cmd /c "` + body + `"`
	}
	return `sh -c "` + body + `"`
}

func TestRunCommandCapture(t *testing.T) {
	t.Run("成功并捕获 stdout 与退出码", func(t *testing.T) {
		out := RunCommandCapture(shellCmd("echo hello-qd"), "", 10*time.Second)
		if out.Err != nil {
			t.Fatalf("不应报错: %v", out.Err)
		}
		if out.TimedOut {
			t.Error("不应超时")
		}
		if out.ExitCode != 0 {
			t.Errorf("退出码应为 0，实为 %d", out.ExitCode)
		}
		if !strings.Contains(out.Stdout, "hello-qd") {
			t.Errorf("stdout 应含输出: %q", out.Stdout)
		}
	})

	t.Run("非零退出码", func(t *testing.T) {
		out := RunCommandCapture(shellCmd("exit 3"), "", 10*time.Second)
		if out.Err != nil {
			t.Fatalf("非零退出不是启动错误: %v", out.Err)
		}
		if out.ExitCode != 3 {
			t.Errorf("退出码应为 3，实为 %d", out.ExitCode)
		}
	})

	t.Run("捕获 stderr", func(t *testing.T) {
		out := RunCommandCapture(shellCmd("echo boom-qd 1>&2"), "", 10*time.Second)
		if out.ExitCode != 0 {
			t.Errorf("退出码应为 0，实为 %d", out.ExitCode)
		}
		if !strings.Contains(out.Stderr, "boom-qd") {
			t.Errorf("stderr 应被捕获: %q（stdout=%q）", out.Stderr, out.Stdout)
		}
	})

	t.Run("工作目录生效", func(t *testing.T) {
		dir := t.TempDir()
		body := "cd"
		if runtime.GOOS != "windows" {
			body = "pwd"
		}
		out := RunCommandCapture(shellCmd(body), dir, 10*time.Second)
		if out.ExitCode != 0 {
			t.Fatalf("退出码应为 0，实为 %d", out.ExitCode)
		}
		// macOS 上 TempDir 经 /private/var 软链，物理路径与逻辑路径不同，
		// 故只比对末级目录名。
		if want := filepath.Base(dir); !strings.Contains(out.Stdout, want) {
			t.Errorf("应运行于工作目录 %s: %q", dir, out.Stdout)
		}
	})

	t.Run("超时不杀进程且尽快返回", func(t *testing.T) {
		body := "ping -n 6 127.0.0.1" // 约 5 秒
		if runtime.GOOS != "windows" {
			body = "sleep 5"
		}
		start := time.Now()
		out := RunCommandCapture(shellCmd(body), "", 400*time.Millisecond)
		elapsed := time.Since(start)
		if !out.TimedOut {
			t.Error("应触发超时")
		}
		if out.ExitCode != -1 {
			t.Errorf("超时未退出时退出码应为 -1，实为 %d", out.ExitCode)
		}
		if elapsed > 3*time.Second {
			t.Errorf("超时路径应尽快返回，实耗时 %v", elapsed)
		}
	})

	t.Run("启动失败", func(t *testing.T) {
		out := RunCommandCapture("qd-no-such-binary-xyz", "", 5*time.Second)
		if out.Err == nil {
			t.Error("启动不存在的命令应报错")
		}
		if out.ExitCode != -1 {
			t.Errorf("启动失败时退出码应为 -1，实为 %d", out.ExitCode)
		}
	})

	t.Run("空命令", func(t *testing.T) {
		if out := RunCommandCapture("   ", "", 5*time.Second); out.Err == nil {
			t.Error("空命令应报错")
		}
	})
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	b.max = 10

	full := "0123456789abcdef"
	n, err := b.Write([]byte(full))
	if err != nil || n != len(full) {
		t.Fatalf("Write 必须回报完整长度（exec 视短写为错误）: n=%d err=%v", n, err)
	}
	if got := b.String(); got != "0123456789" {
		t.Errorf("应按上限封顶: %q", got)
	}
	// 超上限后继续写不应改变内容（封顶幂等）
	if _, err := b.Write([]byte("xyz")); err != nil {
		t.Fatalf("超上限写入不应报错: %v", err)
	}
	if got := b.String(); got != "0123456789" {
		t.Errorf("超上限后内容不应变化: %q", got)
	}
}

func TestNewCappedBufferUnlimited(t *testing.T) {
	b := newCappedBuffer(0)
	if _, err := b.Write([]byte("不限长")); err != nil {
		t.Fatalf("max<=0 应视为不设限: %v", err)
	}
	if got := b.String(); got != "不限长" {
		t.Errorf("不设限时应完整保留: %q", got)
	}
}
