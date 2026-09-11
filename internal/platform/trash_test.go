package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 注意：下面有真实删除的用例，被删的是 t.TempDir() 里当场创建的文件。
// Windows 上会真的进回收站（这是本测试要验的行为），不会永久丢失。

func TestMoveToTrashRejects(t *testing.T) {
	t.Run("空路径", func(t *testing.T) {
		if err := MoveToTrash("   "); err == nil {
			t.Error("应报错")
		}
	})

	t.Run("不存在的路径", func(t *testing.T) {
		if err := MoveToTrash(filepath.Join(t.TempDir(), "ghost.txt")); err == nil {
			t.Error("应报错（否则插件无从判断到底删没删）")
		}
	})

	t.Run("卷根", func(t *testing.T) {
		vol := filepath.VolumeName(t.TempDir())
		if vol == "" {
			vol = "/"
			t.Skip("非 Windows，卷根形式不同")
		}
		root := vol + string(filepath.Separator)
		err := MoveToTrash(root)
		if err == nil {
			t.Fatal("卷根必须被拒绝")
		}
		if !strings.Contains(err.Error(), "卷根") {
			t.Errorf("错误信息应说明是卷根被拒: %v", err)
		}
	})
}

func TestMoveToTrashFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "quickdock-trash-test.txt")
	if err := os.WriteFile(p, []byte("to be trashed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(p); err != nil {
		t.Fatalf("移入回收站失败: %v", err)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("文件应已从原位置消失: %v", err)
	}
}

func TestMoveToTrashDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quickdock-trash-test-dir")
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "nested", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveToTrash(target); err != nil {
		t.Fatalf("目录移入回收站失败: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("目录应已从原位置消失: %v", err)
	}
}

// TestUniqueTrashName 同名文件不能在回收站里互相覆盖——从不同目录删来的文件常同名。
func TestUniqueTrashName(t *testing.T) {
	dir := t.TempDir()
	name := "a.txt"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := uniqueTrashName(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if got == name {
		t.Fatalf("已存在同名文件时应换名, got %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, got)); !os.IsNotExist(err) {
		t.Errorf("换名后的路径仍冲突: %v", err)
	}
}
