package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogfToFile(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	I("info 测试 %d", 42)
	W("warn 测试 %s", "w")
	E("error 测试")
	Close()

	day := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(dir, "quickdock-"+day+".log"))
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	s := string(data)
	for _, want := range []string{"[I] info 测试 42", "[W] warn 测试 w", "[E] error 测试"} {
		if !strings.Contains(s, want) {
			t.Errorf("日志缺少 %q，实际:\n%s", want, s)
		}
	}
}

func TestInitIdempotent(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	Init(dir)         // 二次调用不炸
	Init(dir+"/sub")  // 换目录重新初始化
	I("x")
	Close()
}