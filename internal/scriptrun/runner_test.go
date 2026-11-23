package scriptrun

import (
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestArgvGoAndPHP 守住两条语言特例：Go 必须经 `go run`，PHP 必须带
// display_errors 覆盖。这两条错了不会编译失败，只会「脚本跑起来什么都不发生」，
// 属于最难从现象反推原因的一类问题。
func TestArgvGoAndPHP(t *testing.T) {
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			return name + ".exe"
		}
		return name
	}
	dir := filepath.Join("tmp", "s1")
	entry := filepath.Join(dir, "main.go")

	cases := []struct {
		name string
		ip   Interpreter
		spec Spec
		want []string
	}{
		{
			name: "go 走 run 子命令",
			ip:   Interpreter{Exe: exe("go")},
			spec: Spec{Lang: LangGo, Dir: dir, Entry: "main.go", Args: []string{"-v"}},
			want: []string{exe("go"), "run", entry, "-v"},
		},
		{
			name: "go 工具链命令不加 run",
			ip:   Interpreter{Exe: exe("go")},
			spec: Spec{Lang: LangGo, Dir: dir, Prefix: []string{"mod", "tidy"}},
			want: []string{exe("go"), "mod", "tidy"},
		},
		{
			name: "php 带错误显示参数",
			ip:   Interpreter{Exe: exe("php")},
			spec: Spec{Lang: LangPHP, Dir: dir, Entry: "main.php"},
			want: []string{exe("php"), "-d", "display_errors=stderr", "-d", "error_reporting=E_ALL",
				filepath.Join(dir, "main.php")},
		},
		{
			name: "python 不受影响",
			ip:   Interpreter{Exe: exe("python")},
			spec: Spec{Lang: LangPython, Dir: dir, Entry: "main.py"},
			want: []string{exe("python"), filepath.Join(dir, "main.py")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Argv(c.ip, c.spec); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Argv() = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestCandidateNames(t *testing.T) {
	for _, lang := range []string{LangGo, LangPHP} {
		if len(candidateNames(lang)) == 0 {
			t.Fatalf("%s 没有候选可执行名", lang)
		}
	}
}
