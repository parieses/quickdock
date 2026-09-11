package plugin

import (
	"encoding/json"
	"testing"
)

func TestShellPermAllowsTarget(t *testing.T) {
	cases := []struct {
		name   string
		perm   ShellPerm
		target string
		want   bool
	}{
		{"全放行", ShellPerm{All: true}, "https://anything", true},
		{"未授权", ShellPerm{}, "https://github.com", false},
		{"精确 URL 命中", ShellPerm{Targets: []string{"https://github.com"}}, "https://github.com", true},
		{"前缀命中子页", ShellPerm{Targets: []string{"https://github.com"}}, "https://github.com/parieses", true},
		{"其它域名拒绝", ShellPerm{Targets: []string{"https://github.com"}}, "https://evil.com", false},
		{"文件前缀命中", ShellPerm{Targets: []string{"file:///C:/Users/me"}}, "file:///C:/Users/me/docs/a.txt", true},
		{"文件越界拒绝", ShellPerm{Targets: []string{"file:///C:/Users/me"}}, "file:///C:/Windows", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.perm.AllowsTarget(c.target); got != c.want {
				t.Errorf("AllowsTarget(%q) = %v, want %v", c.target, got, c.want)
			}
		})
	}
}

func TestShellPermUnmarshal(t *testing.T) {
	var p Permissions
	if err := json.Unmarshal([]byte(`{"shell":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Shell.All {
		t.Error("shell:true 应解析为 All=true")
	}
	if err := json.Unmarshal([]byte(`{"shell":["https://github.com","file:///C:/Users/me"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Shell.All || len(p.Shell.Targets) != 2 {
		t.Errorf("数组未正确解析: %+v", p.Shell)
	}
	if err := json.Unmarshal([]byte(`{"shell":123}`), &p); err == nil {
		t.Error("shell:123 应报错")
	}
}
