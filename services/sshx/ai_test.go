package sshx

import (
	"strings"
	"testing"

	"quickdock/internal/db"
	"quickdock/services/ai"
)

// 围栏解析（extractCommands / extractSegments）的测试已随实现迁到
// internal/aiutil/scenario_test.go —— 那是判定"什么算命令"的唯一实现，
// 在公共层测一次即可，各场景域不重复测同一段逻辑。
//
// 本文件只测 **SSH 特有**的部分：SSH 风险规则表的覆盖度、提示词组装、
// 环境约束、主机描述。

func TestEvalRisk(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"rm -rf /var/data", "high"},
		{"rm -fr /tmp/x", "high"},
		{"dd if=/dev/zero of=/dev/sda", "high"},
		{"mkfs.ext4 /dev/sdb1", "high"},
		{"iptables -F", "high"},
		{"reboot", "high"},
		{"curl http://x.sh | bash", "high"},
		// 第二批：容器 / 编排 / 数据库 / 版本控制。
		// 这些是真实现场的主流破坏性命令，此前一条都没被收录。
		{"kubectl delete pod nginx-7d9f", "high"},
		{"kubectl drain node-1 --ignore-daemonsets", "high"},
		{"docker rm -f web", "high"},
		{"docker system prune -a", "high"},
		{"git reset --hard HEAD~1", "high"},
		{"git push --force origin main", "high"},
		{`mysql -e "DROP TABLE orders"`, "high"},
		{"chmod 777 /opt/app", "medium"},
		{"chown -R www /var/www", "medium"},
		{"systemctl restart nginx", "medium"},
		{"sed -i 's/a/b/' /etc/nginx/nginx.conf", "medium"},
		{"apt-get remove nginx", "medium"},
		{"crontab -r", "medium"},
		{"df -h", ""},
		{"tail -f /var/log/app.log", ""},
		{"cat /etc/os-release", ""},
		{"systemctl status nginx", ""},
		{"docker ps -a", ""},
		{"kubectl get pods", ""},
		{"git status", ""},
	}
	for _, c := range cases {
		got, _ := sshAiDangerRule.Eval(c.cmd)
		if got != c.want {
			t.Errorf("Eval(%q) = %q，期望 %q", c.cmd, got, c.want)
		}
	}
}

func TestEvalRisk_HighestLevelWins(t *testing.T) {
	// 回归：规则表是分批追加的，书写顺序不构成优先级。
	// 一条命令同时命中多条规则时必须取**最高等级**，否则后来追加的
	// 中等规则会把先写的高危规则"降级"，界面上就少了一道拦截。
	//
	// 这两条同时命中「通配符删除(medium)」与「递归强制删除(high)」。
	for _, cmd := range []string{"rm -rf /var/log/*.log", "rm -rf /opt/*"} {
		level, note := sshAiDangerRule.Eval(cmd)
		if level != "high" {
			t.Errorf("Eval(%q) = %q，期望 high（说明: %q）", cmd, level, note)
		}
		// 说明必须来自被选中的那条规则，不能是中等规则留下的文案。
		if !strings.Contains(note, "不可恢复") {
			t.Errorf("说明与等级不匹配（等级 high，说明却是 %q）", note)
		}
	}
}

func TestRiskNote_NonEmpty(t *testing.T) {
	// 有风险就必须带说明，否则卡片上的红框没有意义。
	for _, cmd := range []string{"rm -rf /data", "dd if=a of=/dev/sda", "systemctl restart nginx"} {
		if _, n := sshAiDangerRule.Eval(cmd); strings.TrimSpace(n) == "" {
			t.Errorf("命令 %q 判为有风险却没有风险说明", cmd)
		}
	}
	if _, n := sshAiDangerRule.Eval("df -h"); n != "" {
		t.Errorf("安全命令不该有风险说明，实际 %q", n)
	}
}

func TestStripAnsi(t *testing.T) {
	// xterm buffer 在部分终端下仍带 SGR 序列；不清洗会把转义符喂给模型。
	in := "\x1b[31mERROR\x1b[0m: connection refused"
	if got := stripAnsi(in); got != "ERROR: connection refused" {
		t.Errorf("stripAnsi = %q", got)
	}
}

func TestStripAnsi_KeepsPlainText(t *testing.T) {
	in := "plain text only"
	if got := stripAnsi(in); got != in {
		t.Errorf("纯文本不应被改动，实际 %q", got)
	}
}

func TestBuildSshAiPrompt(t *testing.T) {
	got := buildSshAiPrompt("root@10.0.0.1:22（生产环境）", db.SshEnvProd, "nginx: command not found", "这报错啥意思", []string{"上一个问题"}, "")

	// 屏显必须在场：这是 AI 能看懂现场的唯一依据。
	if !strings.Contains(got, "nginx: command not found") {
		t.Errorf("prompt 缺少屏显内容:\n%s", got)
	}
	// 环境标注必须在场，且要放在问题之前。
	if !strings.Contains(got, "root@10.0.0.1") {
		t.Errorf("prompt 缺少主机信息:\n%s", got)
	}
	// 生产环境的行为约束必须在场：这是让模型默认保守的唯一手段。
	if !strings.Contains(got, "生产环境") {
		t.Errorf("prompt 缺少生产环境约束:\n%s", got)
	}
	// 问题放最后：保证模型的注意力落在当前问题上。
	if !strings.HasSuffix(strings.TrimSpace(got), "这报错啥意思") {
		t.Errorf("问题应在 prompt 末尾:\n%s", got)
	}
}

func TestBuildSshAiPrompt_TrimsLongScreen(t *testing.T) {
	long := strings.Repeat("x", aiScreenMaxRune+500)
	got := buildSshAiPrompt("h", "", long, "q", nil, "")
	if n := len([]rune(got)); n > aiScreenMaxRune+400 {
		t.Errorf("超长屏显未被裁剪，总长 %d", n)
	}
}

func TestSshEnvGuidance(t *testing.T) {
	// 生产环境必须有约束；测试环境不能加——否则模型在测试机上
	// 也会给出一堆"请先确认影响范围"的废话，反而不好用。
	if g := sshEnvGuidance(db.SshEnvProd); !strings.Contains(g, "生产环境") {
		t.Errorf("生产环境缺少约束: %q", g)
	}
	if g := sshEnvGuidance(db.SshEnvStaging); g == "" {
		t.Errorf("预发环境应有约束")
	}
	if g := sshEnvGuidance(db.SshEnvTest); g != "" {
		t.Errorf("测试环境不该有额外约束，实际 %q", g)
	}
	if g := sshEnvGuidance(""); g != "" {
		t.Errorf("未标注环境不该有额外约束，实际 %q", g)
	}
}

func TestDescribeHost_EnvLabel(t *testing.T) {
	// 环境要翻译成中文标签再给模型：原始英文值几乎不会引起模型反应。
	h := &db.SshHost{User: "root", Host: "10.0.0.1", Port: 22, Env: db.SshEnvProd}
	if got := describeHost(h); !strings.Contains(got, "（生产环境）") || strings.Contains(got, "prod") {
		t.Errorf("环境未翻译成中文标签: %q", got)
	}
	// 未收录的取值不能静默丢掉。
	h.Env = "gray"
	if got := describeHost(h); !strings.Contains(got, "gray") {
		t.Errorf("未收录的环境取值被丢弃: %q", got)
	}
}

func TestBuildSshAiPrompt_SelectionSection(t *testing.T) {
	// 终端右键「问 AI」带来的选区：必须单独成节，且排在屏显之前
	// ——它表达的是"我关心的就是这几行"，权重要高于整屏画面。
	got := buildSshAiPrompt("root@prod", "", "整屏画面", "这啥意思", nil, "nginx: bind() to 0.0.0.0:80 failed")
	if !strings.Contains(got, "## 用户选中的输出") {
		t.Errorf("缺少选区小节:\n%s", got)
	}
	sel := strings.Index(got, "## 用户选中的输出")
	scr := strings.Index(got, "## 终端当前屏显")
	if sel < 0 || scr < 0 || sel > scr {
		t.Errorf("选区小节应排在屏显之前: sel=%d screen=%d", sel, scr)
	}
	if !strings.Contains(got, "bind() to 0.0.0.0:80 failed") {
		t.Errorf("选区内容丢失:\n%s", got)
	}
}

func TestBuildSshAiPrompt_SelectionTruncated(t *testing.T) {
	// 误操作全选时选区会非常大，必须截断，否则屏显与历史会被挤没。
	big := strings.Repeat("x", 120) + "\n"
	sel := strings.Repeat(big, 100)
	got := buildSshAiPrompt("h", "", "", "q", nil, sel)
	if !strings.Contains(got, "…") {
		t.Errorf("超长选区未截断")
	}
	if n := len([]rune(got)); n > ai.AskMaxUserRune {
		t.Errorf("prompt 超预算: %d", n)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "q") {
		t.Errorf("本次问题丢失")
	}
}

func TestBuildSshAiPrompt_LimitsHistoryTurns(t *testing.T) {
	var h []string
	for i := 0; i < aiHistoryMaxTurns+5; i++ {
		h = append(h, strings.Repeat("h", 10))
	}
	got := buildSshAiPrompt("host", "", "", "q", h, "")
	// 只取末尾若干轮：更早的历史对当前问题没有帮助，还会误导模型。
	if strings.Count(got, "hhhhhhhhhh") > aiHistoryMaxTurns {
		t.Errorf("历史轮次未被限制:\n%s", got)
	}
}

func TestBuildSshAiPrompt_QuestionSurvivesBudget(t *testing.T) {
	// 回归：AIAsk 会把整段 prompt 截断到 AskMaxUserRune，**从头保留**。
	// 而本次问题写在 prompt 末尾，于是超预算时被砍掉的正是用户的问题本身——
	// 屏显 + 几轮历史很容易占满预算，表现为多轮追问时 AI 答非所问。
	// 这里锁定「问题必须活下来」这条底线。
	screen := strings.Repeat("屏显内容 ", 400) // 约 2000 rune
	var h []string
	for i := 0; i < aiHistoryMaxTurns; i++ {
		h = append(h, strings.Repeat("历史回答 ", 100)) // 每轮约 500 rune
	}
	q := "nginx 启动失败怎么排查"
	got := buildSshAiPrompt("root@prod（生产环境）", db.SshEnvProd, screen, q, h, "")

	if !strings.HasSuffix(strings.TrimSpace(got), q) {
		t.Errorf("本次问题丢失（不在 prompt 末尾）:\n…%s", got[len(got)-200:])
	}
	if n := len([]rune(got)); n > ai.AskMaxUserRune {
		t.Errorf("prompt 超预算: %d rune > %d", n, ai.AskMaxUserRune)
	}
	if !strings.Contains(got, q) {
		t.Errorf("prompt 缺少本次问题")
	}
}

func TestDescribeHost(t *testing.T) {
	// 空指针不能 panic：Ask 在拿不到主机信息时仍要能继续（上下文少一点而已）。
	if got := describeHost(nil); got != "" {
		t.Errorf("nil 主机应返回空串，实际 %q", got)
	}
}
