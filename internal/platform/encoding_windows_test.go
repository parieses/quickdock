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
			if got := DecodeConsoleText(c.in); got != c.want {
				t.Errorf("解码结果不符:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}

	// 不出现 U+FFFD：乱码正是这个功能要消灭的东西
	if got := DecodeConsoleText("\xd6\xd0\xce\xc4"); strings.ContainsRune(got, '\uFFFD') {
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

// TestDecodeConsoleTextMixed 同一条管道里混着两种编码时的行为。
//
// 这是真实场景：`go vet ./... 2>&1` 里 go 自己写 UTF-8，而 cmd.exe 拼错命令时的
// 「'head' 不是内部或外部命令」是 GBK。整块按 GBK 解会把 UTF-8 那半边弄成乱码，
// 与要修的问题正好对称——所以按行判，只动非 UTF-8 的行。
func TestDecodeConsoleTextMixed(t *testing.T) {
	if consoleCP() != 936 {
		t.Skipf("系统控制台代码页为 %d，非 936(GBK)，跳过", consoleCP())
	}

	// UTF-8 行 + GBK 行：前者一个字节都不该变，后者要解成中文。
	in := "ok: 中文正常行\n" + "'head' \xb2\xbb\xca\xc7\xc4\xda\xb2\xbf\xbb\xf2\xcd\xe2\xb2\xbf\xc3\xfc\xc1\xee\r\n"
	want := "ok: 中文正常行\n'head' 不是内部或外部命令\r\n"
	if got := DecodeConsoleText(in); got != want {
		t.Errorf("混编解码不符:\n got: %q\nwant: %q", got, want)
	}

	// 整段是合法 UTF-8 时原样返回：这是 npm / node / go 这些程序的常态，
	// 一个字节都不能被「顺手转一下」。
	pure := "构建完成 ✓ 用时 3.2s\n"
	if got := DecodeConsoleText(pure); got != pure {
		t.Errorf("合法 UTF-8 不应被改写: %q", got)
	}
}

// TestDecodeConsoleBytesBOMAndASCII 不依赖系统代码页的边界：BOM 声明优先、纯 ASCII 原样。
func TestDecodeConsoleBytesBOMAndASCII(t *testing.T) {
	if got := string(DecodeConsoleBytes([]byte{0xEF, 0xBB, 0xBF, 'o', 'k', '\r', '\n'})); got != "ok\r\n" {
		t.Errorf("带 UTF-8 BOM 的输出应剥掉 BOM 后原样返回，实际 %q", got)
	}
	if got := string(DecodeConsoleBytes([]byte("12345\r\n"))); got != "12345\r\n" {
		t.Errorf("纯 ASCII 不应被改写，实际 %q", got)
	}
	if got := DecodeConsoleBytes(nil); got != nil {
		t.Errorf("空输入应原样返回 nil，实际 %v", got)
	}
}

// TestDecodeConsoleBytesESChinese —— 文件搜索页中文乱码的回归防线。
// es.exe 1.1.0.38 的结果按 936(GBK) 写字节（实测：默认输出与 -json 都是，
// 命令行也没有 -utf8 开关）。整块解码后必须是可直接显示、可直接 os.Stat 的中文路径。
func TestDecodeConsoleBytesESChinese(t *testing.T) {
	if consoleCP() != 936 {
		t.Skipf("系统控制台代码页为 %d，非 936(GBK)，跳过中文解码断言", consoleCP())
	}
	// 本机 es.exe -n 5 "法老之鹰六杀.mp4" 的真实 stdout（GBK 字节 + CRLF）。
	raw := []byte("H:\\Users\\Mr.liang\\Videos\\\xd3\xce\xcf\xb7\\\xb7\xa8\xc0\xcf\xd6\xae\xd3\xa5\xc1\xf9\xc9\xb1.mp4\r\n")
	want := "H:\\Users\\Mr.liang\\Videos\\游戏\\法老之鹰六杀.mp4\r\n"
	got := string(DecodeConsoleBytes(raw))
	if got != want {
		t.Errorf("es.exe 输出解码不符:\n got: %q\nwant: %q", got, want)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("解码结果不应含替换字符: %q", got)
	}
}
