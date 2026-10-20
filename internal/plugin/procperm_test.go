package plugin

import (
	"encoding/json"
	"testing"
)

// ---- ProcessPerm 序列化 ----

func TestProcessPermUnmarshal(t *testing.T) {
	cases := []struct {
		raw   string
		all   bool
		progs []string
	}{
		{`true`, true, nil},
		{`false`, false, nil},
		{`null`, false, nil},
		{`["git","reg"]`, false, []string{"git", "reg"}},
		{`[]`, false, []string{}},
	}
	for _, c := range cases {
		var p ProcessPerm
		if err := json.Unmarshal([]byte(c.raw), &p); err != nil {
			t.Fatalf("Unmarshal(%s) 报错: %v", c.raw, err)
		}
		if p.All != c.all {
			t.Errorf("Unmarshal(%s).All = %v, 期望 %v", c.raw, p.All, c.all)
		}
		if len(p.Programs) != len(c.progs) {
			t.Errorf("Unmarshal(%s).Programs = %v, 期望 %v", c.raw, p.Programs, c.progs)
		}
	}
}

func TestProcessPermUnmarshalInvalid(t *testing.T) {
	var p ProcessPerm
	if err := json.Unmarshal([]byte(`"git"`), &p); err == nil {
		t.Error("字符串写法应报错")
	}
	if err := json.Unmarshal([]byte(`{"exec":["git"]}`), &p); err == nil {
		t.Error("对象写法应报错（当前只支持 bool 与数组）")
	}
}

func TestProcessPermMarshalRoundTrip(t *testing.T) {
	for _, raw := range []string{`true`, `false`, `["git","reg"]`} {
		var p ProcessPerm
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		out, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(out) != raw {
			t.Errorf("往返不一致: %s -> %s", raw, string(out))
		}
	}
}

// ---- AllowsProgram ----

func TestProcessPermAllowsProgram(t *testing.T) {
	all := ProcessPerm{All: true}
	if !all.AllowsProgram("powershell") {
		t.Error("全放行应允许任意程序")
	}

	none := ProcessPerm{}
	if none.AllowsProgram("git") {
		t.Error("未声明应一律拒绝")
	}

	byName := ProcessPerm{Programs: []string{"netstat", "tasklist"}}
	// 程序名：匹配 PATH 里的同名程序
	if !byName.AllowsProgram("netstat") {
		t.Error("netstat 应被允许")
	}
	if !byName.AllowsProgram("netstat.exe") {
		t.Error("netstat.exe 与 netstat 应视为同一程序（扩展名差异）")
	}
	if !byName.AllowsProgram(`C:\Windows\System32\netstat.exe`) {
		t.Error("声明程序名时应允许任意来源目录下的同名程序")
	}
	if byName.AllowsProgram("taskkill") {
		t.Error("taskkill 不在白名单内，应拒绝")
	}
	if byName.AllowsProgram("") {
		t.Error("空程序名应拒绝")
	}
}

func TestProcessPermAllowsProgramAbsolutePath(t *testing.T) {
	abs := ProcessPerm{Programs: []string{`C:\Windows\System32\netstat.exe`}}
	if !abs.AllowsProgram(`C:\Windows\System32\netstat.exe`) {
		t.Error("绝对路径应精确匹配")
	}
	if abs.AllowsProgram(`C:\Temp\netstat.exe`) {
		t.Error("绝对路径声明不应匹配其他位置的同名程序")
	}
	if abs.AllowsProgram("netstat") {
		t.Error("绝对路径声明不应被纯程序名命中")
	}
}

// 通配符是白名单的绕过口子：声明 *.exe 等于放行 powershell。
func TestProcessPermRejectsWildcard(t *testing.T) {
	p := ProcessPerm{Programs: []string{"*.exe"}}
	if p.AllowsProgram("powershell.exe") {
		t.Error("通配符声明不应放行任意 exe")
	}
}

func TestProcessPermValidate(t *testing.T) {
	if err := (ProcessPerm{Programs: []string{"git"}}).Validate(); err != nil {
		t.Errorf("合法白名单不应报错: %v", err)
	}
	if err := (ProcessPerm{Programs: []string{"  "}}).Validate(); err == nil {
		t.Error("空条目应报错")
	}
	if err := (ProcessPerm{All: true}).Validate(); err != nil {
		t.Errorf("全放行无需校验条目: %v", err)
	}
}

func TestProcessPermGranted(t *testing.T) {
	if (ProcessPerm{}).Granted() {
		t.Error("未声明不应算已授权")
	}
	if !(ProcessPerm{All: true}).Granted() {
		t.Error("全放行应算已授权")
	}
	if !(ProcessPerm{Programs: []string{"git"}}).Granted() {
		t.Error("非空白名单应算已授权")
	}
}

// ---- 权限校验：host.process.exec ----

func TestCheckPermissionProcessExec(t *testing.T) {
	m := newTestManager()
	pid := "test.proc.deny"
	addTestPlugin(m, pid, Permissions{})
	if err := m.checkPermission(pid, "host.process.exec", json.RawMessage(`{"program":"git"}`)); err == nil {
		t.Error("未声明 process 时应拒绝")
	}

	pid2 := "test.proc.all"
	addTestPlugin(m, pid2, Permissions{Process: ProcessPerm{All: true}})
	if err := m.checkPermission(pid2, "host.process.exec", json.RawMessage(`{"program":"anything"}`)); err != nil {
		t.Errorf("process:true 应放行: %v", err)
	}

	pid3 := "test.proc.list"
	addTestPlugin(m, pid3, Permissions{Process: ProcessPerm{Programs: []string{"netstat"}}})
	if err := m.checkPermission(pid3, "host.process.exec", json.RawMessage(`{"program":"netstat"}`)); err != nil {
		t.Errorf("白名单内程序应放行: %v", err)
	}
	if err := m.checkPermission(pid3, "host.process.exec", json.RawMessage(`{"program":"powershell"}`)); err == nil {
		t.Error("白名单外程序应拒绝")
	}
}
