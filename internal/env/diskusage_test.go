package env

import (
	"os"
	"path/filepath"
	"testing"
)

// DiskUsage 必须纳入 Everything 便携版：它平铺在 runtime/everything 根目录（无版本子目录），
// 曾经因「只数版本子目录」而被整行跳过，用户在磁盘占用面板里完全看不到它。
func TestDiskUsageEverythingFlatLayout(t *testing.T) {
	root := t.TempDir() // 即 <数据目录>/runtime
	rtDir := filepath.Join(root, string(RuntimeEverything))
	if err := os.MkdirAll(rtDir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(rtDir, name), make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("Everything.exe", 2048)
	write("es.exe", 1024)
	write("Everything.ini", 512)

	report := diskUsageUnder(root)
	var ev *RuntimeDiskUsage
	for i := range report.Runtimes {
		if report.Runtimes[i].Runtime == string(RuntimeEverything) {
			ev = &report.Runtimes[i]
			break
		}
	}
	if ev == nil {
		t.Fatal("平铺布局的 Everything 未出现在磁盘占用报告中（被 0 值行跳过？）")
	}
	if want := int64(2048 + 1024 + 512); ev.Bytes != want {
		t.Fatalf("Everything 占用 = %d，期望 %d", ev.Bytes, want)
	}
	if len(ev.Versions) != 1 {
		t.Fatalf("Everything 应有 1 个条目（平铺根级文件），实际 %d 个", len(ev.Versions))
	}
	if ev.Versions[0].Version == "" {
		t.Error("Everything 条目版本号不应为空（unknown 也好过空）")
	}
	if report.TotalBytes < ev.Bytes {
		t.Errorf("总计 %d 小于 Everything 单项 %d", report.TotalBytes, ev.Bytes)
	}
}

// 常规运行时（版本子目录布局）统计不受 Everything 分支影响：大小按版本明细汇总。
func TestDiskUsageVersionSubdirLayout(t *testing.T) {
	root := t.TempDir()
	verDir := filepath.Join(root, "redis", "7.0.11")
	if err := os.MkdirAll(verDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "redis.dat"), make([]byte, 4096), 0644); err != nil {
		t.Fatal(err)
	}
	// 版本子目录之外的文件（links.json 之类）不计入。
	if err := os.WriteFile(filepath.Join(root, "redis", "links.json"), make([]byte, 9999), 0644); err != nil {
		t.Fatal(err)
	}

	report := diskUsageUnder(root)
	if len(report.Runtimes) != 1 || report.Runtimes[0].Runtime != "redis" {
		t.Fatalf("应只有 redis 一项，实际 %+v", report.Runtimes)
	}
	if report.Runtimes[0].Bytes != 4096 {
		t.Fatalf("redis 占用 = %d，期望 4096（不含 links.json）", report.Runtimes[0].Bytes)
	}
	if len(report.Runtimes[0].Versions) != 1 || report.Runtimes[0].Versions[0].Version != "7.0.11" {
		t.Fatalf("版本明细异常: %+v", report.Runtimes[0].Versions)
	}
}

// 空根目录返回空报告而非报错（面板据此显示「暂无数据」）。
func TestDiskUsageEmptyRoot(t *testing.T) {
	report := diskUsageUnder(filepath.Join(t.TempDir(), "not-exist"))
	if len(report.Runtimes) != 0 || report.TotalBytes != 0 {
		t.Fatalf("空根应返回空报告，实际 %+v", report)
	}
}

// dirSize 递归统计目录内所有常规文件字节数，读不到的条目跳过不致命。
func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.bin"), make([]byte, 250), 0644); err != nil {
		t.Fatal(err)
	}
	if got := dirSize(dir); got != 350 {
		t.Fatalf("dirSize = %d，期望 350", got)
	}
	if got := dirSize(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("不存在目录应返回 0，实际 %d", got)
	}
}
