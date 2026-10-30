// Package aiutil 承载场景化 AI 问答的公共内核：围栏分段解析、风险规则判定、长度裁剪。
//
// 为什么独立成引擎层（internal）而不塞进某个服务：
// 围栏解析回答的是「模型输出里哪一段是可执行内容」。SSH 终端、运行时日志、
// 端口全景、崩溃日志……每个域都要用同一套判定。判定一旦分叉，同一条命令就会
// 在正文与卡片里各出现一次，或干脆整条消失——而界面看起来完全正常，
// 排查时极难复现。
//
// 风险规则表不放在这里：危害评估必须结合场景前提
// （「这条命令在远程生产机上跑」与「这条命令在本机跑」结论完全不同），
// 规则表由各域自带，这里只提供判定机制（取最高等级 + 等级与说明同处产出）。
package aiutil

import (
	"regexp"
	"strings"
)

// ScenarioCommand 场景化回答里的一条可执行内容。
type ScenarioCommand struct {
	// Text 命令原文（已去掉围栏标记与 shell 提示符）。
	Text string `json:"text"`
	// Risk 风险等级：high / medium / 空（未识别到风险）。
	Risk string `json:"risk"`
	// Note 风险说明，仅 Risk 非空时有值。
	Note string `json:"note"`
}

// ScenarioSegment 回答中的一段内容。
//
// 为什么要分段而不是「一段说明 + 一堆命令」两个字段：
// 模型的输出是交错的——先解释、再给命令、再补充说明。
// 若把说明全堆在顶部、命令全放底部，用户会失去「哪条命令对应哪段解释」的对应关系，
// 也看不出模型原本的叙述顺序。分段后前端按顺序渲染，与模型输出一致。
type ScenarioSegment struct {
	// Type 段落类型：text 说明文字 / cmd 命令。
	Type string `json:"type"`
	// Text 说明文字原文（Type=text 时有值，保留 markdown 语法交前端渲染）。
	Text string `json:"text"`
	// Commands 命令（Type=cmd 时有值）。
	Commands []ScenarioCommand `json:"commands"`
}

// RiskRule 一条风险规则。
type RiskRule struct {
	// Pattern 匹配命令文本。
	Pattern *regexp.Regexp
	// Level 风险等级：high 不可逆/影响整机，medium 影响服务或数据。
	Level string
	// Note 给用户看的风险说明。
	Note string
}

// RiskRules 风险规则表。
//
// 顺序不代表优先级：判定取最高等级的那条（见 Eval），因此新增规则可以直接追加，
// 不必担心插错位置导致等级被降级。
type RiskRules []RiskRule

// Eval 判定单条命令的风险等级与说明。
//
// 等级和说明在同一个函数里产出，不拆成 classifyRisk / riskNote 两个：
// 拆开时两边的优先级判定一旦写得不一样，界面就会出现「红框高风险卡片却配了
// 一条中等风险的说明」这种自相矛盾的展示——而这两处是分开被调用的，
// 编译期发现不了。合起来后，说明必定来自被选中等级的那条规则。
//
// 取最高等级而不是第一条命中的规则：规则表是分批追加的，
// 书写顺序不构成优先级（一条命令可能同时命中多条规则）。
//
// 只给一条说明而不是全部：命令卡片只有一行位置，
// 列三条风险说明反而让人看不出哪条最要紧。
//
// 规则表为空时返回空等级与空说明——纯文本类场景（翻译/摘要）不需要风险标注。
func (r RiskRules) Eval(cmd string) (level string, note string) {
	var medium, mediumNote string
	for _, rule := range r {
		if !rule.Pattern.MatchString(cmd) {
			continue
		}
		if rule.Level == "high" {
			// 最高级，没有更高的了，直接返回。
			return rule.Level, rule.Note
		}
		if medium == "" {
			medium, mediumNote = rule.Level, rule.Note
		}
	}
	return medium, mediumNote
}

// fenceOpenerRe 识别围栏代码块的开标记行。
//
// 为什么不用「一个正则匹配整块」：模型输出的围栏有三种不稳定形态——
//   - 未闭合（被 max_tokens 截断，或模型自己忘了写收尾 ```)
//   - 用 ~~~ 而不是 ```
//   - 语言标注五花八门（bash/sh/shell/zsh/console/shell-session/无标注）
//
// 配对正则 ```([\s\S]*?)``` 对前两种直接失效：未闭合时一条命令都解析不出来，
// 用户看到的就是「模型明明给了命令，界面上却空空如也」。
// 因此改成逐行扫描 + 状态机：只要看到开标记就进入围栏态，
// 遇到匹配的收尾则闭合，遇到不匹配的开标记则继续吞入（兼容嵌套围栏内容），
// 直到文本结束仍未闭合也照样把已收集内容当成一个代码块——
// 命令宁可多显示一条，也不能整条消失。
var fenceOpenerRe = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})\\s*([A-Za-z0-9_+-]*)")

// ExtractSegments 按模型输出的原始顺序切分出「说明文字」与「命令」两类分段。
//
// 与 ExtractCommands 的区别：后者是扁平列表（只要命令，不管位置），
// 前者保留位置信息，让前端能把说明和命令交错渲染——
// 模型输出本来就是交错的（先解释 → 给命令 → 再补充），
// 拆成"说明全在上、命令全在下"会丢掉"哪条命令对应哪段解释"的对应关系。
//
// 围栏识别是本包里判定"什么算命令"的唯一实现：
// ExtractCommands 直接复用本函数的结果，前端 utils/aiSegments.ts 也按
// 同一套规则实现。三处判定不一致会导致同一条命令在正文和卡片里各出现一次。
//
// rules 为空时命令不带风险标注（纯文本类场景用）。
func ExtractSegments(text string, rules RiskRules) []ScenarioSegment {
	lines := strings.Split(text, "\n")
	var segments []ScenarioSegment
	var buf []string
	var fenceLines []string
	var inFence bool
	var fenceChar byte
	var fenceLen int
	seen := map[string]bool{}

	flushText := func() {
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		if body == "" {
			return
		}
		segments = append(segments, ScenarioSegment{Type: "text", Text: body})
	}

	flushCmd := func() {
		body := strings.TrimSpace(strings.Join(fenceLines, "\n"))
		fenceLines = nil
		if body == "" {
			return
		}
		cmds := parseFenceCommands(body, seen, rules)
		if len(cmds) == 0 {
			return
		}
		segments = append(segments, ScenarioSegment{Type: "cmd", Commands: cmds})
	}

	for _, line := range lines {
		m := fenceOpenerRe.FindStringSubmatch(line)
		if m == nil {
			if inFence {
				fenceLines = append(fenceLines, line)
			} else {
				buf = append(buf, line)
			}
			continue
		}
		mark := m[1]
		if !inFence {
			flushText()
			inFence = true
			fenceChar = mark[0]
			fenceLen = len(mark)
			continue
		}
		if mark[0] == fenceChar && len(mark) >= fenceLen {
			flushCmd()
			inFence = false
			continue
		}
		fenceLines = append(fenceLines, line)
	}
	// 收尾：未闭合围栏也要把命令救回来。
	if inFence {
		flushCmd()
	}
	flushText()
	return segments
}

// ExtractCommands 从模型输出里抽出命令并打风险标签。
//
// 不解析、不执行、只做文本提取——这是本文件最重要的约束。
//
// 复用 ExtractSegments 的分段结果，保证「扁平命令列表」与「有序分段」
// 对同一段文字的判定完全一致：否则前端按分段渲染、后端按扁平列表算风险时，
// 会出现同一条命令在两处的存在性判断打架。
func ExtractCommands(text string, rules RiskRules) []ScenarioCommand {
	out := make([]ScenarioCommand, 0, 4)
	for _, seg := range ExtractSegments(text, rules) {
		if seg.Type == "cmd" {
			out = append(out, seg.Commands...)
		}
	}
	return out
}

// shellPromptRe 匹配行首的 shell 提示符（`$` 普通用户 / `#` root）后跟空白。
//
// 模型偶尔按"贴终端实录"的写法给命令，这两种前缀都不是命令的一部分：
//   - 带 `$ ` 发出去，远端会把 `$` 当成命令名报 `command not found`，
//     用户看到的就是「AI 给的命令是坏的」；
//   - `#` 开头的行若一律当注释跳过，模型整段用 root 提示符写法时
//     命令会全部消失——正是这个功能最不该出现的结果。
//
// 要求前缀后必须有空白：`$HOME/bin/x`、`#!/bin/bash` 这类不该被当成提示符。
var shellPromptRe = regexp.MustCompile(`^[$#][ \t]+(.*)$`)

// commandTokenRe 判断一行"像不像一条命令"：首个 token 得是可执行文件名的常见形状。
//
// 只在兜底回扫里用（见 parseFenceCommands）：那时 `# ` 既可能是 root 提示符
// 也可能真是注释，靠它把「# 这是一段说明」这类中文注释挡掉，
// 避免把注释当成命令发给远端。正常扫描不加这道过滤——
// 命令的写法太杂（`./configure`、`(cd /tmp && ls)`），一过滤就会误杀真命令。
var commandTokenRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+`)

// parseFenceCommands 把一个围栏块的正文解析成命令列表。
//
// 两遍扫描，第二遍只在必要时发生：
//  1. 正常遍：剥掉 `$ ` 提示符，`#` 开头按代码块的常规语义当作注释跳过。
//  2. 兜底遍：仅当第一遍一条命令都没得到、且块内每一行都以 `# ` 开头时触发。
//     那种形态说明模型整段用了 root 提示符写法，一律当注释会让命令全部消失；
//     而只要块里还有一行不带前缀，就说明它是常规代码块，`#` 就是注释——
//     所以这个条件必须同时满足，缺一个都会把注释变成命令。
func parseFenceCommands(body string, seen map[string]bool, rules RiskRules) []ScenarioCommand {
	lines := strings.Split(body, "\n")
	cmds := collectFenceCommands(lines, false, seen, rules)
	if len(cmds) == 0 && allPromptLines(lines, "#") {
		cmds = collectFenceCommands(lines, true, seen, rules)
	}
	return cmds
}

// collectFenceCommands 扫一遍行，产出命令。
//
// rescueHash=true 时把 `# ` 当作 root 提示符剥离（并额外用 commandTokenRe 过滤）；
// 否则 `# ` 一律视为注释。
func collectFenceCommands(lines []string, rescueHash bool, seen map[string]bool, rules RiskRules) []ScenarioCommand {
	var cmds []ScenarioCommand
	for _, line := range lines {
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		if m := shellPromptRe.FindStringSubmatch(cmd); m != nil {
			// 只有 rescueHash 时才把 `# ` 当提示符；`$ ` 在任何一遍都剥。
			if cmd[0] == '#' && !rescueHash {
				continue
			}
			cmd = strings.TrimSpace(m[1])
		}
		// 未跟空白的 `#`（如 #!/bin/bash、#注释）在任何一遍都是注释。
		if cmd == "" || strings.HasPrefix(cmd, "#") {
			continue
		}
		if rescueHash && !commandTokenRe.MatchString(cmd) {
			continue
		}
		if seen[cmd] {
			continue
		}
		seen[cmd] = true
		level, note := rules.Eval(cmd)
		cmds = append(cmds, ScenarioCommand{
			Text: cmd,
			Risk: level,
			Note: note,
		})
	}
	return cmds
}

// allPromptLines 判断块内每个非空行是否都以指定提示符 + 空白开头。
func allPromptLines(lines []string, marker string) bool {
	n := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		n++
		if !strings.HasPrefix(l, marker+" ") && !strings.HasPrefix(l, marker+"\t") {
			return false
		}
	}
	return n > 0
}

// TrimRunes 把字符串裁到最多 max 个 rune。
//
// keepTail=true 时保留尾部（最新的内容），false 时保留头部。
// 裁到边界后不切开一行：半行会让模型误读结构（命令被截成 "systemctl stat"）。
func TrimRunes(s string, max int, keepTail bool) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	if !keepTail {
		return string(rs[:max])
	}
	tail := string(rs[len(rs)-max:])
	// 只删整行，避免半行乱码让模型误读。
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		tail = tail[i+1:]
	}
	return tail
}
