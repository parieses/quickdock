package ai

// 任务收尾的「工程笔记」：一次模型调用，同时产出两样东西。
//
//  1. 当天的**工程笔记**（写进 memory/YYYY-MM-DD.md 对应任务小节）：
//     需求 / 做了什么 / 结论 / 坑 / 门禁。这是「下次要复用」的那部分知识。
//  2. 值得长期记住的**常青条目**（写进 MEMORY.md 的硬规则 / 坑 / 事实 / 决策）。
//
// 为什么两地都写：原来的每日记忆只有「用户说了什么、改了几个文件、跑了几轮」，
// 结论和根因一个字都没有——下次遇到同一个坑，记忆帮不上任何忙，
// 用户看到的就是「这记忆很简陋」。而 MEMORY.md 如果把这些细节全塞进去，
// 6000 字的注入上限很快会被过程记录占满，真正该一眼看到的规则反而被裁掉。
// 所以：过程与细节进每日笔记，结论与规则进 MEMORY.md。
//
// 三次返工留下的三条硬约束（别再违反）：
//   - **永不阻塞对话**：跑在 goroutine 里，失败只记日志。记忆整理失败不该让回答发不出去。
//   - **解析宁严勿宽**：只认固定标记块与 `- ` 行，模型顺手写的解释、代码块全部丢掉。
//     宁可这次少记一条，也不要往记忆里灌一段「以下是提炼结果」。
//   - **不写空话**：两块都可能是 NONE；笔记为空就只更新 MEMORY.md，反之亦然。
//
// 开关是 ai_memory_auto（默认开）。关掉之后只剩确定性的活动日志。

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

const (
	// aiMemoryAutoKey 设置项：""/"1" 开，"0" 关。
	aiMemoryAutoKey = "ai_memory_auto"

	// memoryNoteTimeout 单次整理调用的超时。编码任务的最终回答可能很长，
	// 输入裁剪之后仍然要给它几十秒——但不能无限等：goroutine 会一直挂着。
	memoryNoteTimeout = 60 * time.Second
	// memoryNoteMaxTokens 输出上限。两块内容加起来大约 12 行，900 够用，
	// 也给不出「写成一篇总结」的空间。
	memoryNoteMaxTokens = 900

	// memoryNoteUserRune 用户诉求上限（太长的提问人自己都记不住）。
	memoryNoteUserRune = 900
	// memoryNoteReplyRune 最终回答上限：要的是结论，不是全文。
	memoryNoteReplyRune = 5000
	// memoryNoteExistingRune 喂进去的现有 MEMORY.md 上限。
	//
	// 给到 5000（文件上限是 7000）：模型要能用「[取代]」改掉旧条目，就得先看见它。
	// 截得太狠会让它以为没记过，于是又追加一条新的——正是「功能改了、记忆没改」
	// 那个毛病的来源。代价是每次整理多花约 1500 token，换的是记忆不会自相矛盾。
	memoryNoteExistingRune = 5000
	// memoryNoteToolMaxLines 工具活动摘要最多几行。
	memoryNoteToolMaxLines = 18
	// memoryNoteFilesMax 改动文件最多列几个。
	memoryNoteFilesMax = 10
	// memoryNoteLineRune 单行笔记的长度上限。
	memoryNoteLineRune = 140
	// memoryNoteLineMax 单份笔记最多几行。
	memoryNoteLineMax = 6
	// memoryEntryMax 单次最多写几条常青条目。
	//
	// 6 而不是 4：一次改动常常是「2 条新事实 + 1 条取代 + 1 条作废」，
	// 卡在 4 会让模型自己删掉最该留的那条。
	memoryEntryMax = 6
	// memoryNoteMinUserRune 太短的提问不值得单独跑一次调用（「继续」「谢谢」）。
	memoryNoteMinUserRune = 12

	// memoryNoteMinReplyRune 纯问答（没调工具、没动文件）的回答长度门槛。
	//
	// 记忆整理是每次提问后的一次**冷启动**模型调用：prompt 与主对话完全不同，
	// 前缀缓存零命中，是一笔固定开销。动过文件、调过工具的任务值得记；
	// 一问一答里只有「答案本身就有长期价值」的才值得——回不到几百字的
	// 问答（怎么导出、在哪设置）结论早就留在对话里了，再花一次调用把
	// 它抄进记忆纯属浪费。门槛取 400 字：一句话能说清的不配占用整理名额。
	memoryNoteMinReplyRune = 400
)

// memoryAutoOn 读开关。读不到设置时按「开」处理：默认行为应该是用户要的那个。
func (a *AIService) memoryAutoOn() bool {
	if a == nil || a.App == nil || a.App.DB == nil {
		return false
	}
	raw, err := a.App.DB.GetSetting(aiMemoryAutoKey)
	if err != nil {
		return true
	}
	return strings.TrimSpace(raw) != "0"
}

// AIGetMemoryAuto 返回「自动整理记忆」是否开启。
func (a *AIService) AIGetMemoryAuto() *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	return services.Ok(a.memoryAutoOn())
}

// AISetMemoryAuto 开关「自动整理记忆」。
//
// 用 bool 而不是字符串：这个开关只有开/关两态，前端拼错一个单词不该悄悄变成
// 「关闭」——那会表现为「我明明开了它还是不记」。
func (a *AIService) AISetMemoryAuto(on bool) *services.ApiResult {
	if r := a.dbOK(); r != nil {
		return r
	}
	v := "1"
	if !on {
		v = "0"
	}
	if err := a.App.DB.SetSetting(aiMemoryAutoKey, v); err != nil {
		return services.Fail(err)
	}
	return services.Ok(on)
}

// taskMemoryInput 一次任务收尾要整理的全部素材。//
// 用结构体而不是十个参数：这些字段是「从 agent_run 的收尾现场搬过来」的，
// 后续要加素材（比如报错原文）时，加字段不会牵动调用链上的每一处。
type taskMemoryInput struct {
	ConvID string
	// MessageID 这次提问最后一条助手消息：记忆整理的用量要并进它，
	// 否则「输入框下方的 token 用量」永远不含这笔开销（见 recordMemoryUsage）。
	MessageID string
	// SessionTag 这次提问的网关会话标识（见 ai_session_tag.go）。
	//
	// 记忆整理自己造 ctx（要单独的超时），不带上这个标识的话，它就成了网关
	// 里那条「未提供会话标识」的黑户——而它每次带几千字已有记忆，token 不小。
	SessionTag sessionInfo
	Cwd        string
	Title      string
	User       string
	Reply      string
	Files      []string
	Rounds     int
	Tools      int
	ToolLines  []string
}

// summarizeToolActivity 把工具调用记录压成几行「做了什么」。
//
// 只留名字 + 目标 + 报错：这次整理要判断的是「这个任务做成了什么」，
// 不需要完整参数与输出（那些在最终回答里已经复述过一遍）。
func summarizeToolActivity(records []aiToolRecord, max int) []string {
	if max <= 0 {
		max = memoryNoteToolMaxLines
	}
	lines := make([]string, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, rec := range records {
		if rec.Dedup || rec.Denied {
			continue
		}
		line := strings.TrimSpace(rec.Name)
		if line == "" {
			continue
		}
		if t := toolMemoryTarget(rec); t != "" {
			line += " " + t
		}
		if rec.Error != "" {
			line += "（失败：" + clipRunes(collapseSpaces(rec.Error), 60) + "）"
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	if len(lines) > max {
		// 折叠的是「被省掉的调用」，不是调用总数：写总数会让模型以为这次跑了那么多命令。
		lines = append(lines[:max:max], fmt.Sprintf("…还有 %d 条调用", len(lines)-max))
	}
	return lines
}

// toolMemoryTarget 从一条调用记录里抠出「对象」：文件路径优先，其次是命令名。
func toolMemoryTarget(rec aiToolRecord) string {
	if p := strings.TrimSpace(rec.Path); p != "" {
		return collapseSpaces(p)
	}
	args := strings.TrimSpace(rec.Args)
	if args == "" {
		return ""
	}
	// 参数是模型给的 JSON 原文，可能被截断：只在能抠到明显字段时才用，
	// 抠不到就只显示工具名——总比在记忆里留一段半截 JSON 好。
	for _, key := range []string{`"path":"`, `"command":"`, `"pattern":"`, `"query":"`} {
		if i := strings.Index(args, key); i >= 0 {
			rest := args[i+len(key):]
			if j := strings.Index(rest, `"`); j > 0 {
				return clipRunes(collapseSpaces(rest[:j]), 80)
			}
		}
	}
	return ""
}

// memoryAutoSlots 同时在跑的记忆整理上限（2 = 一个前台任务 + 一个后台留守）。
//
// 为什么要有上限：每次整理都是一次完整计费调用，而且输入里还带着几千字已有
// 记忆。没有上限时，连点几次「结束」或几个短任务接连收尾就会并发发出好几份
// 账单，而它们写的是同一个 MEMORY.md。
var memoryAutoSlots = make(chan struct{}, 2)

// memoryAutoInflight 正在整理的「会话|消息」，防同一条回复被整理多次。
var memoryAutoInflight sync.Map

// writeTaskMemoryAsync 任务收尾后，把这次任务的笔记与常青条目丢到后台整理。
//
// 传 cfg 的值而不是指针：用户可能在整理期间切档案 / 改配置，而这次调用属于
// 已经结束的那一轮，应该用当时的模型与参数。
func (a *AIService) writeTaskMemoryAsync(cfg AIProfile, in taskMemoryInput) {
	if strings.TrimSpace(in.Cwd) == "" || strings.TrimSpace(in.Reply) == "" {
		return
	}
	// 纯寒暄 / 续跑口令不值得一次完整计费调用。动了文件或调了工具的照整理：
	// 那往往就是「继续做」的后半段，跳过它等于那半段永远进不了记忆。
	// 纯问答（没调工具、没动文件）还要回答够长才整理：短问答的结论留在
	// 对话里就行，每次都为它跑一次零缓存命中的整理调用不划算（见常量说明）。
	if in.Rounds == 0 && len(in.Files) == 0 {
		u := utf8.RuneCountInString(strings.TrimSpace(in.User))
		r := utf8.RuneCountInString(strings.TrimSpace(in.Reply))
		if u < memoryNoteMinUserRune || r < memoryNoteMinReplyRune {
			return
		}
	}
	if !a.memoryAutoOn() {
		return
	}
	sc := resolveMemoryScope(in.Cwd)
	if sc.root == "" {
		return
	}
	// 同一条回复只整理一次：用户连点几次「结束」、或前端重连触发二次收尾时，
	// 每次都会走到这里，而每次都是一份完整计费调用（还带 5000 字已有记忆）。
	inflightKey := in.ConvID + "|" + in.MessageID
	if _, busy := memoryAutoInflight.LoadOrStore(inflightKey, struct{}{}); busy {
		logger.I("[ai] 同一任务已在整理记忆，跳过重复 conv=%s msg=%s", in.ConvID, in.MessageID)
		return
	}
	go func() {
		defer memoryAutoInflight.Delete(inflightKey)
		// 并发上限之外直接跳过，不排队：排队只会在内存里攒起一串等待的
		// goroutine，而它们醒来时记忆早就被前面那几份改过了。
		select {
		case memoryAutoSlots <- struct{}{}:
			defer func() { <-memoryAutoSlots }()
		default:
			logger.I("[ai] 记忆整理并发已满（%d），本次跳过 conv=%s", cap(memoryAutoSlots), in.ConvID)
			return
		}
		// 后台协程里 panic 会带走整个进程：一次摘要失败不值得让桌面应用退出。
		defer func() {
			if r := recover(); r != nil {
				logger.W("[ai] 工程笔记 panic conv=%s: %v", in.ConvID, r)
			}
		}()
		a.writeTaskMemory(cfg, sc, in)
	}()
}

// writeTaskMemory 真正干活的那一次调用（同步，方便单测覆盖解析与落盘）。
func (a *AIService) writeTaskMemory(cfg AIProfile, sc memoryScope, in taskMemoryInput) {
	if sc.root == "" {
		return
	}
	existing := ""
	if data, rerr := os.ReadFile(overallMemoryPath(sc.root)); rerr == nil {
		existing = strings.TrimSpace(string(data))
		if isPlaceholderProfile(existing) {
			// 自动生成的空画像不是记忆：既别喂给模型（它会照着抄），也别拿来去重。
			existing = ""
		}
	}

	memCfg := aiCheapProfile(cfg)
	memCfg.MaxTokens = memoryNoteMaxTokens
	memCfg.Temperature = 0.2

	ctx, cancel := context.WithTimeout(context.Background(), memoryNoteTimeout)
	defer cancel()
	// 把提问的会话标识带进去：这次整理属于哪一次提问，网关上要能看出来。
	ctx = withSessionInfo(ctx, in.SessionTag)
	out, usage, err := a.callAIOnce(ctx, memCfg, []map[string]string{
		{"role": "system", "content": taskMemorySystemPrompt(sc)},
		{"role": "user", "content": taskMemoryUserPrompt(in, existing)},
	})
	if err != nil {
		logger.W("[ai] 工程笔记调用失败 conv=%s: %v", in.ConvID, err)
		return
	}
	// 这也是一次完整计费的调用，不记就会系统性少算（同标题生成）。
	a.recordAIUsage(in.ConvID, usageKindMemory, memCfg, usage)
	a.recordMemoryUsage(in, usage)

	note, entries := parseTaskMemoryOutput(out)
	if len(note) > 0 {
		appendDailyNote(in.ConvID, in.Cwd, in.Title, in.User, time.Now(), note)
	}
	if len(entries) > 0 {
		if werr := updateOverallMemory(sc.root, entries); werr != nil {
			logger.W("[ai] 写入记忆失败 root=%s: %v", sc.root, werr)
		}
	}
}

// recordMemoryUsage 把记忆整理的用量并进那条回复，并推一条事件让前端立刻算进
// 「输入框下方的 token 用量」。
//
// 两个落点缺一不可：并进消息行是为了**刷新后仍然算得对**（前端那个数字是把消息行
// 求和出来的），事件是为了**本次会话不必等刷新**——记忆整理在回答结束之后才跑完，
// 等下次打开会话才更新，用户会以为自己记错了。
func (a *AIService) recordMemoryUsage(in taskMemoryInput, u db.TokenUsage) {
	if u.Empty() {
		return
	}
	if in.MessageID != "" && a.App != nil && a.App.DB != nil {
		ok, err := a.App.DB.AddAIMessageUsage(in.MessageID, u)
		switch {
		case err != nil:
			logger.W("[ai] 记忆用量并进消息失败 msg=%s: %v", in.MessageID, err)
		case !ok:
			// 目标消息行已经不在了。总账（ai_usage）已经记过，这里只是不再
			// 往前端推那条增量——推了会让本次会话的数字虚高，刷新后又消失。
			logger.I("[ai] 记忆用量目标消息已不存在，跳过事件 msg=%s", in.MessageID)
			return
		}
	}
	a.emitAI("ai:memory-usage", map[string]interface{}{
		"convId":     in.ConvID,
		"messageId":  in.MessageID,
		"prompt":     u.Prompt,
		"completion": u.Completion,
		"cached":     u.Cached,
	})
}

// taskMemorySystemPrompt 工程笔记的指令。
//
// 两个作用域分开写：项目里该记的是技术栈与坑，跨任务的共享记忆里该记的是
// 「这个人是谁、偏好什么、上次给了什么结论」。
func taskMemorySystemPrompt(sc memoryScope) string {
	what := "用户的偏好与禁忌、他在做什么事、明确要求过的做法、你给出的结论与决定、踩过的坑"
	if sc.project {
		what = "这个项目的事实：技术栈、关键文件路径、约定、踩过的坑、已经做过的决策"
	}
	return "你是工程记忆整理器。根据「本次任务」输出两块内容，用固定标记包裹。\n\n" +
		"输出格式（严格遵守，不要写任何别的东西）：\n" +
		"<笔记>\n" +
		"- 需求：用户要解决什么（一句话）\n" +
		"- 做了什么：改了哪些文件/模块、关键做法（一到两行）\n" +
		"- 结论：最终结果或验证到的结论\n" +
		"- 坑：踩到的坑与根因（没有就省略这一行）\n" +
		"- 门禁：跑过的验证命令与结果（没有就省略这一行）\n" +
		"</笔记>\n" +
		"<长期>\n" +
		"- [规则] 一句话规则 ｜ 为什么：违反它会出什么事\n" +
		"- [坑] 现象 ｜ 根因：… ｜ 修法：…\n" +
		"- [事实] 一句话事实\n" +
		"- [决策] 已拍板的取舍 ｜ 理由：…\n" +
		"- [取代] 旧条目的开头几个字 → 改后的完整内容\n" +
		"- [作废] 旧条目的开头几个字\n" +
		"</长期>\n\n" +
		"约束：\n" +
		"- 笔记写 3-" + fmt.Sprint(memoryNoteLineMax) + " 行，每行不超过 80 字，只写这次任务的真实产出；" +
		"不要复述寒暄与过程流水账，不要贴代码，不要写 Markdown 标题或代码块。\n" +
		"- 长期最多 " + fmt.Sprint(memoryEntryMax) + " 条，只写「以后的轮次还会用到」的东西；" +
		"确实没有就整块只写一行 NONE。\n" +
		"- **这次改动让「已有长期记忆」里的某条不再成立时（功能行为变了、方案换了、结论被推翻），" +
		"必须用 [取代] 把那条改掉，而不是再追加一条新的**——两条互相矛盾的记忆比没有记忆更糟。" +
		"[取代] 后面的旧片段要能唯一定位到那一行（复制它开头十来个字即可）。\n" +
		"- [规则] 必须写清为什么：写不出理由的，说明它还不是规则，别写。\n" +
		"- 只写结论，不要「我检查了/我尝试了」这类叙述。\n" +
		"- 「已有长期记忆」里已经有的不要重复，也不要换个说法再写一遍。\n" +
		"- 该记的是：" + what + "。\n" +
		"- 不该记的是：一次性的问题与寒暄、临时路径、代码内容、这次跑了哪几条命令的细节。"
}

// taskMemoryUserPrompt 拼「本次任务」素材。
func taskMemoryUserPrompt(in taskMemoryInput, existing string) string {
	var b strings.Builder
	b.WriteString("【任务】")
	b.WriteString(clipRunes(collapseSpaces(in.Title), 80))
	b.WriteString("\n【用户】")
	b.WriteString(clipRunes(strings.TrimSpace(in.User), memoryNoteUserRune))
	b.WriteString("\n")
	if len(in.ToolLines) > 0 {
		b.WriteString("\n【工具活动】\n")
		for _, l := range in.ToolLines {
			b.WriteString("- " + l + "\n")
		}
	}
	if files := dedupKeepOrder(in.Files); len(files) > 0 {
		if len(files) > memoryNoteFilesMax {
			files = files[:memoryNoteFilesMax]
		}
		b.WriteString("\n【改动文件】" + strings.Join(files, "、") + "\n")
	}
	if in.Rounds > 0 || in.Tools > 0 {
		fmt.Fprintf(&b, "\n【投入】%d 轮 / %d 次调用\n", in.Rounds, in.Tools)
	}
	b.WriteString("\n【最终回答】\n")
	b.WriteString(clipRunes(strings.TrimSpace(in.Reply), memoryNoteReplyRune))
	b.WriteString("\n")
	b.WriteString("\n【已有长期记忆】\n")
	if existing == "" {
		b.WriteString("（空）\n")
	} else {
		b.WriteString(clipRunes(existing, memoryNoteExistingRune))
		b.WriteString("\n")
	}
	return b.String()
}

// ---- 解析模型输出 ----

// parseTaskMemoryOutput 把模型输出拆成「笔记行」与「常青条目」。
//
// 标记缺失时的兜底是**只取笔记、不写 MEMORY.md**：模型没按格式输出说明这次
// 它没打算分类，把一堆无法归类的长句塞进常青记忆，比少记一次糟糕得多。
func parseTaskMemoryOutput(raw string) (note []string, entries []memoryEntry) {
	noteBlock, okNote := extractMarkedBlock(raw, "笔记")
	longBlock, okLong := extractMarkedBlock(raw, "长期")
	if !okNote && !okLong {
		// 有开标签却没闭合：基本是撞了 memoryNoteMaxTokens 被截断。半截块的
		// 最后一行通常是被切开的残句，硬解析等于把残句永久钉进记忆文件。
		// 整份丢弃，下一次任务结束时再整理。
		if strings.Contains(raw, "<笔记>") || strings.Contains(raw, "<长期>") {
			logger.W("[ai] 工程笔记输出被截断（标签未闭合），本次不写记忆：%d 字",
				utf8.RuneCountInString(raw))
			return nil, nil
		}
		// 完全没标记：整段当作笔记候选（仍然只认 `- ` 行）。
		noteBlock, okNote = raw, true
	}
	if okNote {
		note = parseNoteLines(noteBlock)
	}
	if okLong {
		entries = parseMemoryEntryLines(longBlock)
	}
	return note, entries
}

// extractMarkedBlock 取 `<标签>…</标签>` 之间的内容。
func extractMarkedBlock(raw, tag string) (string, bool) {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	i := strings.Index(raw, open)
	if i < 0 {
		return "", false
	}
	rest := raw[i+len(open):]
	j := strings.Index(rest, closeTag)
	if j < 0 {
		// 未闭合 = 不是「有内容的块」，而是被截断的半截输出。
		// 当成有内容交给解析器，会产出以残句结尾的条目（见调用点的处理）。
		return "", false
	}
	return rest[:j], true
}

// noteLinePrefixRe 去掉笔记里的项目符号（含全角、编号）。
var noteLinePrefixRe = regexp.MustCompile(`^([-*•·]|\d+[.、)]|第[一二三四五六七八九十]+[、.])\s*`)

// parseNoteLines 清洗笔记行：只留 `- ` 条目，去掉 Markdown 装饰，限长限量。
func parseNoteLines(block string) []string {
	out := make([]string, 0, memoryNoteLineMax)
	seen := make(map[string]bool, memoryNoteLineMax)
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "#") {
			continue
		}
		// 只认列表行：模型偶尔会先寒暄一句（「以下是提炼结果」），那行没有项目符号。
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") && !strings.HasPrefix(line, "• ") {
			continue
		}
		body := strings.TrimSpace(noteLinePrefixRe.ReplaceAllString(line, ""))
		if body == "" || isNoneText(body) || seen[body] {
			continue
		}
		seen[body] = true
		out = append(out, clipRunes(collapseSpaces(body), memoryNoteLineRune))
		if len(out) >= memoryNoteLineMax {
			break
		}
	}
	return out
}

// entryTagRe 匹配 `[规则] 正文`。
var entryTagRe = regexp.MustCompile(`^\[\s*([^\]]{1,12})\s*\]\s*(.+)$`)

// entryWordRe 匹配 `规则：正文` 这种没用方括号的写法（模型很常见）。
var entryWordRe = regexp.MustCompile(`^([^\s：:]{1,4})\s*[：:]\s*(.+)$`)

// parseMemoryEntryLines 解析常青条目：必须有可识别的类别标签。
//
// 没有标签的行一律丢弃——它不是「没分类」，而是「模型在写散文」。
func parseMemoryEntryLines(block string) []memoryEntry {
	out := make([]memoryEntry, 0, memoryEntryMax)
	seen := make(map[string]bool, memoryEntryMax)
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") && !strings.HasPrefix(line, "• ") {
			continue
		}
		body := strings.TrimSpace(noteLinePrefixRe.ReplaceAllString(line, ""))
		if body == "" || isNoneText(body) {
			continue
		}
		e, ok := parseMemoryEntryBody(body)
		if !ok {
			continue
		}
		key := normalizeMemoryText(e.Text)
		if e.Replace != "" {
			key = "replace:" + normalizeMemoryText(e.Replace)
		}
		if e.Drop != "" {
			key = "drop:" + normalizeMemoryText(e.Drop)
		}
		if key == "" || key == "replace:" || key == "drop:" || seen[key] {
			continue
		}
		seen[key] = true
		if e.Text != "" {
			e.Text = clipRunes(e.Text, memoryEntryMaxRune)
		}
		out = append(out, e)
		if len(out) >= memoryEntryMax {
			break
		}
	}
	return out
}

// parseMemoryEntryBody 把一行拆成条目（三种形态之一）。
func parseMemoryEntryBody(body string) (memoryEntry, bool) {
	if m := entryTagRe.FindStringSubmatch(body); m != nil {
		tag := m[1]
		rest := strings.TrimSpace(m[2])
		switch {
		case isReplaceWord(tag):
			// `[取代] 旧片段 → 改后的内容`：箭头前是定位旧条目的片段，箭头后是新内容。
			// 旧条目可能跨分区，类别由「找到的那条」决定，所以这里不填 Category。
			old, text := splitEntryArrow(rest)
			if strings.TrimSpace(old) == "" {
				return memoryEntry{}, false
			}
			if strings.TrimSpace(text) == "" {
				// 只给了旧片段没有新内容：当作用一条「作废」处理，别写出一条空条目。
				return memoryEntry{Drop: old}, true
			}
			return memoryEntry{Replace: strings.TrimSpace(old), Text: strings.TrimSpace(text)}, true
		case isDropWord(tag):
			if rest == "" {
				return memoryEntry{}, false
			}
			return memoryEntry{Drop: rest}, true
		}
		cat, ok := memoryCategoryFromWord(tag)
		if !ok {
			return memoryEntry{}, false
		}
		return memoryEntry{Category: cat, Text: rest}, true
	}
	if m := entryWordRe.FindStringSubmatch(body); m != nil {
		if cat, ok := memoryCategoryFromWord(m[1]); ok {
			return memoryEntry{Category: cat, Text: strings.TrimSpace(m[2])}, true
		}
	}
	return memoryEntry{}, false
}

// splitEntryArrow 按第一个箭头切成「旧片段 / 新内容」。
//
// 不能切最后一个箭头：改后的内容里本身可能带箭头（「A → B」这种流程描述），
// 从后面切会把旧片段切进新内容里。
func splitEntryArrow(s string) (string, string) {
	for _, sep := range []string{"→", "->", "⇒", "=>"} {
		if i := strings.Index(s, sep); i >= 0 {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):])
		}
	}
	return "", ""
}

// isReplaceWord 这条是在「改掉旧的」。
func isReplaceWord(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	for _, w := range []string{"取代", "更新", "替换", "修正", "replace", "update", "supersede"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// isDropWord 这条是在「作废旧的」。
func isDropWord(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	for _, w := range []string{"作废", "删除", "废弃", "移除", "drop", "remove", "obsolete"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// memoryCategoryFromWord 只在词面明确对应某个分区时才返回（与 normalizeMemoryCategory
// 的区别：后者对认不出来的词会兜底到「项目事实」，这里必须能说「我不认识」）。
func memoryCategoryFromWord(word string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(word))
	switch {
	case strings.Contains(c, "规则"), strings.Contains(c, "rule"), strings.Contains(c, "约束"):
		return sectionRules, true
	case strings.Contains(c, "坑"), strings.Contains(c, "pitfall"), strings.Contains(c, "故障"), strings.Contains(c, "bug"):
		return sectionPits, true
	case strings.Contains(c, "决策"), strings.Contains(c, "decision"), strings.Contains(c, "取舍"):
		return sectionDecisions, true
	case strings.Contains(c, "事实"), strings.Contains(c, "fact"), strings.Contains(c, "技术栈"):
		return sectionFacts, true
	}
	return "", false
}

// isNoneText 模型表示「没有」的几种写法。
func isNoneText(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.Trim(t, "。.!！:：")
	switch t {
	case "none", "n/a", "na", "无", "没有", "无内容", "（无）", "(无)", "-", "空":
		return true
	}
	return false
}
