package ai

// 自动整理记忆：一轮对话结束后，用一次小调用把「以后还用得上」的事摘进 MEMORY.md。
//
// 为什么非要有这一步：确定性的每日日志（见 ai_memory.go 的 appendDailyMemory）
// 只记得住「用户问了什么、改了哪些文件」，而记忆真正值钱的是**结论、偏好、踩过的坑**
// ——哪些值得记需要判断，机器归纳不了。于是每轮多花一次很便宜的调用：输入是
// 本轮对话 + 现有记忆，输出最多几条 `- ` 条目。
//
// 三条硬约束：
//   - **永不阻塞对话**：跑在 goroutine 里，失败只记日志。记忆整理失败不该让回答发不出去，
//     也不该让用户多等一秒。
//   - **不写空话**：模型输出 NONE 时一个字都不写；输出逐行过滤，只认 `- ` 开头的条目，
//     模型顺手写的标题、解释、代码块全部丢掉。
//   - **文件有界**：MEMORY.md 超过 memoryAutoFileCap 就从**最旧**的 `## ` 段丢起。
//     注入时是按 memoryCapOverall 从头截的，让它无限长等于把最近的记忆裁掉。
//
// 第四条是后来补的——**调用次数有界**：每次整理都是一次完整计费调用，早先「每轮
// 结束跑一次」让一次二十轮的编码任务多付二十次费用。现在按作用域攒批，攒够 3 轮
// 或距上次 ≥90 秒才真跑（见 consolidateMemoryAsync）。
//
// 开关是 ai_memory_auto（默认开）。关掉之后只剩每日日志，MEMORY.md 只由模型
// 在对话里主动调 project_memory 才会变——那正是用户抱怨的「记忆跟当前提问没关系」。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"quickdock/internal/logger"
	"quickdock/services"
)

const (
	// aiMemoryAutoKey 设置项：""/"1" 开，"0" 关。
	aiMemoryAutoKey = "ai_memory_auto"

	// memoryAutoUserRune 喂给整理器的用户消息上限（太长的提问人自己都记不住）。
	memoryAutoUserRune = 800
	// memoryAutoReplyRune 喂给整理器的助手回复上限：要的是结论，不是全文。
	memoryAutoReplyRune = 4000
	// memoryAutoExistingRune 喂给整理器的现有记忆上限：够它去重就行。
	memoryAutoExistingRune = 3000
	// memoryAutoFileCap MEMORY.md 的体积上限，超出从最旧的段落丢起。
	//
	// 必须 ≤ memoryCapOverall（注入时 clipRunes 从头截的上限）：文件比注入上限
	// 还长的话，被截掉的正好是文件末尾，也就是**最新**的那几段——与这里
	// 「宁可删旧的」的意图完全相反，症状是「刚记住的事下一轮就不见了」。
	memoryAutoFileCap = memoryCapOverall
	// memoryAutoBulletMax / memoryAutoBulletRune 单次最多写几条、每条多长。
	memoryAutoBulletMax  = 6
	memoryAutoBulletRune = 220
	// memoryAutoMinUserRune 太短的提问不值得单独跑一次整理（「继续」「谢谢」）。
	memoryAutoMinUserRune = 6

	// memoryAutoMinInterval 两次整理之间的最小间隔。
	//
	// 这是省钱的主力：早先是「每轮结束跑一次完整计费调用」，一次 20 轮的编码任务
	// 就是 20 次额外调用（每次输入 = 3000 字旧记忆 + 4000 字回复）。现在改成
	// 攒着一起整理，常规节奏下同样的任务只跑 2-3 次。
	memoryAutoMinInterval = 90 * time.Second
	// memoryAutoMinTurns 攒够几轮强制整理一次。
	//
	// 只有时间间隔不够：用户十分钟里连问八句短问题，八轮都落在 90 秒内就会一直
	// 攒着不整理，一旦进程退出这几轮的结论全丢。按轮数兜一道，最坏坏掉一轮。
	memoryAutoMinTurns = 3
)

// memoryBatch 待整理的一批轮次（一轮或多轮攒在一起）。
type memoryBatch struct {
	user  string
	reply string
	files []string
	turns int
}

// add 把一轮对话并进这一批，总量按注入上限截断。
//
// 超长时从**前面**截而不是后面：攒起来的几轮是同一件事的连续追问，结论基本
// 落在最后那轮；截尾等于把最该记的部分丢掉。
func (b *memoryBatch) add(user, reply string, files []string) {
	b.turns++
	b.user = appendCapped(b.user, user, memoryAutoUserRune)
	b.reply = appendCapped(b.reply, reply, memoryAutoReplyRune)
	b.files = append(b.files, files...)
}

// memoryScopeState 一个记忆作用域的攒批与在跑状态。
type memoryScopeState struct {
	pending memoryBatch
	// lastAt 上一次真正发起整理的时刻（零值 = 从没整理过，第一轮就该跑）。
	lastAt time.Time
	// running 该作用域已有一轮在跑：网络调用慢的时候不再叠加第二次，
	// 新来的轮次继续往 pending 里攒，下一次触发时一起喂进去。
	running bool
}

var (
	// memoryAutoStateMu 只保护下面那张 pending 表。
	//
	// 原先是「整理全程持一把全局锁」（锁在发请求前加、收到回复后才解），三个会话
	// 同时结束就串行 3×45 秒。现在这把锁只覆盖几次内存读写，网络调用期间不持锁。
	memoryAutoStateMu sync.Mutex
	memoryAutoStates  = map[string]*memoryScopeState{}

	// memoryAutoFileMu 串行化「读 MEMORY.md → 追加 → 裁回上限」这一整段。
	//
	// 这一段的正确性靠锁（各自读旧内容、各自覆盖就是丢条目），而它只有几次
	// 本地文件 IO，几十微秒，排队没有代价。锁下沉到这里之后，代价是去重依据
	// 可能来自几百毫秒前的快照——后果只是偶尔多一条重复条目，比串行 45 秒划算。
	memoryAutoFileMu sync.Mutex
)

// memoryAutoStateLocked 取（必要时创建）某个作用域的状态。调用方持 memoryAutoStateMu。
func memoryAutoStateLocked(root string) *memoryScopeState {
	st, ok := memoryAutoStates[root]
	if !ok {
		st = &memoryScopeState{}
		memoryAutoStates[root] = st
	}
	return st
}

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

// consolidateMemoryAsync 把这一轮对话攒进待整理批，够了就丢到后台整理一次。
//
// 传 cfg 的值而不是指针：用户可能在整理期间切档案 / 改配置，而这次调用属于
// 已经结束的那一轮，应该用当时的模型与参数。
//
// 触发条件是「攒够 memoryAutoMinTurns 轮 **或** 距上次整理 ≥ memoryAutoMinInterval」
// ——不满足就只入批不发请求。
//
// 「动过文件」**不**作为即时触发条件：编码类任务几乎每轮都改文件，拿它当触发
// 等于退回「每轮一次」，一分钱都省不下来。文件清单只是随那一轮内容一起攒进
// pending，下一次触发时一并喂给整理器。
func (a *AIService) consolidateMemoryAsync(cfg AIProfile, convID, cwd, user, reply string, files []string) {
	if strings.TrimSpace(cwd) == "" || strings.TrimSpace(reply) == "" {
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(user)) < memoryAutoMinUserRune && len(files) == 0 {
		// 「继续」「谢谢」这类话不单独跑一次整理——但**动了文件就照整理**：
		// 续跑往往就是同一件事的后半段，跳过它等于那半段永远进不了长期记忆。
		return
	}
	if !a.memoryAutoOn() {
		return
	}
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return
	}

	memoryAutoStateMu.Lock()
	st := memoryAutoStateLocked(sc.root)
	st.pending.add(user, reply, files)
	// 已经有一轮在跑就不再叠加：新内容留在 pending 里，等下一次触发一起喂。
	// 这里不排队等它跑完——等就是让「一轮结束」这件事被一次网络调用挡住。
	run := !st.running && (st.pending.turns >= memoryAutoMinTurns ||
		time.Since(st.lastAt) >= memoryAutoMinInterval)
	var batch memoryBatch
	if run {
		batch = st.pending
		st.pending = memoryBatch{}
		st.running = true
		st.lastAt = time.Now()
	}
	memoryAutoStateMu.Unlock()
	if !run {
		return
	}

	go func() {
		// 后台协程里 panic 会带走整个进程：一个摘要失败不值得让桌面应用退出。
		// running 的复位必须在这里做（不在 consolidateMemory 里）：那次调用提前
		// return 的路径太多，漏一条这个作用域就永远不再整理了。
		defer func() {
			memoryAutoStateMu.Lock()
			st.running = false
			memoryAutoStateMu.Unlock()
			if r := recover(); r != nil {
				logger.W("[ai] 记忆整理 panic conv=%s: %v", convID, r)
			}
		}()
		a.consolidateMemory(cfg, convID, sc, batch)
	}()
}

// consolidateMemory 真正干活的那一次调用（同步，能单测）。
//
// 不在整段调用期间持锁：读文件拿的是快照，网络调用更是别人的时间。
// 写回时由 appendOverallMemory 自己加锁（见那里的说明）。
func (a *AIService) consolidateMemory(cfg AIProfile, convID string, sc memoryScope, batch memoryBatch) {
	if sc.root == "" {
		return
	}
	existing := ""
	if data, err := os.ReadFile(overallMemoryPath(sc.root)); err == nil {
		existing = strings.TrimSpace(string(data))
		if isPlaceholderProfile(existing) {
			// 自动生成的空画像不是记忆：既别喂给模型（它会照着抄），也别拿来去重。
			existing = ""
		}
	}

	var b strings.Builder
	b.WriteString("【已有长期记忆】\n")
	if existing == "" {
		b.WriteString("（空）\n")
	} else {
		b.WriteString(clipRunes(existing, memoryAutoExistingRune))
		b.WriteString("\n")
	}
	// 攒了多轮时用「最近的对话」而不是「本轮」：标注与内容不符时模型会只摘最后一段，
	// 前面几轮的结论就被它自己忽略了。
	if batch.turns > 1 {
		b.WriteString("\n【最近 ")
		b.WriteString(fmt.Sprint(batch.turns))
		b.WriteString(" 轮对话】\n用户：")
	} else {
		b.WriteString("\n【本轮对话】\n用户：")
	}
	b.WriteString(clipRunes(strings.TrimSpace(batch.user), memoryAutoUserRune))
	b.WriteString("\n")
	if uniq := dedupKeepOrder(batch.files); len(uniq) > 0 {
		b.WriteString("改动文件：")
		b.WriteString(clipRunes(strings.Join(uniq, "、"), 600))
		b.WriteString("\n")
	}
	b.WriteString("助手：")
	b.WriteString(clipRunes(strings.TrimSpace(batch.reply), memoryAutoReplyRune))
	b.WriteString("\n")

	// 输出必须极短：这次调用的唯一产出是几行条目，给个 300 token 就绰绰有余，
	// 越短的输出也越不容易跑成「一段解释」。
	memCfg := cfg
	memCfg.MaxTokens = 300
	memCfg.Temperature = 0.2

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, usage, err := a.callAIOnce(ctx, memCfg, []map[string]string{
		{"role": "system", "content": memoryAutoSystemPrompt(sc)},
		{"role": "user", "content": b.String()},
	})
	if err != nil {
		logger.W("[ai] 记忆整理调用失败 conv=%s: %v", convID, err)
		return
	}
	// 这也是一次完整计费的调用，不记就会系统性少算（同标题生成）。
	a.recordAIUsage(convID, usageKindMemory, memCfg, usage)

	bullets := memoryBullets(out, existing)
	if len(bullets) == 0 {
		return
	}
	if err := appendOverallMemory(sc.root, bullets); err != nil {
		logger.W("[ai] 写入记忆失败 root=%s: %v", sc.root, err)
	}
}

// appendCapped 把 add 接到 cur 后面，总量不超过 cap（超了从前面截）。
//
// 用 "\n---\n" 分隔而不是直接拼：连续几轮的用户消息直接粘在一起，模型会把
// 上一轮的结尾和下一轮的开头读成一句话。
func appendCapped(cur, add string, cap int) string {
	add = strings.TrimSpace(add)
	if add == "" {
		return cur
	}
	if cur == "" {
		return clipRunes(add, cap)
	}
	s := cur + "\n---\n" + add
	r := []rune(s)
	if cap <= 0 || len(r) <= cap {
		return s
	}
	return "…" + string(r[len(r)-cap:])
}

// memoryAutoSystemPrompt 两个作用域分开写：项目里该记的是技术栈与坑，
// 跨任务的共享记忆里该记的是「这个人是谁、偏好什么、问过什么结论」。
func memoryAutoSystemPrompt(sc memoryScope) string {
	what := "用户的偏好与禁忌、他在做什么事、明确要求过的做法、你给出的结论与决定、踩过的坑"
	if sc.project {
		what = "这个项目的事实：技术栈、关键文件路径、约定、踩过的坑、已经做过的决策"
	}
	return "你是长期记忆整理器。从「本轮对话」里挑出**以后还用得上**的事实，供下次直接使用。\n" +
		"只输出条目：每行以 \"- \" 开头，最多 " + fmt.Sprint(memoryAutoBulletMax) + " 行，每行不超过 60 字。\n" +
		"不要标题、不要编号、不要解释、不要代码块、不要复述过程。\n" +
		"「已有长期记忆」里已经有的不要重复。\n" +
		"该记的是：" + what + "。\n" +
		"不该记的是：一次性的问题与寒暄、你这次做了哪些操作、临时路径、代码内容、从对话里明显读得出的临时上下文。\n" +
		"如果本轮没有任何值得长期记住的内容，只输出一行：NONE"
}

// memoryBullets 从模型输出里抽出条目：只要 `- ` / `* ` / `• ` 开头的行，
// 去掉已有的、去重、限长限量。模型偶尔会加一句「以下是提炼结果」，这类行没有
// 项目符号，自然被丢掉——比按行号猜哪行是正文可靠。
func memoryBullets(raw, existing string) []string {
	out := make([]string, 0, memoryAutoBulletMax)
	seen := make(map[string]bool, memoryAutoBulletMax)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "NONE") || line == "无" || line == "（无）" {
			continue
		}
		var body string
		switch {
		case strings.HasPrefix(line, "- "):
			body = line[2:]
		case strings.HasPrefix(line, "* "), strings.HasPrefix(line, "• "):
			body = line[2:]
		case line == "-" || line == "*" || line == "•":
			continue
		default:
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" || seen[body] {
			continue
		}
		if existing != "" && strings.Contains(existing, body) {
			continue
		}
		seen[body] = true
		out = append(out, clipRunes(body, memoryAutoBulletRune))
		if len(out) >= memoryAutoBulletMax {
			break
		}
	}
	return out
}

// appendOverallMemory 把条目追加到 MEMORY.md 的当天小节下，并把文件裁回上限。
//
// 每次追加都开一个新的 `## 日期 时间` 段而不是往今天那一段里塞：找「今天那段」
// 要解析文件结构，而模型自己写的内容不一定守格式，解析错了就写进别的小节里。
func appendOverallMemory(root string, bullets []string) error {
	// 锁在这里而不是在调用方：这一段是 read-modify-write，两个作用域同时结束
	// （或同一作用域连着跑两次）不串行就会各自读到旧内容、各自覆盖，丢条目。
	// 锁只罩文件读写，不罩那次模型调用——那才是原先串行 45 秒的原因。
	memoryAutoFileMu.Lock()
	defer memoryAutoFileMu.Unlock()

	p := overallMemoryPath(root)
	cur := ""
	if data, err := os.ReadFile(p); err == nil {
		cur = string(data)
	}
	if strings.TrimSpace(cur) == "" || isPlaceholderProfile(cur) {
		// 空画像没有保留价值，直接换成一个干净标题：留着它会被当成正文第一段。
		cur = "# 长期记忆\n"
	}
	if !strings.HasSuffix(cur, "\n") {
		cur += "\n"
	}
	var b strings.Builder
	b.WriteString(cur)
	fmt.Fprintf(&b, "\n## %s\n", time.Now().Format("2006-01-02 15:04"))
	for _, l := range bullets {
		b.WriteString("- " + l + "\n")
	}
	content := trimMemoryToCap(b.String(), memoryAutoFileCap)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// trimMemoryToCap 超出 cap 时丢掉**最旧**的 `## ` 段，保留开头那段说明与最新的几节。
//
// 为什么不直接截尾：注入时是 `clipRunes(s, 6000)` 从头截的，文件越长，
// 被截掉的就是越新的记忆——正是最该留的那部分。所以宁可自己删旧的。
func trimMemoryToCap(content string, cap int) string {
	if cap <= 0 || utf8.RuneCountInString(content) <= cap {
		return content
	}
	idx := strings.Index(content, "\n## ")
	if idx < 0 {
		// 没有分节（模型用 project_memory 整份重写过）：只能从尾巴截，保住开头的标题。
		return clipRunes(content, cap)
	}
	head := content[:idx]   // 标题 + 说明
	rest := content[idx+1:] // 以 "## " 开头
	parts := strings.Split(rest, "\n## ")
	for i := 1; i < len(parts); i++ {
		parts[i] = "## " + parts[i]
	}
	kept := make([]string, 0, len(parts))
	used := utf8.RuneCountInString(head)
	for i := len(parts) - 1; i >= 0; i-- {
		n := utf8.RuneCountInString(parts[i])
		if used+n > cap && len(kept) > 0 {
			break
		}
		kept = append([]string{parts[i]}, kept...)
		used += n
	}
	return head + "\n" + strings.Join(kept, "\n")
}
