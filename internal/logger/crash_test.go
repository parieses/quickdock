package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCapturePanicWritesAndRethrows 验证 CapturePanic 落盘现场后原样重抛：
// 既留下排查线索，又不改变接入前「崩溃即崩溃」的语义。
func TestCapturePanicWritesAndRethrows(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	defer Close()

	var rethrown any
	func() {
		defer func() { rethrown = recover() }()
		func() {
			defer CapturePanic("unit:test")
			panic("boom")
		}()
	}()
	if rethrown == nil {
		t.Fatal("CapturePanic 应当重新抛出 panic")
	}

	files, err := os.ReadDir(filepath.Join(dir, "crash"))
	if err != nil {
		t.Fatalf("崩溃目录未创建: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("期望 1 个崩溃文件，实际 %d", len(files))
	}
	s := readCrashFile(t, dir, files[0].Name())
	for _, want := range []string{"panic: boom", "label: unit:test", "goroutine"} {
		if !strings.Contains(s, want) {
			t.Errorf("崩溃文件缺少 %q，实际:\n%s", want, s)
		}
	}
}

// TestRecoverToLogNoRethrow 验证 RecoverToLog 记录后不重抛——
// 后台常驻协程靠它做到「一次异常不拖垮宿主」。
func TestRecoverToLogNoRethrow(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	defer Close()

	// 两个要点：
	// 1) recover() 只有被「直接」延迟的函数调用才有效，所以 RecoverToLog 必须直接 defer，
	//    包一层匿名函数会让它失效（那层匿名函数才是 deferred 函数）。
	// 2) panic 被消化后函数不会从 panic 点继续，标志位只能放在后注册、先执行的 defer 里。
	recovered := false
	func() {
		defer func() { recovered = true }()
		defer RecoverToLog("unit:goroutine")
		panic("inner")
	}()
	if !recovered {
		t.Fatal("RecoverToLog 不应重新抛出 panic")
	}

	files, err := os.ReadDir(filepath.Join(dir, "crash"))
	if err != nil {
		t.Fatalf("崩溃目录未创建: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("期望 1 个崩溃文件，实际 %d", len(files))
	}
	if s := readCrashFile(t, dir, files[0].Name()); !strings.Contains(s, "inner") {
		t.Errorf("崩溃文件缺少 panic 信息:\n%s", s)
	}
}

// TestReportFrontend 验证前端异常落盘（js- 前缀，供诊断页按类型分色展示）。
func TestReportFrontend(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	defer Close()

	ReportFrontend("unhandledrejection", "boom from js", "at foo (bar.js:1:2)", "http://localhost/x")

	files, err := os.ReadDir(filepath.Join(dir, "crash"))
	if err != nil {
		t.Fatalf("崩溃目录未创建: %v", err)
	}
	if len(files) != 1 || !strings.HasPrefix(files[0].Name(), "js-") {
		t.Fatalf("期望 1 个 js- 前缀文件，实际: %+v", files)
	}
	s := readCrashFile(t, dir, files[0].Name())
	for _, want := range []string{"boom from js", "at foo (bar.js:1:2)", "http://localhost/x"} {
		if !strings.Contains(s, want) {
			t.Errorf("前端异常文件缺少 %q:\n%s", want, s)
		}
	}
}

// TestPruneCrashKeepsLatest 验证崩溃文件只保留最近 maxCrashFiles 个（按修改时间）。
func TestPruneCrashKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxCrashFiles+5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("panic-x-%02d.log", i))
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
		mt := time.Now().Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatalf("设置修改时间失败: %v", err)
		}
	}
	pruneCrash(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	if len(entries) != maxCrashFiles {
		t.Errorf("期望保留 %d 个，实际 %d", maxCrashFiles, len(entries))
	}
	// 最旧的 5 个应被清理
	if _, err := os.Stat(filepath.Join(dir, "panic-x-00.log")); err == nil {
		t.Error("最旧的崩溃文件应被清理")
	}
}

func readCrashFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "crash", name))
	if err != nil {
		t.Fatalf("读取崩溃文件失败: %v", err)
	}
	return string(data)
}
