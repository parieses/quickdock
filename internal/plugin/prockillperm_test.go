package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---- ProcessKillPerm：目标进程白名单 ----

func TestProcessKillPermUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		all     bool
		progs   []string
		granted bool
	}{
		{"缺省即无权限", `null`, false, nil, false},
		{"false 无权限", `false`, false, nil, false},
		{"空字符串无权限", `""`, false, nil, false},
		{"true 全放行", `true`, true, nil, true},
		{"数组白名单", `["chrome","code.exe"]`, false, []string{"chrome", "code.exe"}, true},
		{"空数组等于未声明", `[]`, false, []string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var p ProcessKillPerm
			if err := json.Unmarshal([]byte(c.in), &p); err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if p.All != c.all {
				t.Errorf("All = %v, 期望 %v", p.All, c.all)
			}
			if len(p.Programs) != len(c.progs) {
				t.Errorf("Programs = %v, 期望 %v", p.Programs, c.progs)
			}
			if p.Granted() != c.granted {
				t.Errorf("Granted() = %v, 期望 %v", p.Granted(), c.granted)
			}
		})
	}
}

func TestProcessKillPermUnmarshalInvalid(t *testing.T) {
	var p ProcessKillPerm
	if err := json.Unmarshal([]byte(`{"pid":1}`), &p); err == nil {
		t.Fatal("对象写法应报错")
	}
}

func TestProcessKillPermMarshalRoundTrip(t *testing.T) {
	cases := []ProcessKillPerm{
		{},
		{All: true},
		{Programs: []string{"chrome"}},
	}
	for _, want := range cases {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		var got ProcessKillPerm
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("反序列化失败: %v", err)
		}
		if got.All != want.All || len(got.Programs) != len(want.Programs) {
			t.Errorf("往返不一致: %+v -> %s -> %+v", want, b, got)
		}
	}
}

func TestProcessKillPermAllowsTarget(t *testing.T) {
	cases := []struct {
		name  string
		perm  ProcessKillPerm
		param string
		want  bool
	}{
		{"全放行放行任意", ProcessKillPerm{All: true}, "anything.exe", true},
		{"全放行放行空名", ProcessKillPerm{All: true}, "", true},
		{"白名单同名放行", ProcessKillPerm{Programs: []string{"chrome"}}, "chrome.exe", true},
		{"白名单大小写不敏感", ProcessKillPerm{Programs: []string{"Chrome"}}, "chrome.exe", true},
		{"白名单不含则拒绝", ProcessKillPerm{Programs: []string{"chrome"}}, "code.exe", false},
		{"白名单空名拒绝", ProcessKillPerm{Programs: []string{"chrome"}}, "", false},
		{"未声明任何目标拒绝", ProcessKillPerm{}, "chrome.exe", false},
		// 路径形式只比 basename：目标是 tasklist 给的镜像名，拿不到完整路径，
		// 因此 `C:\...\chrome.exe` 与 `chrome` 在此等价（文档已说明这一退化）。
		{"路径形式退化为 basename", ProcessKillPerm{Programs: []string{`C:\Program Files\Google\Chrome\chrome.exe`}}, "chrome.exe", true},
		{"路径形式 basename 不符拒绝", ProcessKillPerm{Programs: []string{`C:\Windows\System32\cmd.exe`}}, "chrome.exe", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.perm.AllowsTarget(c.param); got != c.want {
				t.Errorf("AllowsTarget(%q) = %v, 期望 %v", c.param, got, c.want)
			}
		})
	}
}

func TestProcessKillPermValidate(t *testing.T) {
	if err := (ProcessKillPerm{All: true}).Validate(); err != nil {
		t.Errorf("全放行应通过: %v", err)
	}
	if err := (ProcessKillPerm{Programs: []string{"chrome"}}).Validate(); err != nil {
		t.Errorf("正常白名单应通过: %v", err)
	}
	if err := (ProcessKillPerm{Programs: []string{"chrome", "  "}}).Validate(); !errors.Is(err, ErrInvalidManifest) {
		t.Errorf("空条目应报 ErrInvalidManifest，得到: %v", err)
	}
}

// ---- selfAudit 逃生舱 ----

func TestSelfAuditValues(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"audit", false},
		{"skip", true},
		{" skip ", true},   // 前后空白应被容忍
		{"SKIP", false},    // 大小写不归一：拼错就按「执行审计」处理，宁可多审
		{"skipped", false}, // 近似词不算
		{"none", false},
	}
	for _, c := range cases {
		p := Permissions{SelfAudit: c.in}
		if got := p.SelfAuditSkip(); got != c.want {
			t.Errorf("SelfAudit=%q → SelfAuditSkip()=%v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestSelfAuditValidate(t *testing.T) {
	cases := []struct {
		name    string
		audit   string
		reason  string
		wantErr bool
	}{
		{"缺省合法", "", "", false},
		{"显式 audit 合法", "audit", "", false},
		{"skip 但无理由", "skip", "", true},
		{"skip 理由过短", "skip", "嗯", true},
		{"skip 带合法理由", "skip", "路径来自配置文件，静态分析必然漏判", false},
		{"skip 理由前后空白", "skip", "   合法理由在这里   ", false},
		{"未知值", "skip-all", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Permissions{SelfAudit: c.audit, SkipReason: c.reason}
			err := p.Validate()
			if c.wantErr {
				if !errors.Is(err, ErrInvalidManifest) {
					t.Fatalf("期望 ErrInvalidManifest，得到: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
		})
	}
}

// ---- 新增的三类能力纳入 CheckAgainst ----

func TestCheckAgainstNewKinds(t *testing.T) {
	cases := []struct {
		name    string
		perms   Permissions
		audit   *PermissionAudit
		wantErr bool
		expect  string
	}{
		{
			name:    "剪贴板未声明",
			perms:   Permissions{},
			audit:   &PermissionAudit{NeedsClipboard: true},
			wantErr: true, expect: "clipboard",
		},
		{
			name:    "剪贴板已声明",
			perms:   Permissions{Clipboard: true},
			audit:   &PermissionAudit{NeedsClipboard: true},
			wantErr: false,
		},
		{
			name:    "结束进程未声明",
			perms:   Permissions{},
			audit:   &PermissionAudit{NeedsProcessKill: true},
			wantErr: true, expect: "processKill",
		},
		{
			name:    "结束进程已声明白名单",
			perms:   Permissions{ProcessKill: ProcessKillPerm{Programs: []string{"chrome"}}},
			audit:   &PermissionAudit{NeedsProcessKill: true},
			wantErr: false,
		},
		{
			name:    "唤起 shell 未声明",
			perms:   Permissions{},
			audit:   &PermissionAudit{NeedsShellOpen: true},
			wantErr: true, expect: "shell",
		},
		{
			name:    "唤起 shell 已声明",
			perms:   Permissions{Shell: ShellPerm{All: true}},
			audit:   &PermissionAudit{NeedsShellOpen: true},
			wantErr: false,
		},
		{
			name:    "三类都未用到时不误报",
			perms:   Permissions{},
			audit:   &PermissionAudit{},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.perms.CheckAgainst(c.audit)
			if c.wantErr {
				if err == nil {
					t.Fatal("期望报错，得到 nil")
				}
				if !strings.Contains(err.Error(), c.expect) {
					t.Errorf("错误信息应含 %q，实际: %v", c.expect, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
		})
	}
}
