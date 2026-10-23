package sshx

import (
	"strings"
	"testing"

	"quickdock/internal/db"
	"quickdock/services/ai"
)

// extractCommands 是这个功能里最该被测的一环：它决定用户复制到生产机上的到底是什么。
// 一旦漏解析或误解析，错的不是显示，是即将执行的命令。

func TestExtractCommands_Basic(t *testing.T) {
	in := "先看磁盘：\n```bash\ndf -h\n```\n然后重启服务。\n"
	got := extractCommands(in)
	if len(got) != 1 {
		t.Fatalf("期望 1 条命令，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Text != "df -h" {
		t.Errorf("命令文本 = %q，期望 %q", got[0].Text, "df -h")
	}
}

func TestExtractCommands_NoFence(t *testing.T) {
	// 模型没按要求用围栏：不能崩，也不能把整段说明当成命令。
	got := extractCommands("你可以先运行 df -h 看看磁盘占用。")
	if len(got) != 0 {
		t.Errorf("无围栏时不应解析出命令，实际: %+v", got)
	}
}

func TestExtractCommands_MultiLineBlock(t *testing.T) {
	// 一个块里多条命令：复制要一条一个卡片，否则用户只能整段粘。
	in := "```bash\nsystemctl status nginx\nss -tlnp\n```"
	got := extractCommands(in)
	if len(got) != 2 {
		t.Fatalf("期望 2 条命令，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Text != "systemctl status nginx" || got[1].Text != "ss -tlnp" {
		t.Errorf("命令顺序或内容不对: %+v", got)
	}
}

func TestExtractCommands_SkipsComments(t *testing.T) {
	in := "```bash\n# 先查看状态\nps aux | grep nginx\n```"
	got := extractCommands(in)
	if len(got) != 1 {
		t.Fatalf("注释行应被跳过，期望 1 条，实际 %d: %+v", len(got), got)
	}
	if got[0].Text != "ps aux | grep nginx" {
		t.Errorf("命令文本 = %q", got[0].Text)
	}
}

func TestExtractCommands_Dedup(t *testing.T) {
	in := "```bash\ndf -h\n```\n重复一遍：\n```bash\ndf -h\n```"
	got := extractCommands(in)
	if len(got) != 1 {
		t.Errorf("重复命令应去重，实际 %d 条: %+v", len(got), got)
	}
}

func TestExtractCommands_AcceptsShAndBareFence(t *testing.T) {
	// 模型不总写 bash：sh 与无语言标注都要能解析，否则功能看起来"时好时坏"。
	for _, in := range []string{
		"```sh\nfree -m\n```",
		"```\nfree -m\n```",
		"```shell\nfree -m\n```",
	} {
		got := extractCommands(in)
		if len(got) != 1 || got[0].Text != "free -m" {
			t.Errorf("输入 %q 解析失败: %+v", in, got)
		}
	}
}

func TestExtractCommands_UnclosedFence(t *testing.T) {
	// 真实故障：模型输出被 max_tokens 截断，围栏没有收尾。
	// 旧实现用配对正则，未闭合时解析出 0 条命令 —— 用户看到的是
	// 「模型明明给了命令，界面上却空空如也」。这里锁定"宁可多显示也不丢命令"。
	in := "这条命令查看当前监听80端口的进程。\n\n```bash\nnetstat -tlnp | grep 80\n"
	got := extractCommands(in)
	if len(got) != 1 {
		t.Fatalf("未闭合围栏应照样解析出命令，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Text != "netstat -tlnp | grep 80" {
		t.Errorf("命令文本 = %q", got[0].Text)
	}
}

func TestExtractCommands_TildeFence(t *testing.T) {
	// 部分模型用 ~~~ 而不是 ```。
	in := "~~~bash\nfree -m\n~~~"
	got := extractCommands(in)
	if len(got) != 1 || got[0].Text != "free -m" {
		t.Errorf("波浪围栏未解析: %+v", got)
	}
}

func TestExtractCommands_ArbitraryLangTag(t *testing.T) {
	// 语言标注五花八门（console / shell-session / 无标注）都应照常解析。
	for _, in := range []string{
		"```console\nfree -m\n```",
		"```shell-session\nfree -m\n```",
		"```BASH\nfree -m\n```",
	} {
		got := extractCommands(in)
		if len(got) != 1 || got[0].Text != "free -m" {
			t.Errorf("输入 %q 解析失败: %+v", in, got)
		}
	}
}

func TestExtractCommands_MultipleFencesKeepOrder(t *testing.T) {
	// 多个围栏块：命令顺序必须与模型输出一致，否则用户复制到的顺序会错。
	in := "先看端口：\n```bash\nss -tlnp | grep :80\n```\n再看服务：\n```bash\nsystemctl status nginx\n```\n"
	got := extractCommands(in)
	if len(got) != 2 {
		t.Fatalf("期望 2 条命令，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Text != "ss -tlnp | grep :80" || got[1].Text != "systemctl status nginx" {
		t.Errorf("命令顺序或内容不对: %+v", got)
	}
}

func TestExtractCommands_FenceInsideText(t *testing.T) {
	// 正文里出现行内代码（单反引号）不应被误当成围栏。
	in := "命令 `netstat -tlnp | grep 80` 没有任何输出。\n\n```bash\nss -tlnp\n```"
	got := extractCommands(in)
	if len(got) != 1 || got[0].Text != "ss -tlnp" {
		t.Errorf("行内代码被误判为围栏: %+v", got)
	}
}

func TestExtractSegments_KeepsInterleavedOrder(t *testing.T) {
	// 真实模型输出形态：说明 → 命令 → 补充说明 → 命令。
	// 分段必须保留这个顺序，否则界面会把说明全堆在顶部、命令全放底部。
	in := "先探测端口：\n```bash\nss -tlnp | grep :80\n```\n再查服务：\n```bash\nsystemctl status nginx\n```\n拿到结果再判断。\n"
	segs := extractSegments(in)
	if len(segs) != 5 {
		t.Fatalf("期望 5 段（说明/命令/说明/命令/说明），实际 %d 段: %+v", len(segs), segs)
	}
	want := []string{"text", "cmd", "text", "cmd", "text"}
	for i, w := range want {
		if segs[i].Type != w {
			t.Errorf("第 %d 段类型 = %q，期望 %q", i, segs[i].Type, w)
		}
	}
	if len(segs[1].Commands) != 1 || segs[1].Commands[0].Text != "ss -tlnp | grep :80" {
		t.Errorf("第 1 段命令不对: %+v", segs[1])
	}
	if len(segs[3].Commands) != 1 || segs[3].Commands[0].Text != "systemctl status nginx" {
		t.Errorf("第 3 段命令不对: %+v", segs[3])
	}
}

func TestExtractSegments_NoBlankSegments(t *testing.T) {
	// 空段会让界面出现莫名空隙：连续空行、纯围栏块都不应产生空段。
	in := "\n\n```bash\n\n```\n\n说明文字\n\n"
	segs := extractSegments(in)
	for i, s := range segs {
		if s.Type == "text" && strings.TrimSpace(s.Text) == "" {
			t.Errorf("第 %d 段是空说明段: %+v", i, segs)
		}
		if s.Type == "cmd" && len(s.Commands) == 0 {
			t.Errorf("第 %d 段是空命令段: %+v", i, segs)
		}
	}
}

func TestExtractSegments_UnclosedFenceRescued(t *testing.T) {
	// 模型被截断时围栏没闭合：命令仍要出现在分段里，不能整段丢失。
	in := "这条命令查看当前监听80端口的进程。\n\n```bash\nnetstat -tlnp | grep 80\n"
	segs := extractSegments(in)
	found := false
	for _, s := range segs {
		if s.Type == "cmd" {
			for _, c := range s.Commands {
				if c.Text == "netstat -tlnp | grep 80" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("未闭合围栏中的命令丢失: %+v", segs)
	}
}

func TestExtractSegments_KeepsMarkdownInText(t *testing.T) {
	// 文字段必须保留 markdown 原文（加粗/列表），由前端渲染成 HTML。
	// 若后端就把标记吃掉，前端再渲染也救不回来。
	in := "说明**加粗**部分。\n\n```bash\ndf -h\n```\n"
	segs := extractSegments(in)
	if len(segs) == 0 || segs[0].Type != "text" {
		t.Fatalf("首段应为说明文字: %+v", segs)
	}
	if !strings.Contains(segs[0].Text, "**加粗**") {
		t.Errorf("文字段丢失 markdown 标记: %q", segs[0].Text)
	}
}

// realModelOutput 是用本机真实档案（agnes-2.5-flash）实际调用一次拿到的原始返回。
//
// 把它固化成样本的原因：真实模型的输出形态（行内代码、**加粗**、编号列表、
// 多个围栏块交错）比构造用例更"脏"，旧实现的三个问题只有在这份原文上才暴露：
// 正文中间出现 3~4 行连续空白、**加粗** 显示成星号、围栏未闭合时命令整条消失。
const realModelOutput = "命令 `netstat -tlnp | grep 80` 没有任何输出，说明**当前没有进程在监听 TCP 80 端口**。\n" +
	"\n" +
	"可能的原因：\n" +
	"1. Web 服务（如 nginx/httpd）未启动\n" +
	"2. 服务监听的是其他端口（如 8080、443）\n" +
	"3. 使用 IPv6 而非 IPv4\n" +
	"\n" +
	"先探测一下实际状态：\n" +
	"\n" +
	"```bash\nss -tlnp | grep -E ':80|:8080|:443'\n```\n" +
	"\n" +
	"```bash\nsystemctl status nginx httpd apache2 2>/dev/null || ps aux | grep -E 'nginx|httpd|apache' | grep -v grep\n```\n" +
	"\n" +
	"这两条命令会显示：\n" +
	"- 是否有相关端口正在监听\n" +
	"- Web 服务是否运行\n" +
	"\n" +
	"拿到结果后告诉我，我再帮你定位具体进程或启动服务。"

func TestExtractSegments_RealModelOutput(t *testing.T) {
	segs := extractSegments(realModelOutput)

	// 两条命令都要在，且顺序与模型输出一致。
	var cmds []string
	var kinds []string
	for _, s := range segs {
		kinds = append(kinds, s.Type)
		for _, c := range s.Commands {
			cmds = append(cmds, c.Text)
		}
	}
	if len(cmds) != 2 {
		t.Fatalf("真实输出应解析出 2 条命令，实际 %d 条: %+v", len(cmds), cmds)
	}
	if cmds[0] != "ss -tlnp | grep -E ':80|:8080|:443'" {
		t.Errorf("第 1 条命令不对: %q", cmds[0])
	}
	if !strings.HasPrefix(cmds[1], "systemctl status nginx httpd apache2") {
		t.Errorf("第 2 条命令不对: %q", cmds[1])
	}

	// 文字段不得含连续 3 个以上换行：旧实现剥围栏后会留下大片空白，
	// 界面上看起来就是"内容显示不全"。
	for i, s := range segs {
		if s.Type != "text" {
			continue
		}
		if strings.Contains(s.Text, "\n\n\n") {
			t.Errorf("第 %d 段含连续空行: %q", i, s.Text)
		}
	}

	// 说明与命令必须交错（说明→命令→命令→说明），
	// 不能退化成"说明全在前、命令全在后"。
	if len(kinds) < 4 {
		t.Errorf("分段数过少，说明与命令未交错: %v", kinds)
	}
}

func TestExtractCommands_RealModelOutput(t *testing.T) {
	got := extractCommands(realModelOutput)
	if len(got) != 2 {
		t.Fatalf("真实输出应解析出 2 条命令，实际 %d 条: %+v", len(got), got)
	}
	// 管道 + grep -E 组合不应被误判成高风险。
	if got[0].Risk != "" {
		t.Errorf("只读探测命令不该判为风险: %+v", got[0])
	}
}

func TestExtractCommands_ShellPromptPrefix(t *testing.T) {
	// 模型常按"贴终端实录"的写法给命令：$ 是普通用户提示符，# 是 root 提示符。
	// 这两种前缀都不是命令的一部分，必须处理掉——
	// 带着 `$ ` 发出去，远端会把 `$` 当成命令名报 "command not found"，
	// 用户看到的就是"AI 给的命令是坏的"。
	in := "先看看：" + "\n" + "```bash" + "\n" + "$ df -h" + "\n" + "```" + "\n" +
		"再装：" + "\n" + "```bash" + "\n" + "# apt-get install nginx" + "\n" + "```" + "\n"
	got := extractCommands(in)
	if len(got) != 2 {
		t.Fatalf("两条命令都应解析出来，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Text != "df -h" {
		t.Errorf("`$ ` 提示符未被剥离: %q", got[0].Text)
	}
	if got[1].Text != "apt-get install nginx" {
		t.Errorf("`# ` 提示符未被剥离: %q", got[1].Text)
	}
}

func TestExtractCommands_PromptRescueBoundaries(t *testing.T) {
	// 兜底回扫（把 `# ` 当 root 提示符）是一条危险的放宽：
	// 放错方向就会**把注释变成命令**，用户一键填入的是一段中文说明。
	// 下面四条是这个放宽必须守住的边界，每条都对应一种"放错就出事"的形态。
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"纯中文注释块不该被救成命令", "```bash\n# 这是说明\n# 也是说明\n```", nil},
		{"shebang 不是命令", "```bash\n#!/bin/bash\nset -e\n```", []string{"set -e"}},
		{"$ 后无空白不是提示符", "```bash\n$HOME/bin/deploy\n```", []string{"$HOME/bin/deploy"}},
		{"整块 root 提示符才救", "```bash\n# apt-get install nginx\n# systemctl enable nginx\n```",
			[]string{"apt-get install nginx", "systemctl enable nginx"}},
		{"块内有不带前缀的行就按注释处理", "```bash\n# cp a b\nls -l\n```", []string{"ls -l"}},
	}
	for _, c := range cases {
		got := extractCommands(c.in)
		var texts []string
		for _, g := range got {
			texts = append(texts, g.Text)
		}
		if len(texts) != len(c.want) {
			t.Errorf("%s: 得到 %v，期望 %v", c.name, texts, c.want)
			continue
		}
		for i := range c.want {
			if texts[i] != c.want[i] {
				t.Errorf("%s: 第 %d 条 = %q，期望 %q", c.name, i, texts[i], c.want[i])
			}
		}
	}
}

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
		got, _ := evalRisk(c.cmd)
		if got != c.want {
			t.Errorf("evalRisk(%q) = %q，期望 %q", c.cmd, got, c.want)
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
		level, note := evalRisk(cmd)
		if level != "high" {
			t.Errorf("evalRisk(%q) = %q，期望 high（说明: %q）", cmd, level, note)
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
		if _, n := evalRisk(cmd); strings.TrimSpace(n) == "" {
			t.Errorf("命令 %q 判为有风险却没有风险说明", cmd)
		}
	}
	if _, n := evalRisk("df -h"); n != "" {
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
