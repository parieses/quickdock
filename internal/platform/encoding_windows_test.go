//go:build windows

package platform

import (
	"strings"
	"testing"
	"time"
)

// TestDecodeConsoleOutputGBK 验证中文 Windows 下控制台输出的 GBK→UTF-8 解码。
// 非 936 代码页的系统上跳过（那是英文/其它语言环境，字节本就不是 GBK）。
func TestDecodeConsoleOutputGBK(t *testing.T) {
	if consoleCP() != 936 {
		t.Skipf("系统控制台代码页为 %d，非 936(GBK)，跳过中文解码断言", consoleCP())
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"纯 GBK 中文", "\xd6\xd0\xce\xc4\xb2\xe2\xca\xd4", "中文测试"},
		{"ASCII 混合", "AB\xd6\xd0", "AB中"},
		{"尾部残缺序列回退边界", "\xd6\xd0\xce\xc4\xd6", "中文"},
		{"纯 ASCII 原样", "hello-qd\r\n", "hello-qd\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeConsoleOutput(c.in); got != c.want {
				t.Errorf("解码结果不符:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}

	// 不出现 U+FFFD：乱码正是这个功能要消灭的东西
	if got := decodeConsoleOutput("\xd6\xd0\xce\xc4"); strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("解码结果不应含替换字符: %q", got)
	}
}

// TestRunCommandCaptureDecodesChinese 走完整执行链验证：cmd 的中文输出经
// RunCommandCapture 回来应是 UTF-8 文本，而不是一坨 U+FFFD。
// 这正是「定时任务跑 dir，执行结果是乱码」的回归防线。
func TestRunCommandCaptureDecodesChinese(t *testing.T) {
	if consoleCP() != 936 {
		t.Skipf("系统控制台代码页为 %d，非 936(GBK)，跳过", consoleCP())
	}
	out := RunCommandCapture(shellCmd("echo 中文测试-qd"), "", 10*time.Second)
	if out.ExitCode != 0 {
		t.Fatalf("退出码应为 0，实为 %d", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "中文测试-qd") {
		t.Errorf("中文输出应解码为 UTF-8: %q", out.Stdout)
	}
	if strings.ContainsRune(out.Stdout, '\uFFFD') {
		t.Errorf("不应出现替换字符（乱码）: %q", out.Stdout)
	}
}
