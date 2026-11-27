package mailbox

import "testing"

func TestClassifyReset(t *testing.T) {
	cases := []struct {
		subject string
		want    bool
	}{
		{"Reset your password", true},
		{"Password reset request", true},
		{"Forgot your password?", true},
		{"重置您的密码", true},
		{"账号密码重置", true},
		{"找回登录密码", true},
		{"您的账户密码需要重设", true},
		{"Your verification code is 123456", false},
		{"限时优惠：重置你的购物车", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ClassifyReset(c.subject); got != c.want {
			t.Errorf("ClassifyReset(%q) = %v, want %v", c.subject, got, c.want)
		}
	}
}

func TestClassifySecurityResetPrecedence(t *testing.T) {
	cases := []struct {
		subject, body, want string
	}{
		// 重置请求：安全事件规则刻意排除的功能入口，应落到 reset。
		{"Reset your password", "Click here to reset your password.", "reset"},
		// 已发生的事件仍归 password，不能降格成重置请求。
		{"密码已重置", "您的密码已重置。", "password"},
		{"Your password was changed", "Your password was changed.", "password"},
		{"New sign-in from a new device", "", "login"},
		// 验证码邮件页脚的善后话术不能误收。
		{"Your verification code", "If you did not request this, reset your password.", ""},
	}
	for _, c := range cases {
		if got := ClassifySecurity(c.subject, c.body); got != c.want {
			t.Errorf("ClassifySecurity(%q, %q) = %q, want %q", c.subject, c.body, got, c.want)
		}
	}
}
