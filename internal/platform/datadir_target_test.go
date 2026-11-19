package platform

import (
	"path/filepath"
	"testing"
)

// TestIsSubPath 验证迁移前的父子目录判定：子目录（含多层、含自身）必须命中。
func TestIsSubPath(t *testing.T) {
	root := filepath.Join("D:", "data")
	cases := []struct {
		parent, child string
		want          bool
	}{
		{root, filepath.Join(root, ".quickdock"), true},
		{root, filepath.Join(root, "a", "b", "c"), true},
		{root, root, true},
		{root, filepath.Join("D:", "data2"), false}, // 前缀相同但不是子目录
		{root, filepath.Join("D:", "other", "data"), false},
		{filepath.Join(root, "sub"), root, false}, // 反向：父目录不是子目录
		{"", filepath.Join(root, "x"), false},
		{root, "", false},
	}
	for _, c := range cases {
		if got := IsSubPath(c.parent, c.child); got != c.want {
			t.Errorf("IsSubPath(%q, %q) = %v，期望 %v", c.parent, c.child, got, c.want)
		}
	}
}

// TestIsSubPathSiblingAndTrailingSep 验证尾部斜杠、相对片段不影响判定，
// 且兄弟目录（迁移目标常见形态）不会被误判成父子。
func TestIsSubPathSiblingAndTrailingSep(t *testing.T) {
	oldDir := filepath.Clean(filepath.Join("D:", "quickdock", ".quickdock"))
	if !IsSubPath(oldDir, oldDir+string(filepath.Separator)) {
		t.Errorf("尾部斜杠应视为同一目录")
	}
	if !IsSubPath(oldDir, filepath.Join(oldDir, ".", "logs")) {
		t.Errorf("含 . 的相对片段应正常解析")
	}
	newDir := filepath.Join("E:", "quickdock", ".quickdock")
	if IsSubPath(oldDir, newDir) || IsSubPath(newDir, oldDir) {
		t.Errorf("不同盘的目录不应判定为父子")
	}
}
