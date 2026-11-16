package aiutil

// 不跑 go test（项目纪律禁止编译/测试），这些用例留给 CI 与本机 wails3 dev
// 之外的显式验证。重点不是穷举所有密钥格式，而是守住三条不能退让的性质：
// 明文不外泄、行数与行尾不变、重复脱敏不破坏已有标记。
import (
	"strings"
	"testing"
)

func TestRedactSecretsPrivateKeyBlock(t *testing.T) {
	in := "$ cat ~/.ssh/id_ed25519\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACBSuperSecretBase64MaterialHere1234567890abcdefgh\n" +
		"-----END OPENSSH PRIVATE KEY-----\n" +
		"$ echo done\n"
	out := RedactSecrets(in)
	if strings.Contains(out, "b3BlbnNzaC1rZXktdjEAAAA") || strings.Contains(out, "SuperSecretBase64Material") {
		t.Fatalf("私钥正文仍留在输出里:\n%s", out)
	}
	if !strings.Contains(out, redactKey) {
		t.Fatalf("私钥块没有替换为 %s:\n%s", redactKey, out)
	}
	if got, want := strings.Count(out, "\n"), strings.Count(in, "\n"); got != want {
		t.Fatalf("行数被改变: got %d want %d", got, want)
	}
	if !strings.HasSuffix(out, "$ echo done\n") {
		t.Fatalf("私钥块之后的普通行被吞掉了:\n%s", out)
	}
}

func TestRedactSecretsAssignmentsAndURL(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		leak       string
		wantSubstr string
	}{
		{
			name:       "aws secret",
			in:         "export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			leak:       "wJalrXUtnFEMI",
			wantSubstr: "AWS_SECRET_ACCESS_KEY=" + redactGeneric,
		},
		{
			name:       "yaml colon with quotes",
			in:         `password: "hunter2secret"`,
			leak:       "hunter2secret",
			wantSubstr: "password: ",
		},
		{
			// 带业务前缀的环境变量：键名前缀与 password 同为 word 字符，
			// 只写 \b(password)\b 会整条漏掉，屏显里却最常见。
			name:       "prefixed env var",
			in:         "DB_PASSWORD=supersecretvalue",
			leak:       "supersecretvalue",
			wantSubstr: "DB_PASSWORD=" + redactGeneric,
		},
		{
			name:       "url embedded credentials",
			in:         "curl https://admin:s3cr3tpass@db.internal/api/v1",
			leak:       "s3cr3tpass",
			wantSubstr: "https://[REDACTED]@db.internal/api/v1",
		},
		{
			name:       "cli flag",
			in:         "mysql --password=hunter2secret -h db",
			leak:       "hunter2secret",
			wantSubstr: "--password=" + redactGeneric,
		},
		{
			name:       "bearer header",
			in:         `curl -H "Authorization: Bearer abcdefghijklmnop1234" https://api.example.com`,
			leak:       "abcdefghijklmnop1234",
			wantSubstr: "Bearer " + redactGeneric,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactSecrets(tc.in)
			if strings.Contains(out, tc.leak) {
				t.Fatalf("明文泄露: %q", out)
			}
			if !strings.Contains(out, tc.wantSubstr) {
				t.Fatalf("期望保留上下文 %q，实际 %q", tc.wantSubstr, out)
			}
		})
	}
}

// 样本一律用运行期拼接，不写成完整字面量：GitHub 的推送保护会把这些形状
// 当成真密钥拦下整条 push（xoxb- 开头的假样本就会被判成 Slack token），
// 而这只是一个脱敏用例。拼接后源码里不再有可匹配的串，测试语义不变。
func TestRedactSecretsTokenFormats(t *testing.T) {
	const (
		digits = "1234567890123456789"
		alpha  = "abcdefghijklmnopqrstuvwxyz"
	)
	tokens := []string{
		"ghp_" + digits + alpha[:7],
		"github_pat_11ABCDEFG0123456789_" + alpha,
		"xoxb-" + digits[:12] + "-" + digits[:13] + "-" + alpha[:18],
		"sk-" + alpha + digits[:10],
		"glpat-" + alpha[:18] + "123456",
		"AKIA" + "IOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV",
	}
	for _, tok := range tokens {
		out := RedactSecrets("leaked=" + tok)
		if strings.Contains(out, tok) {
			t.Errorf("token 未被脱敏: %s -> %s", tok, out)
		}
	}
}

func TestRedactSecretsKeepsLayoutAndIdempotent(t *testing.T) {
	// token 同样运行期拼出来，避免源码里出现可被推送保护命中的字面量。
	fakeToken := "ghp_" + "1234567890" + "abcdefghijklmnopqrstuv"
	in := "root@prod:~$ cat /etc/app.conf\r\n" +
		"# 数据库连接配置（中文注释不应被改动）\r\n" +
		"DB_PASSWORD=supersecretvalue\r\n" +
		"token: " + fakeToken + "\r\n" +
		"done\r\n"
	out := RedactSecrets(in)
	if strings.Contains(out, "supersecretvalue") || strings.Contains(out, fakeToken) {
		t.Fatalf("敏感值残留:\n%s", out)
	}
	if !strings.Contains(out, "中文注释不应被改动") {
		t.Fatalf("非敏感行被改动:\n%s", out)
	}
	if got, want := strings.Count(out, "\r\n"), strings.Count(in, "\r\n"); got != want {
		t.Fatalf("CRLF 行数改变: got %d want %d", got, want)
	}
	if strings.Contains(out, "\n") && strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatal("混入了裸 LF，CRLF 被破坏")
	}
	// 幂等：流式上下文会对同一段文本反复调用，不能把标记越叠越长。
	if again := RedactSecrets(out); again != out {
		t.Fatalf("二次脱敏结果不一致:\n第一次 %q\n第二次 %q", out, again)
	}
}

func TestRedactSecretsShortValueAndEmpty(t *testing.T) {
	// "password: yes" 是提示词不是凭据，值不足 4 字符不该被抹掉，
	// 否则模型会以为上下文被人为篡改。
	if got := RedactSecrets("password: yes"); got != "password: yes" {
		t.Fatalf("短值被误伤: %q", got)
	}
	if got := RedactSecrets(""); got != "" {
		t.Fatalf("空串应原样返回: %q", got)
	}
	if got := RedactSecrets("完全普通的一行输出"); got != "完全普通的一行输出" {
		t.Fatalf("普通文本被改动: %q", got)
	}
}

func TestRedactSecretsInReturnsNewSlice(t *testing.T) {
	in := []string{"token=abcdefgh1234", "echo ok"}
	out := RedactSecretsIn(in)
	if len(out) != len(in) {
		t.Fatalf("长度不符: %d", len(out))
	}
	if strings.Contains(out[0], "abcdefgh1234") {
		t.Fatalf("第一项未脱敏: %q", out[0])
	}
	if out[1] != "echo ok" {
		t.Fatalf("第二项被误改: %q", out[1])
	}
	// 入参 slice 不应被就地改写（调用方可能还要拿原文做展示）。
	if in[0] != "token=abcdefgh1234" {
		t.Fatal("入参被就地修改")
	}
}
