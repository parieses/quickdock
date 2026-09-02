package env

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExtractStripsSingleTopDir 验证单一顶层目录（node/go/nginx 归档）被剥离。
func TestExtractStripsSingleTopDir(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "node.zip")
	writeZip(t, zipPath, map[string][]byte{
		"node-v18/foo.txt":     []byte("a"),
		"node-v18/bin/bar.txt": []byte("b"),
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(zipPath, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "foo.txt")); err != nil {
		t.Fatalf("expected stripped foo.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "bar.txt")); err != nil {
		t.Fatalf("expected stripped bin/bar.txt: %v", err)
	}
	// 顶层目录本身不应残留
	if _, err := os.Stat(filepath.Join(dest, "node-v18")); !os.IsNotExist(err) {
		t.Fatal("top dir should be stripped")
	}
}

// TestExtractFlatPreserves 验证扁平归档（PHP/redis 直接铺在根）保留原结构。
func TestExtractFlatPreserves(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "php.zip")
	writeZip(t, zipPath, map[string][]byte{
		"php.exe":      []byte("PE"),
		"ext/xyz.dll":  []byte("DLL"),
		"lib/zend.dll": []byte("Z"),
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(zipPath, dest); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"php.exe", "ext/xyz.dll", "lib/zend.dll"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(p))); err != nil {
			t.Fatalf("expected %s preserved: %v", p, err)
		}
	}
}

// TestExtractRejectsPathTraversal 验证路径穿越条目被拒绝（防 zip slip）。
// 注意：zip 写入器会把 "../evil.txt" 归一化为 "evil.txt"，因此这里用
// "a/../../evil.txt" 这种含 ".." 且无法被简单归一化的名字，确保穿越意图保留。
func TestExtractRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	writeZip(t, zipPath, map[string][]byte{
		"a/../../evil.txt": []byte("pwned"),
	})
	dest := filepath.Join(dir, "out")
	if err := Extract(zipPath, dest); err == nil {
		t.Fatal("expected error for path traversal entry")
	}
	// 目标目录不应被写出越界文件
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("path traversal produced out-of-bounds file")
	}
}

// TestSafeJoinGuard 直接验证 safeJoin 对越界路径的拒绝与对合法路径的接受。
func TestSafeJoinGuard(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := safeJoin(dest, "../../evil.txt"); err == nil {
		t.Fatal("safeJoin should reject escaping path")
	}
	if _, err := safeJoin(dest, "sub/file.txt"); err != nil {
		t.Fatalf("safeJoin should accept in-bounds path: %v", err)
	}
	if _, err := safeJoin(dest, ""); err == nil {
		t.Fatal("safeJoin should reject empty path")
	}
}

// TestExtractTarGzStripsTop 验证 .tar.gz 同样剥离单一顶层目录。
func TestExtractTarGzStripsTop(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "go.tar.gz")
	f, err := os.Create(tgz)
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	entries := []struct {
		name string
		body string
	}{
		{"go-1.21/src/main.go", "package main"},
		{"go-1.21/README", "readme"},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	// 额外加一个目录条目
	_ = tw.WriteHeader(&tar.Header{Name: "go-1.21/", Mode: 0755, Typeflag: tar.TypeDir})
	tw.Close()
	gw.Close()
	f.Close()

	dest := filepath.Join(dir, "out")
	if err := Extract(tgz, dest); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"src/main.go", "README"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(p))); err != nil {
			t.Fatalf("expected %s after strip: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "go-1.21")); !os.IsNotExist(err) {
		t.Fatal("top dir should be stripped for tar.gz")
	}
}
