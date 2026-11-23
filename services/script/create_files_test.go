package script

import "testing"

// TestEntryFromFiles 守住「AI 写的文件名和模板默认入口不一致」这条最容易出错的路径：
// 选错了不会报错，只会跑起来没有任何输出（跑的是模板里那三行 hello）。
func TestEntryFromFiles(t *testing.T) {
	cases := []struct {
		name  string
		lang  string
		files map[string]string
		want  string
	}{
		{
			name:  "python 有 requirements 时入口必须是 .py",
			lang:  "python",
			files: map[string]string{"requirements.txt": "requests\n", "main.py": "print(1)"},
			want:  "main.py",
		},
		{
			name:  "只给了 index.js 也要认",
			lang:  "javascript",
			files: map[string]string{"index.js": "console.log(1)"},
			want:  "index.js",
		},
		{
			name:  "go 目录里混着 go.mod",
			lang:  "go",
			files: map[string]string{"go.mod": "module x\n", "main.go": "package main"},
			want:  "main.go",
		},
		{
			name:  "php 只有 composer.json 时不硬选",
			lang:  "php",
			files: map[string]string{"composer.json": "{}"},
			want:  "",
		},
		{
			name:  "custom 没有扩展名约定",
			lang:  "custom",
			files: map[string]string{"run.ps1": "echo 1"},
			want:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := entryFromFiles(c.lang, c.files); got != c.want {
				t.Fatalf("entryFromFiles(%s) = %q, want %q", c.lang, got, c.want)
			}
		})
	}
}

// TestHasInternalMarker 内部标记是「可信」与「依赖已装」的凭证，任何写入口
// 能写它等于自己给自己发豁免——必须逐个路径段拦住，包括子目录里的。
func TestHasInternalMarker(t *testing.T) {
	yes := []string{".qd-trusted", ".qd-deps-ok", "sub/.qd-trusted", "a\\b\\.qd-deps-ok"}
	for _, p := range yes {
		if !hasInternalMarker(p) {
			t.Fatalf("%q 应判为内部标记", p)
		}
	}
	no := []string{"main.py", "requirements.txt", "qd-trusted", "sub/main.py"}
	for _, p := range no {
		if hasInternalMarker(p) {
			t.Fatalf("%q 不应判为内部标记", p)
		}
	}
}

func TestResolveRelForNew(t *testing.T) {
	s := &ScriptService{}
	ok := []string{"main.py", "sub/helper.py", "a/b/c.txt"}
	for _, p := range ok {
		if _, err := s.resolveRelForNew(p); err != nil {
			t.Fatalf("%q 应允许：%v", p, err)
		}
	}
	bad := []string{"", "../escape.py", "/abs.py", `C:\abs.py`, ".qd-trusted", "sub/.qd-deps-ok"}
	for _, p := range bad {
		if _, err := s.resolveRelForNew(p); err == nil {
			t.Fatalf("%q 应拒绝", p)
		}
	}
}
