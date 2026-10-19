package dsh

import (
	"strings"
	"testing"
)

// 回归测试：newLogScanner 曾误写成递归调用自身（sc := newLogScanner(r)）。
// 任何读取子进程输出的路径（dsh 启动、npm 安装 dsh/pnpm、插件安装/更新/回滚）都会
// 因无限递归触发 "fatal error: stack overflow"——Go 的致命错误不可 recover，
// 宿进程直接退出（用户侧表现就是「点一下 DeepSeek Harness 就闪退」）。
func TestNewLogScannerReadsLongLine(t *testing.T) {
	long := strings.Repeat("x", 200*1024) // 超过 bufio.Scanner 默认 64KB 单行上限
	in := "first\n" + long + "\nlast"

	sc := newLogScanner(strings.NewReader(in))
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("期望 3 行，实际 %d 行", len(lines))
	}
	if len(lines[1]) != len(long) {
		t.Fatalf("超长行被截断: 期望 %d 字节，实际 %d", len(long), len(lines[1]))
	}
}
