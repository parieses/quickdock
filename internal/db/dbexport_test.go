package db

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExportDatabaseFile 验证数据库导出的一致性。
//
// 关键场景：源库处于 WAL 模式且有未 checkpoint 的写入（数据在 -wal 里）。
// 这时逐字节复制主库文件会丢事务；VACUUM INTO 由 SQLite 自己保证快照一致。
func TestExportDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")

	d, err := Open(src)
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	if err := d.ExecuteParams("INSERT INTO app_state (key, value) VALUES (?, ?)", []interface{}{"theme", "dark"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 故意不 Close：保持 WAL 模式与打开状态，模拟「迁移时数据库正被占用」的真实场景。

	if err := ExportDatabaseFile(src, dst); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("导出文件不存在: %v", err)
	}

	// 导出出的库必须能打开，且含源库的数据。
	out, err := Open(dst)
	if err != nil {
		t.Fatalf("打开导出库失败: %v", err)
	}
	defer out.Close()
	v, err := out.GetValue("theme")
	if err != nil {
		t.Fatalf("读取导出数据失败: %v", err)
	}
	if v != "dark" {
		t.Errorf("导出数据不正确: got %q, want %q", v, "dark")
	}

	// 收尾：释放源库句柄，否则 TempDir 清理会因文件被占用而失败。
	_ = d.Close()
}

// TestExportDatabaseFileMissingSource 源库不存在时应静默成功（幂等）。
func TestExportDatabaseFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := ExportDatabaseFile(filepath.Join(dir, "nope.db"), filepath.Join(dir, "out.db")); err != nil {
		t.Fatalf("源库不存在时应返回 nil, 实际: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.db")); !os.IsNotExist(err) {
		t.Errorf("源库不存在时不应产生导出文件")
	}
}

// TestExportDatabaseFileOverwrites 目标已存在时应被覆盖。
func TestExportDatabaseFileOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")

	d, err := Open(src)
	if err != nil {
		t.Fatalf("打开源库失败: %v", err)
	}
	if err := d.ExecuteParams("INSERT INTO app_state (key, value) VALUES (?, ?)", []interface{}{"theme", "light"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 预置一个陈旧的导出文件。
	if err := os.WriteFile(dst, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ExportDatabaseFile(src, dst); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	out, err := Open(dst)
	if err != nil {
		t.Fatalf("打开导出库失败: %v", err)
	}
	defer out.Close()
	v, _ := out.GetValue("theme")
	if v != "light" {
		t.Errorf("导出未覆盖旧文件: got %q, want %q", v, "light")
	}
	_ = d.Close()
}
