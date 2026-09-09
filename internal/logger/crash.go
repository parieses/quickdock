package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// 崩溃与前端异常收集。
//
// 主日志 quickdock-<date>.log 混写所有级别，排查「某次闪退 / 白屏」时很难定位到现场。
// 这里把 panic 与前端 JS 异常单独落到 <logDir>/crash/，一次问题一个文件，
// 只保留最近 maxCrashFiles 个，避免长期累积占盘。

const maxCrashFiles = 20

// CrashDir 返回崩溃日志目录；未 Init 时返回空串。
func CrashDir() string {
	mu.Lock()
	defer mu.Unlock()
	if logDir == "" {
		return ""
	}
	return filepath.Join(logDir, "crash")
}

// LogDir 返回当前日志目录（未初始化时为空），供绑定层列举/读取日志文件。
func LogDir() string {
	mu.Lock()
	defer mu.Unlock()
	return logDir
}

// CapturePanic 以 defer 方式调用：捕获 panic、落盘完整堆栈，然后原样重新抛出。
// 不吞异常——崩溃语义与接入前一致，只是多留一份现场。
//
//	func risky() {
//	    defer logger.CapturePanic("env:watchdog")
//	    ...
//	}
func CapturePanic(label string) {
	r := recover()
	if r == nil {
		return
	}
	writeCrash("panic", label, fmt.Sprintf("panic: %v", r), string(debug.Stack()))
	panic(r)
}

// RecoverToLog 以 defer 方式调用：记录 panic 后**不**重新抛出。
// 适用于 goroutine 内部（看门狗、巡检循环等），避免单个后台协程把宿主带崩。
func RecoverToLog(label string) {
	if r := recover(); r != nil {
		writeCrash("panic", label, fmt.Sprintf("panic: %v", r), string(debug.Stack()))
	}
}

// CapturePanicInGo 包装将在 goroutine 中运行的函数：panic 落盘后不再上抛。
// 等价于在函数首行写 defer logger.RecoverToLog(label)。
func CapturePanicInGo(label string, fn func()) func() {
	return func() {
		defer RecoverToLog(label)
		fn()
	}
}

// ReportFrontend 记录前端 JS 异常（window.onerror / unhandledrejection 上报）。
func ReportFrontend(kind, message, stack, url string) {
	writeCrash("js", kind, message, stack+"\n\nurl: "+url)
}

func writeCrash(kind, label, message, detail string) {
	mu.Lock()
	dir := ""
	if logDir != "" {
		dir = filepath.Join(logDir, "crash")
	}
	mu.Unlock()

	if dir == "" {
		// 未初始化：至少打到 stderr，避免现场完全丢失
		fmt.Fprintf(os.Stderr, "QuickDock crash[%s/%s]: %s\n%s\n", kind, label, message, detail)
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	ts := time.Now().Format("20060102-150405.000")
	name := fmt.Sprintf("%s-%s-%s.log", kind, sanitizeFilePart(label), ts)
	body := fmt.Sprintf("time: %s\nkind: %s\nlabel: %s\nmessage: %s\n\n%s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), kind, label, message, detail)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		return
	}
	E("crash 已记录 kind=%s label=%s file=%s", kind, label, name)
	pruneCrash(dir)
}

// pruneCrash 只保留最近 maxCrashFiles 个崩溃文件（按修改时间倒序）。
func pruneCrash(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fi struct {
		name string
		t    time.Time
	}
	var list []fi
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		list = append(list, fi{e.Name(), info.ModTime()})
	}
	if len(list) <= maxCrashFiles {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })
	for _, x := range list[maxCrashFiles:] {
		_ = os.Remove(filepath.Join(dir, x.name))
	}
}

// sanitizeFilePart 把标签变成安全的文件名片段（label 可能含运行时名、插件 ID 等）。
func sanitizeFilePart(s string) string {
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "-", "<", "-", ">", "-", "|", "-", " ", "-")
	out := repl.Replace(s)
	if out == "" {
		return "unknown"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
