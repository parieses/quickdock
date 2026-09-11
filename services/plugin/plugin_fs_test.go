package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDecodeFSContent(t *testing.T) {
	t.Run("默认 utf8", func(t *testing.T) {
		got, err := decodeFSContent("你好\n", "")
		if err != nil || string(got) != "你好\n" {
			t.Fatalf("got %q, err %v", got, err)
		}
	})

	t.Run("base64", func(t *testing.T) {
		got, err := decodeFSContent("5L2g5aW9", "base64")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "你好" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("非法 base64 报错", func(t *testing.T) {
		if _, err := decodeFSContent("!!!not-base64!!!", "base64"); err == nil {
			t.Fatal("应报错")
		}
	})

	t.Run("未知 encoding 报错", func(t *testing.T) {
		if _, err := decodeFSContent("x", "utf16"); err == nil {
			t.Fatal("应报错")
		}
	})
}

func TestFSReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")

	if err := fsWriteFile(p, []byte("hello 世界")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := fsReadFile(p)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got["encoding"] != "utf8" {
		t.Errorf("文本应识别为 utf8, got %v", got["encoding"])
	}
	if got["content"] != "hello 世界" {
		t.Errorf("内容不符: %v", got["content"])
	}
	if got["size"] != len("hello 世界") {
		t.Errorf("size 不符: %v", got["size"])
	}
}

// TestFSWriteFileAtomic 写入必须不留临时文件，也不破坏原文件。
func TestFSWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")

	if err := fsWriteFile(p, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := fsWriteFile(p, []byte("v2-longer")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2-longer" {
		t.Fatalf("覆盖写入结果不符: %q", data)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".qd-fs-*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("残留临时文件: %v", leftovers)
	}

	// 父目录不存在 → 报错且同样不留临时文件
	bad := filepath.Join(dir, "nope", "x.txt")
	if err := fsWriteFile(bad, []byte("x")); err == nil {
		t.Error("父目录不存在应报错")
	}
}

// TestFSWriteFilePreservesMode 覆盖写入不应重置原有权限位。
// Windows 上 Chmod 语义不完整，跳过。
func TestFSWriteFilePreservesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无 POSIX 权限位")
	}
	p := filepath.Join(t.TempDir(), "a.sh")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsWriteFile(p, []byte("y")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("权限位被改: got %v, want 0600", fi.Mode().Perm())
	}
}

func TestFSReadFileBinary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin.dat")
	if err := os.WriteFile(p, []byte{0x00, 0x01, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fsReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got["encoding"] != "base64" {
		t.Fatalf("含 NUL 的内容应转 base64, got %v", got["encoding"])
	}
	if got["content"] != "AAH/" {
		t.Errorf("base64 不符: %v", got["content"])
	}
}

// TestFSReadFileLimits 超限必须报错而不是截断——截断后写回会毁数据。
func TestFSReadFileLimits(t *testing.T) {
	dir := t.TempDir()

	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	// 稀疏文件：用 Truncate 造出超限大小，不实际占用磁盘
	if err := f.Truncate(pluginFSMaxRead + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := fsReadFile(big); err == nil {
		t.Error("超过读取上限应报错")
	}

	if _, err := fsReadFile(dir); err == nil {
		t.Error("目录不能按文件读取")
	}
	if _, err := fsReadFile(filepath.Join(dir, "ghost")); err == nil {
		t.Error("不存在的文件应报错")
	}
	if err := fsWriteFile(filepath.Join(dir, "x"), make([]byte, pluginFSMaxWrite+1)); err == nil {
		t.Error("超过写入上限应报错")
	}
}

func TestFSListDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	list, truncated, err := fsListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("不该截断")
	}
	if len(list) != 2 {
		t.Fatalf("条目数不符: %d", len(list))
	}
	byName := map[string]map[string]interface{}{}
	for _, e := range list {
		byName[e["name"].(string)] = e
	}
	if byName["a.txt"]["isDir"] != false || byName["a.txt"]["size"] != int64(3) {
		t.Errorf("文件条目不符: %v", byName["a.txt"])
	}
	if byName["sub"]["isDir"] != true {
		t.Errorf("目录条目不符: %v", byName["sub"])
	}
	if byName["a.txt"]["mtime"] == nil {
		t.Error("应带 mtime")
	}

	if _, _, err := fsListDir(filepath.Join(dir, "a.txt")); err == nil {
		t.Error("对文件列目录应报错")
	}
}

func TestFSListDirTruncates(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < pluginFSMaxList+3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%05d.txt", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, truncated, err := fsListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("应标记截断")
	}
	if len(list) != pluginFSMaxList {
		t.Errorf("应恰好返回 %d 条, got %d", pluginFSMaxList, len(list))
	}
}

func TestFSMove(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "a.txt")
	to := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(from, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := fsMove(from, to); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if _, err := os.Lstat(from); !os.IsNotExist(err) {
		t.Error("源应已不存在")
	}
	got, err := os.ReadFile(to)
	if err != nil || string(got) != "hello" {
		t.Errorf("目标内容不符: %q, err %v", got, err)
	}

	t.Run("源不存在报错", func(t *testing.T) {
		if err := fsMove(filepath.Join(dir, "ghost"), filepath.Join(dir, "c.txt")); err == nil {
			t.Error("应报错")
		}
	})

	// 不覆盖是刻意的选择：批量重命名算错一个目标名不该毁掉已有文件
	t.Run("目标已存在不覆盖", func(t *testing.T) {
		other := filepath.Join(dir, "c.txt")
		if err := os.WriteFile(other, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := fsMove(to, other); err == nil {
			t.Fatal("目标已存在应报错")
		}
		data, err := os.ReadFile(other)
		if err != nil || string(data) != "keep" {
			t.Errorf("已存在的目标被改动: %q, err %v", data, err)
		}
	})

	t.Run("源目标相同视为无操作", func(t *testing.T) {
		if err := fsMove(to, to); err != nil {
			t.Errorf("相同路径应视为成功: %v", err)
		}
		if _, err := os.Lstat(to); err != nil {
			t.Errorf("文件不该消失: %v", err)
		}
	})

	t.Run("目录可移动", func(t *testing.T) {
		src := filepath.Join(dir, "srcdir")
		dst := filepath.Join(dir, "dstdir")
		if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := fsMove(src, dst); err != nil {
			t.Fatalf("移动目录失败: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dst, "nested")); err != nil {
			t.Errorf("子目录未跟过来: %v", err)
		}
	})

	t.Run("目标父目录缺失报错", func(t *testing.T) {
		if err := fsMove(to, filepath.Join(dir, "nope", "d.txt")); err == nil {
			t.Error("父目录不存在应报错（不自动创建）")
		}
	})
}

func TestFSStat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := fsStat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got["exists"] != true || got["isDir"] != false || got["size"] != int64(3) {
		t.Errorf("stat 结果不符: %v", got)
	}

	got, err = fsStat(filepath.Join(dir, "ghost"))
	if err != nil {
		t.Fatalf("不存在的路径不应报错（便于插件做存在性判断）: %v", err)
	}
	if got["exists"] != false {
		t.Errorf("exists 应为 false: %v", got)
	}

	got, err = fsStat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got["isDir"] != true {
		t.Errorf("目录 isDir 应为 true: %v", got)
	}
}
