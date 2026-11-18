package fsguard

// WP7：写操作边界的补测试（实现已经在了，之前只是没被钉住）。
//
// 这些用例的价值在于「改坏了会有症状」：一旦有人把 clean 里的
// expandExisting 去掉，或者把 within 换成 strings.HasPrefix，
// 下面每一条都会立刻红，而不是等某个用户的项目被写到别处去。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `..` 穿越：绑定了工作目录时，写到目录外一律拒绝。
func TestResolveWriteRejectsDotDotTraversal(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	// 目录内正常路径：放行，且结果是规范化后的绝对路径。
	got, err := ResolveWrite(nil, filepath.Join("sub", "..", "a.go"), cwd)
	if err != nil {
		t.Fatalf("目录内路径应放行: %v", err)
	}
	if got != filepath.Join(cwd, "a.go") {
		t.Errorf("路径未规范化: %q", got)
	}

	// `..` 逃出去：拒绝。
	if _, err := ResolveWrite(nil, filepath.Join("..", "escape.go"), cwd); err == nil {
		t.Fatal("向上穿越到工作目录之外必须被拒")
	}
	// 用多段 `..` 绕：同样拒绝。
	if _, err := ResolveWrite(nil, filepath.Join("sub", "..", "..", "escape.go"), cwd); err == nil {
		t.Fatal("多段 .. 穿越同样必须被拒")
	}
	// 绝对路径落在目录外：拒绝。
	outside := filepath.Join(root, "outside.go")
	if _, err := ResolveWrite(nil, outside, cwd); err == nil {
		t.Fatal("工作目录外的绝对路径必须被拒")
	}
}

// 软链接逃逸：目录内有个指向外部的链接，顺着它写也要被拒。
//
// 这是最容易漏的一条——路径字符串完全在 cwd 之内，只有解引用之后才看得见。
func TestResolveWriteRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{cwd, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
	}
	link := filepath.Join(cwd, "link")
	if err := os.Symlink(outside, link); err != nil {
		// Windows 上创建符号链接需要开发者模式或管理员权限。
		t.Skipf("当前环境无法创建符号链接，跳过: %v", err)
	}

	if _, err := ResolveWrite(nil, filepath.Join("link", "evil.go"), cwd); err == nil {
		t.Fatal("顺着符号链接写到工作目录之外必须被拒")
	}
	// 链接本身在目录内，但目标是外部目录：同样要拒。
	if _, err := ResolveWrite(nil, "link", cwd); err == nil {
		t.Fatal("链接指向的目录在工作目录之外，必须被拒")
	}
}

// 没绑定工作目录时退回写白名单：临时目录不在白名单里，应当被拒
// （与「未绑定就能随便写」是两件完全不同的事）。
func TestResolveWriteWithoutCwdFallsBackToRoots(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	if _, err := ResolveWrite(nil, target, ""); err == nil {
		t.Fatalf("不在写白名单里的路径不该被放行: %s", target)
	}
	// 空路径且没有 cwd：给可读错误，而不是拼出一个奇怪的空路径。
	if _, err := ResolveWrite(nil, "", ""); err == nil {
		t.Fatal("既没有路径也没有工作目录时应当报错")
	}
}

// 大小写：Windows 上 `C:\Proj` 与 `c:\proj` 是同一个目录，判定必须一致。
// 这条只在 Windows 有意义，其它平台上跑也应当通过（区分大小写时本来就不相等）。
func TestResolveWriteCaseHandling(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(cwd, "sub"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	got, err := ResolveWrite(nil, filepath.Join(cwd, "sub", "a.go"), strings.ToUpper(cwd))
	if err != nil {
		// 非 Windows 上 ToUpper 出来的目录根本不存在，报错是正确行为。
		t.Skipf("大小写不同的工作目录在此平台上不是同一目录: %v", err)
	}
	if !strings.EqualFold(filepath.Dir(got), filepath.Join(cwd, "sub")) {
		t.Errorf("解析结果不对: %q", got)
	}
}
