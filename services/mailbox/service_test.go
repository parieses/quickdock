package mailbox

import (
	"testing"

	internalmailbox "quickdock/internal/mailbox"
)

// 钉住「留空自动补服务器」的口径：设置面板的 placeholder 承诺了这件事，
// SetConfig 与 TestConnection 两条路径都不能漏。曾经漏在测试连接上，
// 症状是用户配好了却报「IMAP 服务器地址为空」，还以为是服务商不通。
func TestResolveHost(t *testing.T) {
	cases := []struct {
		name  string
		host  string
		email string
		want  string
	}{
		{"QQ 邮箱留空自动补", "", "someone@qq.com", "imap.qq.com"},
		{"Foxmail 走同一台", "", "someone@foxmail.com", "imap.qq.com"},
		{"域名大小写不敏感", "", "Someone@QQ.COM", "imap.qq.com"},
		{"网易邮箱", "", "someone@163.com", "imap.163.com"},
		{"手填优先于自动识别", "imap.custom.test", "someone@qq.com", "imap.custom.test"},
		{"手填带空格要去掉", "  imap.163.com  ", "", "imap.163.com"},
		{"认不出的域名返回空，由调用方报错", "", "someone@example.com", ""},
		{"空邮箱返回空", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveHost(c.host, c.email); got != c.want {
				t.Fatalf("resolveHost(%q, %q) = %q, want %q", c.host, c.email, got, c.want)
			}
		})
	}
}

// 多账号下 GetConfig 的聚合口径：只要有一个启用账号没连上就是「未全连上」，
// 禁用账号不参与判定，错误原因取第一个非空；Accounts 必须逐账号原样返回。
func TestGetConfigAggregatesAccounts(t *testing.T) {
	s := &MailboxService{dismissed: make(map[string]bool)}
	one := newAccount("a1", internalmailbox.Config{Email: "one@qq.com", Host: "imap.qq.com", Port: 993, Enabled: true}, "")
	one.connected = true
	two := newAccount("a2", internalmailbox.Config{Email: "two@163.com", Host: "imap.163.com", Port: 993, Enabled: true}, "")
	two.lastErr = "登录超时"
	off := newAccount("a3", internalmailbox.Config{Email: "off@example.com", Enabled: false}, "")
	s.accounts = []*account{one, two, off}

	got := s.GetConfig().Data.(internalmailbox.PublicConfig)
	if len(got.Accounts) != 3 {
		t.Fatalf("Accounts 应有 3 个，实际 %d", len(got.Accounts))
	}
	if got.Connected {
		t.Error("有一个启用账号没连上，聚合 Connected 不该为 true")
	}
	if got.LastErr != "登录超时" {
		t.Errorf("LastErr = %q, want %q", got.LastErr, "登录超时")
	}
	if !got.Enabled || got.Email != "one@qq.com" {
		t.Errorf("兼容字段不对: Enabled=%v Email=%q", got.Enabled, got.Email)
	}

	// 禁用账号即使 connected=false、lastErr 非空，也不该把整体拖成失败。
	off.lastErr = "旧错误"
	two.connected, two.lastErr = true, ""
	got = s.GetConfig().Data.(internalmailbox.PublicConfig)
	if !got.Connected || got.LastErr != "" {
		t.Errorf("禁用账号不该影响聚合: Connected=%v LastErr=%q", got.Connected, got.LastErr)
	}

	one.cfg.Enabled = false
	two.cfg.Enabled = false
	got = s.GetConfig().Data.(internalmailbox.PublicConfig)
	if got.Enabled || got.Connected {
		t.Errorf("全部禁用时应是 Enabled=false Connected=false，实际 %v/%v", got.Enabled, got.Connected)
	}
}
