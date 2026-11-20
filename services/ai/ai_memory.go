package ai

// 记忆：把「在做什么、改过什么、踩过什么坑、结论是什么」沉淀到磁盘，
// 让 AI 在后续轮次/会话里不必反复重读项目，也不必让用户重复交代背景。
//
// 三个位置（都对用户可读、可编辑，这不是黑盒缓存）：
//
//   - MEMORY.md          常青记忆：项目基线 / 硬规则 / 踩过的坑 / 项目事实 / 决策，
//     末尾是按文件系统自动生成的「分册索引」。由任务收尾的那次整理调用写入
//     （见 ai_memory_auto.go），模型也可以用 project_memory 工具直接改。
//   - memory/YYYY-MM-DD.md  当天的工作记录。每轮结束先由系统写一条确定性的活动行
//     （谁在几点要了什么、改了几个文件、跑了几轮），任务收尾再补一段模型产出的
//     「工程笔记」（需求/做了什么/结论/坑/门禁）。前者一定存在，后者让记忆有内容。
//   - memory/_distill/NN-主题.md  专题分册：某个主题（比如「插件系统」）的细节
//     超过十来条时，整段历史挪到这里，MEMORY.md 只留结论与规则。
//
// 为什么要分成三层：注入进系统提示的是「MEMORY.md + 今明两天的每日记忆」，
// 字符预算只有万把字。把所有细节都塞进 MEMORY.md，注入时会被截断，而截掉的
// 正好是最新的那部分；只写每日流水账又没有可复用的结论——两头都试过，都不行。
//
// **记忆跟着「工作区」走，但工作区不等于每个任务的临时目录**（2026-11-01 修）：
//
//   - 属于空间的任务 → 记忆在 <空间目录>/.quickdock，项目事实跟着项目走。
//   - 不属于空间的任务 → cwd 是系统在 ai_tasks 下新建的空目录，一个任务一个。
//     按 cwd 存的话每条对话都从零开始，MEMORY.md 永远只有自动生成的空画像——
//     用户看到的就是「记忆全是默认的、跟刚才问的没关系」。所以这类任务共用
//     数据目录下的 ai_memory/，跨任务、跨天累积。
//
// 为什么能省 token：原来「优化一下网页」会让模型把整个 index.html 再读一遍，
// 而且那份内容会留在对话历史里每轮重发。现在每轮开头把记忆块（整体 + 近两日）
// 注入系统提示，模型先看到「我已经建了一个单页站点、结构是这样、昨天改到哪了」，
// 大多数情况直接用 file_read 的 offset/limit 读相关片段或 file_grep 定位就够了，
// 不必整文件重读。跨会话也是同理——下次打开同一个目录，记忆还在。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const (
	// memoryDirName 项目记忆目录名（就在工作目录下，跟着项目走）。
	// memoryDirName 项目记忆目录。
	//
	// **必须与 ai_rules.go / hooks.go 的 .quickdock 完全一致（含大小写）**：
	// 曾经这里是 ".quickDock" 而规则是 ".quickdock"，Windows 上两者是同一个
	// 目录（大小写不敏感）所以问题被掩盖了；Linux/macOS 上它们是两个目录，
	// 表现为「记忆写在 A、规则从 B 读」，谁都不报错。
	memoryDirName = ".quickdock"
	// assistantMemoryDirName 无空间任务共用的助手记忆目录名（在数据目录下）。
	// 为什么不复用 <数据目录>/.quickDock：那是宿主自己的数据（db、logs、plugins），
	// 记忆混进去以后「哪些是机器在用的、哪些是 AI 的记忆」就分不清了。
	assistantMemoryDirName = "ai_memory"
)

// memoryScope 记忆落在哪儿。
type memoryScope struct {
	// root MEMORY.md 与 memory/ 所在的目录。
	root string
	// project true = <cwd>/.quickDock（跟着项目走）；false = 数据目录下跨任务共享。
	project bool
}

// resolveMemoryScope 决定「这个工作目录的记忆该存哪儿」。
//
// 唯一的判据是「cwd 是不是系统给无空间任务自动建的目录」：是就说明这里没有项目，
// 记忆只能跨任务共用一份；否则就是用户真的在某个项目里干活，记忆跟着项目最合适。
func resolveMemoryScope(cwd string) memoryScope {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return memoryScope{}
	}
	if isScratchWorkDir(cwd) {
		return memoryScope{root: assistantMemoryRoot()}
	}
	return memoryScope{root: filepath.Join(cwd, memoryDirName), project: true}
}

// assistantMemoryRoot 无空间任务的共享记忆目录（数据目录下）。
//
// 每次调用现算而不是启动时算一次：数据目录可以被用户迁移，缓存下来迁移后
// 就会写到旧目录去（而旧目录可能已经被删了）。
func assistantMemoryRoot() string {
	return filepath.Join(platform.DefaultDataDir(), assistantMemoryDirName)
}

// isScratchWorkDir 判断 cwd 是不是 ai_tasks 下那个「每个任务一个」的空目录。
//
// 用路径关系判断而不是看目录里有没有文件：任务目录里迟早会有 AI 生成的文件，
// 那时候再「有文件就当项目」会把同一个任务的记忆从共享区搬走，前后不是一份。
func isScratchWorkDir(cwd string) bool {
	base, err := filepath.Abs(filepath.Join(platform.DefaultDataDir(), aiTaskDirName))
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func overallMemoryPath(root string) string { return filepath.Join(root, "MEMORY.md") }
func dailyMemoryPath(root string, day time.Time) string {
	return filepath.Join(root, "memory", day.Format("2006-01-02")+".md")
}

// memoryCapOverall / memoryCapDaily / memoryCapDailyShared 注入时各自的上限（字符），
// 防止记忆本身撑爆上下文。共享的那份一天里会攒下所有任务的条目，给宽一点。
//
// 这三个数一起决定「每轮白付多少 token」：7000 字记忆 ≈ 4500 token，
// 已经是常态开销的上限了。要加内容就往分册（memory/_distill/）里放，
// 它不参与注入，只在模型需要时按需 file_read。
const (
	memoryCapOverall     = 7000
	memoryCapDaily       = 3200
	memoryCapDailyShared = 5600
)

// readProjectMemory 读取整体记忆 + 今日与昨日每日记忆，拼成一个紧凑的上下文块。
// 没有任何记忆时返回空串，调用方据此决定是否注入（避免无谓地污染系统提示）。
func readProjectMemory(cwd string) string {
	sc := resolveMemoryScope(cwd)
	if sc.root == "" || !dirExists(sc.root) {
		return ""
	}
	var b strings.Builder
	if data, err := os.ReadFile(overallMemoryPath(sc.root)); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" && !isPlaceholderProfile(s) {
			b.WriteString("【整体记忆】\n")
			b.WriteString(clipRunes(s, memoryCapOverall))
			b.WriteString("\n\n")
		}
	}
	dailyCap := memoryCapDaily
	if !sc.project {
		dailyCap = memoryCapDailyShared
	}
	now := time.Now()
	// 今日 + 昨日：够覆盖「这次会话之前做到哪了」，又不把一整周的日志都塞进来。
	for _, d := range []time.Time{now, now.AddDate(0, 0, -1)} {
		s := readDailyMemory(dailyMemoryPath(sc.root, d), dailyCap)
		if s != "" {
			b.WriteString(fmt.Sprintf("【%s 每日记忆】\n", d.Format("2006-01-02")))
			b.WriteString(s)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// readDailyMemory 读一天的活动日志并压进 cap：**工作重点块始终保留**，活动记录只留最近的。
//
// 不能整份 clipRunesTail：重点块在文件顶部，按尾巴截正是最先被裁掉的那部分，
// 于是「今天在做什么」反而看不到，只剩一串「用户：继续」——那恰恰是这次要修的毛病。
func readDailyMemory(path string, cap int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return ""
	}
	head, body := splitDailySections(s)
	if head == "" {
		// 旧格式（只有活动记录）：只能按尾巴截，最近的记录才有用。
		return clipRunesTail(s, cap)
	}
	out := clipRunes(head, cap*2/5)
	body = strings.TrimSpace(body)
	if body == "" {
		return out
	}
	// 换行 + clipRunesTail 那句省略号提示都要算进预算，否则拼完会超出去十几个字。
	rest := cap - utf8.RuneCountInString(out) - 1 - utf8.RuneCountInString(clipTailEllipsis)
	if rest <= 200 {
		// 剩下的位置太小，塞进去只会读到半句话，不如只给重点。
		return out
	}
	return out + "\n" + clipRunesTail(body, rest)
}

// memoryBlock 组装注入系统提示的整块记忆（含指引），不需要注入时返回空串。
//
// 两条不同的空值规则：
//   - 项目作用域：没内容就不注入（项目画像一生成就有内容，读不到说明这项目没记忆）。
//   - 助手作用域：即使一个字都没写过也要给指引。否则模型压根不知道往哪写，
//     共享记忆会一直是空的——「记忆永远是默认的」另一种成因。
func memoryBlock(cwd string) string {
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return ""
	}
	if mem := readProjectMemory(cwd); mem != "" {
		label := "项目记忆"
		if !sc.project {
			label = "助手记忆"
		}
		return "[" + label + "]\n" + mem + memoryGuidance(sc)
	}
	if sc.project {
		return ""
	}
	return "[助手记忆]\n（还没有长期记忆）" + memoryGuidance(sc)
}

// ---- 会话内的记忆快照（为了前缀缓存） ----
//
// 记忆块贴在 system 的最前面，而它**每轮都会变**：每日日志每跑一次就多一行活动
// 记录，任务收尾还会往 MEMORY.md 里加条目。上游是前缀缓存——system 里改一个字，
// 后面整段历史（工具定义 + 全部消息）全部作废。
//
// 实测（2026-11-20，本机 ai_usage）：同一会话跨 run 的第一次请求命中率只有
// 24.6%，而「上一次请求的 prompt」能覆盖这一次的 98%+；纯聊天会话整段只有
// 15-22%。也就是说，跨轮次本来能省下的钱几乎全被这一次无谓的变更吃掉了。
//
// 做法与 Cursor / DSH 这类工具一致：**记忆在会话开头读一次，会话内不再重读**。
// 会话里刚发生的事本来就在对话历史里，模型不需要靠记忆再看一遍；新写的记忆
// 最多晚一会话间隙（aiMemorySnapshotTTL）生效，而这段时间里的每一轮都省下整段重算。
var aiMemorySnapshotTTL = 10 * time.Minute

// aiMemorySnapMax 快照表上限。每个快照约 7KB（注入上限），256 个 ≈ 1.8MB，
// 超过就从最久没刷新的那个开始丢——被丢掉的会话下一轮重读一次即可。
const aiMemorySnapMax = 256

type aiMemorySnapshot struct {
	root  string // 记忆根目录：跟着项目走，换项目（换 cwd）要重建
	block string
	at    time.Time
}

var (
	aiMemorySnapMu sync.Mutex
	aiMemorySnaps  = map[string]aiMemorySnapshot{}
)

// memoryBlockForConversation 取这条会话要注入的记忆块（带会话内快照）。
//
// convID 为空（一次性调用 / 测试）时直接现读，不进快照表：那些调用没有「下一轮」
// 可言，缓存复用无从谈起。
func memoryBlockForConversation(convID, cwd string) string {
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return ""
	}
	id := strings.TrimSpace(convID)
	if id == "" {
		return memoryBlock(cwd)
	}

	now := time.Now()
	aiMemorySnapMu.Lock()
	if s, ok := aiMemorySnaps[id]; ok && s.root == sc.root && now.Sub(s.at) < aiMemorySnapshotTTL {
		aiMemorySnapMu.Unlock()
		return s.block
	}
	aiMemorySnapMu.Unlock()

	// 文件读取放在锁外：一次要读 MEMORY.md + 两天的每日记忆，可能几十毫秒，
	// 而锁是全局的（所有会话共用一张表）。
	block := memoryBlock(cwd)

	aiMemorySnapMu.Lock()
	if len(aiMemorySnaps) >= aiMemorySnapMax {
		evictOldestMemorySnapshotLocked()
	}
	aiMemorySnaps[id] = aiMemorySnapshot{root: sc.root, block: block, at: now}
	aiMemorySnapMu.Unlock()
	return block
}

// evictOldestMemorySnapshotLocked 丢掉最旧的快照。调用方持锁。
func evictOldestMemorySnapshotLocked() {
	oldestID, oldest := "", time.Time{}
	for id, s := range aiMemorySnaps {
		if oldest.IsZero() || s.at.Before(oldest) {
			oldestID, oldest = id, s.at
		}
	}
	if oldestID != "" {
		delete(aiMemorySnaps, oldestID)
	}
}

// appendDailyMemory 在一轮正常结束后追加一条当日记录。
//
// 纯确定性提取（用户诉求 + 改了哪些文件 + 工具轮次），不调模型——这是记忆的
// 可靠底座。即使 AI 从没主动写过 MEMORY.md，每日记忆也能让它下一轮知道「刚才做了什么」。
//
// 结构（见文件头注释）：
//
//	# 2026-11-02 · 工作重点
//	- 扫描这个项目 · 12 轮 / 35 次调用 · 09:55 · 3f2a1b
//	## 09:55 · 3f2a1b
//	- 用户：扫描这个项目
//	- 工具：12 轮 / 35 次调用
//
// title 是这个任务的标题（会话标题，通常是首句提问）：重点块用它当条目名，
// 续跑的「继续」才不会把条目名覆盖成「继续」。
func appendDailyMemory(convID, cwd, title, userMsg string, files []string, rounds, tools int) {
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return
	}
	// 纯问答轮次（没调工具、没改文件）只记「这次问过什么」，不记工具统计。
	//
	// 早先这里是直接 return：后果是**新用户跑几天也看不到任何记忆文件**——
	// 目录是惰性创建的，一次纯聊天就让整条链路看起来「没生效」。现在改成
	// 「照写，但不写 0 轮 0 次调用那种空统计」（统计行由 formatDailyActivity 决定），
	// 既不再被垃圾条目填满，也不会看起来什么都没发生。
	chatOnly := rounds == 0 && len(files) == 0
	root := sc.root
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		logger.W("[ai] 创建记忆目录失败 root=%s: %v", root, err)
		return
	}
	now := time.Now()
	path := dailyMemoryPath(root, now)
	key := shortConvID(convID)

	// 读-改-写必须串行：两个任务同时结束时会各自读到旧文件、各自覆盖，
	// 丢的是对方那条重点与活动记录（与 MEMORY.md 的 memoryAutoFileMu 同理）。
	dailyMemoryMu.Lock()
	defer dailyMemoryMu.Unlock()

	cur := ""
	if data, err := os.ReadFile(path); err == nil {
		cur = string(data)
	}
	head, body := splitDailySections(cur)

	// 条目名优先用任务标题：续跑时用户只打了「继续」，那不是一个重点。
	label := dailyLabel(title, userMsg)
	items := upsertDailyFocus(parseDailyFocus(head), dailyFocus{
		key:    key,
		label:  label,
		rounds: rounds,
		tools:  tools,
		at:     now.Format("15:04"),
	})

	// 一天里一个任务只占一个小节，后续续跑往同一个小节里追加一行活动。
	//
	// 原来的格式是「每轮一个新 `## 日期 时:分`」：一次长任务下来日志变成八个小节，
	// 每节只有一句「用户：继续」加一大串绝对路径——用户的原话是「这记忆根本不能用」。
	// 换成「按任务分节 + 节内按时间追加」之后，同一件事的进展是连续的。
	secTitle := dailySectionTitle(label, key)

	// 一行活动：时间 · 用户诉求（续跑省略） · 投入 · 改动文件。
	var act strings.Builder
	act.WriteString("- ")
	act.WriteString(now.Format("15:04"))
	if q := strings.TrimSpace(userMsg); q != "" && !isContinuationText(q) {
		act.WriteString(" · ")
		act.WriteString(clipRunes(collapseSpaces(q), dailyActivityUserRune))
	}
	// 纯问答不写「0 轮 / 0 次调用」——那正是用户说的「垃圾记忆」。
	// 只记时间与诉求，信息量足够回答「那天我问过什么」。
	if !chatOnly {
		fmt.Fprintf(&act, " · %d 轮 / %d 次调用", rounds, tools)
	}
	if fs := formatDailyFiles(cwd, files); len(fs) > 0 {
		act.WriteString(" · 文件：")
		act.WriteString(strings.Join(fs, "、"))
	}

	merged, found := appendDailyActivity(body, secTitle, act.String())
	if !found {
		// 没有这个任务的小节：开一个，标题用任务名（比时间戳有用得多）。
		if strings.TrimSpace(merged) != "" && !strings.HasSuffix(merged, "\n") {
			merged += "\n"
		}
		merged += "\n## " + secTitle + "\n" + act.String() + "\n"
	}

	var b strings.Builder
	b.WriteString(buildDailyHead(now.Format("2006-01-02"), items))
	b.WriteString("\n")
	if s := strings.TrimSpace(merged); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	content := trimMemoryToCap(b.String(), dailyMemoryFileCap)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		logger.W("[ai] 写每日记忆失败 root=%s: %v", root, err)
	}
}

// appendDailyActivity 把一行活动追加到指定小节的末尾；没有该小节时原样返回。
//
// 按「整行等于 `## 标题`」匹配而不是子串匹配：任务名互相包含（「扫描项目」与
// 「扫描项目文档」）时，子串匹配会把活动写进别人的小节里。
func appendDailyActivity(body, secTitle, line string) (string, bool) {
	return appendDailySectionLines(body, secTitle, []string{line})
}

// appendDailySectionLines 把若干行插到指定小节最后一个非空行之后。
func appendDailySectionLines(body, secTitle string, add []string) (string, bool) {
	if strings.TrimSpace(body) == "" || len(add) == 0 {
		return body, false
	}
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \r") == "## "+secTitle {
			start = i
			break
		}
	}
	if start < 0 {
		return body, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	// 插到这一节最后一个非空行之后（保持小节内紧凑，不留空行）。
	insert := end
	for insert > start+1 && strings.TrimSpace(lines[insert-1]) == "" {
		insert--
	}
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:insert]...)
	out = append(out, add...)
	out = append(out, lines[insert:]...)
	return strings.Join(out, "\n"), true
}

// appendDailyNote 把任务收尾时模型产出的「工程笔记」写进当天该任务小节的末尾。
//
// 为什么和活动行分开写：活动行由系统同步写（一定存在、不依赖模型），笔记是后台
// 那次整理调用的产物（可能失败）。分开才能做到「模型失败时至少还有流水账」。
//
// 落地形态：
//
//	## 检查待提交的都是什么功能 · 684bddf3
//	- 08:00 · 拉取一下最新代码 · 3 轮 / 4 次调用
//	#### 08:00 · 笔记
//	- 需求：…
//	- 根因：…
//
// 用 `#### ` 而不是 `### `：每日文件里 `## ` 是任务小节，`### ` 及以上留给模型
// 自己在笔记里分层，解析器只认 `## `，多一层不会把结构搞乱。
func appendDailyNote(convID, cwd, title, userMsg string, at time.Time, lines []string) {
	if len(lines) == 0 {
		return
	}
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(sc.root, "memory"), 0o755); err != nil {
		logger.W("[ai] 创建记忆目录失败 root=%s: %v", sc.root, err)
		return
	}
	path := dailyMemoryPath(sc.root, at)
	secTitle := dailySectionTitle(dailyLabel(title, userMsg), shortConvID(convID))

	dailyMemoryMu.Lock()
	defer dailyMemoryMu.Unlock()

	cur := ""
	if data, err := os.ReadFile(path); err == nil {
		cur = string(data)
	}
	head, body := splitDailySections(cur)

	block := []string{"#### " + at.Format("15:04") + " · 笔记"}
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- "))
		if l == "" {
			continue
		}
		block = append(block, "- "+l)
	}
	if len(block) == 1 {
		return
	}

	merged, found := appendDailySectionLines(body, secTitle, block)
	if !found {
		// 活动行还没写（或标题在这两处之间变过）：补一个小节，笔记本身就有信息量。
		if strings.TrimSpace(merged) != "" && !strings.HasSuffix(merged, "\n") {
			merged += "\n"
		}
		if strings.TrimSpace(merged) != "" {
			merged += "\n"
		}
		merged += "## " + secTitle + "\n" + strings.Join(block, "\n") + "\n"
	}
	if strings.TrimSpace(head) == "" {
		// 一天里可能先只有笔记（活动行那次写失败了）：补一个空的重点块，
		// 否则下次 appendDailyMemory 会把整份内容当活动记录，重点行永远补不回来。
		head = "# " + at.Format("2006-01-02") + focusLineSep + "工作重点"
	}

	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\n")
	if s := strings.TrimSpace(merged); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(trimMemoryToCap(b.String(), dailyMemoryFileCap)), 0o644); err != nil {
		logger.W("[ai] 写每日笔记失败 root=%s: %v", sc.root, err)
	}
}

// dailyLabel 任务在记忆里的显示名。
func dailyLabel(title, userMsg string) string {
	label := strings.TrimSpace(title)
	if label == "" {
		label = strings.TrimSpace(userMsg)
	}
	return clipRunes(collapseSpaces(label), dailyFocusLabelRune)
}

// dailySectionTitle 每日记忆里一个小节的标题：任务名 + 短会话 id。
//
// 为什么带 id：只按标题分节时，两个会话同名（都叫「继续」、或长标题被截成同一
// 前缀）就会写进同一节，一个任务的历史里混进别人的记录；而文件顶部的重点块
// 是按会话 id 去重的，两处口径就对不上了。
func dailySectionTitle(label, key string) string {
	sec := label
	if sec == "" {
		sec = key
	}
	if key != "" {
		sec += " · " + key
	}
	return sec
}

// formatDailyFiles 把改动文件整理成「相对项目根的短列表」。
//
// 三条规则，都是为了让它**可读**：
//   - 相对路径：原来写的是 `D:\project\quickdock-v3\.tmp_scan\stats.ps1` 这种全路径，
//     一条就占掉半行，十条就把日志变成一堵墙；
//   - 丢掉辅助目录（`.quickdock/`）里的文件：那是 AI 自己的临时脚本，不是项目改动，
//     记进去只会让人以为项目被改了一堆东西；
//   - 最多 6 条，其余折成「…等 N 个」：记忆的用途是「想起来做过什么」，
//     不是提交清单。
func formatDailyFiles(cwd string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	// 基准一律是**任务的工作目录**（不是记忆根目录）。
	//
	// 原来共享作用域用记忆根做 base，而任务文件永远在项目里 → Rel 必然失败，
	// 全部退化成 basename：`src/a.go` 与 `test/a.go` 会被去重成一条，
	// 子目录信息全丢。项目作用域原来传的是 root，再由 project 标志换算成 cwd，
	// 绕了一圈还容易传错；现在直接传 cwd。
	base := strings.TrimSpace(cwd)
	out := make([]string, 0, len(files))
	seen := make(map[string]bool, len(files))
	skipped := 0
	for _, f := range dedupKeepOrder(files) {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		rel := f
		if base != "" {
			if r, err := filepath.Rel(base, f); err == nil && !strings.HasPrefix(r, "..") {
				rel = filepath.ToSlash(r)
			} else {
				rel = filepath.Base(f)
			}
		}
		if isScratchPath(rel) {
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if len(out) < dailyFilesMax {
			out = append(out, rel)
		} else {
			skipped++
		}
	}
	if skipped > 0 {
		out = append(out, fmt.Sprintf("…等 %d 个", skipped))
	}
	return out
}

// isScratchPath 判断一个相对路径是不是 AI 的辅助文件。
func isScratchPath(rel string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	// Windows 上大小写不敏感：旧版目录名是 `.quickDock`，写成大写时也要认出来，
	// 否则那些临时脚本会被当成「项目改动」记进每日记忆。
	lower := strings.ToLower(rel)
	dir := strings.ToLower(memoryDirName)
	return strings.HasPrefix(lower, dir+"/") || lower == dir
}

// isContinuationText 这句是不是「接着说」而不是新任务。
//
// 只认几种极短的口语：长了就可能是新需求（「继续，不过这次改用 pnpm」）。
func isContinuationText(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.Trim(s, "。.!！?？~ ")
	switch s {
	case "继续", "接著做", "接着做", "继续做", "往下", "continue", "go on", "keep going", "ok", "好", "好的", "嗯":
		return true
	}
	return false
}

// hasDailySectionFor 今天的日志里有没有这个会话的小节。
//
// 判据是「小节标题里带会话短 id」：项目作用域下标题只有时间（不带 id），
// 所以这里对项目日志恒为 false —— 那种情况不值得为「续跑多一条」做特判，
// 而共享日志（不带项目目录）正是最容易堆重复的地方。
func hasDailySectionFor(content, key string) bool {
	if key == "" || content == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		if strings.Contains(line, key) {
			return true
		}
	}
	return false
}

// ---- 每日记忆：工作重点块 ----
const (
	// dailyFocusMaxLines 重点块最多记几条：一天做了十几件事已经很满，
	// 再细的追查交给下面的活动记录。
	dailyFocusMaxLines = 12
	// dailyFocusLabelRune 重点条目名的长度上限（与标题截断同一量级）。
	dailyFocusLabelRune = 30
	// dailyMemoryFileCap 单个每日记忆文件的体积上限。
	// 一天里所有任务都写这一个文件，不限的话它会成为每次注入都要读的大文件。
	dailyMemoryFileCap = 20000
	// focusLineSep 重点行的分隔符。选 " · " 而不是 " | "：中文行里它更像分隔符。
	focusLineSep = " · "
	// dailyActivityUserRune 活动行里用户诉求的截断长度。
	// 记忆要的是「这次干了什么」，不是原话存档。
	dailyActivityUserRune = 60
	// dailyFilesMax 活动行里最多列几个改动文件，其余折成「…等 N 个」。
	dailyFilesMax = 6
)

// dailyMemoryMu 同一时刻只改一份每日记忆（见 appendDailyMemory）。
var dailyMemoryMu sync.Mutex

// dailyFocus 重点块里的一条：一个任务（会话）今天投入了多少。
type dailyFocus struct {
	key    string // 会话短 id：同一个任务的多次续跑合并成一条
	label  string // 任务标题（没有标题时用首句提问）
	rounds int
	tools  int
	at     string // 最后更新的时刻 "15:04"
}

func (f dailyFocus) line() string {
	return "- " + f.label + focusLineSep +
		fmt.Sprintf("%d 轮 / %d 次调用", f.rounds, f.tools) + focusLineSep +
		f.at + focusLineSep + f.key
}

// focusCountRe 匹配重点行里那段「N 轮 / M 次调用」。
var focusCountRe = regexp.MustCompile(`^(\d+) 轮 / (\d+) 次调用$`)

// parseFocusLine 解析一条重点行（格式见 dailyFocus.line）。
//
// 从右往左切：任务标题里本身可能带 " · "（「性能 · 首页」），从左切会把标题切碎。
// 尾部三段（轮次 / 时刻 / 会话 id）格式固定，认不出来就整行放弃——
// 宁可少一条，也不要写出一条对不上的（它会被下次累加成天文数字）。
func parseFocusLine(line string) (dailyFocus, bool) {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
	parts := strings.Split(s, focusLineSep)
	if len(parts) < 4 {
		return dailyFocus{}, false
	}
	n := len(parts)
	m := focusCountRe.FindStringSubmatch(parts[n-3])
	if m == nil {
		return dailyFocus{}, false
	}
	f := dailyFocus{key: parts[n-1], at: parts[n-2]}
	f.rounds, _ = strconv.Atoi(m[1])
	f.tools, _ = strconv.Atoi(m[2])
	f.label = strings.Join(parts[:n-3], focusLineSep)
	return f, true
}

// splitDailySections 把每日记忆拆成「重点块」和「活动记录」。
//
// 旧格式（没有 `# ` 标题、直接就是 `## 09:55` 小节）整份都算活动记录，
// 下次写入时自动补上重点块——旧文件不需要迁移脚本。
func splitDailySections(content string) (head, body string) {
	s := strings.TrimLeft(content, "\n")
	if !strings.HasPrefix(s, "# ") {
		return "", s
	}
	if i := strings.Index(s, "\n## "); i >= 0 {
		return strings.TrimRight(s[:i], "\n"), s[i+1:]
	}
	return strings.TrimRight(s, "\n"), ""
}

// parseDailyFocus 从重点块里取出所有能认出来的条目。
func parseDailyFocus(head string) []dailyFocus {
	out := make([]dailyFocus, 0, dailyFocusMaxLines)
	for _, line := range strings.Split(head, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "- ") {
			continue
		}
		if f, ok := parseFocusLine(line); ok {
			out = append(out, f)
		}
	}
	return out
}

// upsertDailyFocus 按会话 id 合并：同一条任务续跑只累加，不新增条目。
//
// 超过上限时从**最早出现**的那条丢起：列表按首次出现排序，所以正好是「今天最早
// 开始的那件事」先走；正在做的任务一定排在后面，不会被自己的第 N 次续跑挤掉。
func upsertDailyFocus(items []dailyFocus, f dailyFocus) []dailyFocus {
	for i := range items {
		if items[i].key != f.key {
			continue
		}
		if f.label != "" {
			items[i].label = f.label
		}
		items[i].rounds += f.rounds
		items[i].tools += f.tools
		items[i].at = f.at
		return items
	}
	if f.label == "" || (f.rounds == 0 && f.tools == 0) {
		return items
	}
	items = append(items, f)
	if len(items) > dailyFocusMaxLines {
		items = items[len(items)-dailyFocusMaxLines:]
	}
	return items
}

func buildDailyHead(day string, items []dailyFocus) string {
	var b strings.Builder
	b.WriteString("# " + day + focusLineSep + "工作重点\n")
	for _, it := range items {
		b.WriteString(it.line() + "\n")
	}
	return b.String()
}

// collapseSpaces 把换行与连续空白压成单个空格：重点行必须是一行。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// shortConvID 任务 id 前 8 位（与 ai_tasks 下的目录名同一段），只用于在共享日志里标来源。
func shortConvID(convID string) string {
	id := strings.TrimSpace(convID)
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "未知"
	}
	return id
}

// memoryGuidance 写进系统提示的指引：记忆在哪、结构是什么、该往里记什么、读文件别整篇重读。
//
// 两个作用域分开写，因为「值得长期记住的事实」不是一类东西：项目里是技术栈与坑，
// 跨任务的助手记忆里是用户是谁、偏好什么、问过什么、上次给了什么结论。
// 混着讲模型只会把项目细节写进共享记忆，或者反过来什么都不写。
func memoryGuidance(sc memoryScope) string {
	read := "在改动一个你已经碰过的文件前，先用 file_read 的 offset/limit 只读相关片段，或用 file_grep 定位，" +
		"不要整文件重读——一整页 HTML/JS 重读一次就会吞掉大量 token，而且那份内容还会留在历史里每轮重发。"
	layout := "记忆结构：MEMORY.md 是常青记忆（项目基线 / 硬规则 / 踩过的坑 / 项目事实 / 决策 / 分册索引）；" +
		"memory/YYYY-MM-DD.md 是按天的工作记录（系统自动写，你只读不写，要细节就去读它）；" +
		"memory/_distill/NN-主题.md 是专题分册（长文放这里，MEMORY.md 只留结论与规则）。"
	if !sc.project {
		return "\n\n[记忆与读文件指引]\n这个任务没有绑定项目目录，记忆不在工作目录里，而在 " + sc.root + "。\n" + layout +
			"\n**记忆只存在这里**：用 project_memory 工具读写 MEMORY.md；绝不要在任务目录或项目根目录创建 " +
			"AGENTS.md / README.md / memory.md 充当记忆——AGENTS.md 是用户维护的「项目规则文件」，去改它会污染规则。" +
			"这份记忆是跨任务共享的，所以**只记跨任务还有用的事**：用户怎么称呼、习惯与偏好、在做什么事、" +
			"问过哪些问题且结论是什么、明确拒绝过的做法。一次性的闲聊、临时路径、随手查的资料不要写。" +
			"用户说「记住」时，就把它写进去。" + read
	}
	return "\n\n[记忆与读文件指引]\n当前工作目录下有 .quickdock/ 项目记忆：" + sc.root + "。\n" + layout +
		"\n**记忆只存在 .quickdock/ 里**：用 project_memory 工具读写 MEMORY.md；" +
		"绝不要在项目根目录创建或修改 AGENTS.md / README.md / memory.md——AGENTS.md 是「项目规则文件」，" +
		"由用户在设置里维护、每轮自动注入系统提示，不是你的记忆，你去改它会污染用户的规则。" +
		"往 MEMORY.md 里写什么：① **硬规则**——违反过、或用户明确要求过的做法，一条一句，必须带「为什么」；" +
		"② **踩过的坑**——现象 / 根因 / 修法，下次遇到同一个报错能直接对上；③ **项目事实**——技术栈、入口命令、关键路径；" +
		"④ **决策**——已拍板且会被反复问到的取舍。不要往 MEMORY.md 里写当天做了什么（那是 memory/ 的活），" +
		"也不要把整段代码贴进去。某个主题的细节超过十来条时，整理进 memory/_distill/NN-主题.md，" +
		"MEMORY.md 只留一条规则加一句指路（索引会自动生成）。" +
		"**记忆要跟着代码走**：后续改动让某条旧事实/旧规则不再成立时，用 project_memory 把那条改掉或删掉，" +
		"不要只在旁边追加一条新的——两条互相矛盾的记忆，下次读到哪条全看运气。" + read
}

// ---- 项目基线（确定性扫描，不调模型） ----

// memoryProfileNoiseDirs 扫描顶层时跳过这些目录：要么是依赖/构建产物（node_modules、
// dist），要么是本工具自己的数据目录（.quickDock），要么是以点开头的隐藏目录。
// 写进基线只会稀释有效信息，而且每次扫描都把它们数一遍毫无意义。
var memoryProfileNoiseDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	".quickDock": true, "vendor": true, ".idea": true, ".vscode": true,
	"target": true, "bin": true, "obj": true, ".next": true, "out": true,
	"coverage": true, ".cache": true, "tmp": true, "venv": true,
}

// memoryProfileManifests 用清单文件名识别技术栈。顺序即识别顺序，命中即记。
var memoryProfileManifests = []struct {
	name  string
	stack string
	exact bool // true=文件名精确匹配；false=后缀匹配（如 *.csproj）
}{
	{"go.mod", "Go", true},
	{"package.json", "Node.js / TypeScript", true},
	{"Cargo.toml", "Rust", true},
	{"requirements.txt", "Python", true},
	{"pyproject.toml", "Python", true},
	{"Pipfile", "Python", true},
	{"pom.xml", "Java (Maven)", true},
	{"build.gradle", "Java (Gradle)", true},
	{"build.gradle.kts", "Java (Gradle)", true},
	{"Gemfile", "Ruby", true},
	{"composer.json", "PHP", true},
	{"pubspec.yaml", "Dart / Flutter", true},
	{".csproj", "C# (.NET)", false},
}

// projectPackageJSON package.json 里基线用得到的字段。
type projectPackageJSON struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// ensureMemoryProfile 在记忆还是空的时，确定性扫描项目并写一份「项目基线」到 MEMORY.md。
//
// 这样无论用户有没有显式说「建记忆」，首次对话就能把 .quickdock 建起来——
// 而不是让模型自己在项目根目录生成 AGENTS.md 当记忆。
//
// 确定性、不调模型：基线只描述「这是什么项目、怎么构建」，判断哪些是值得长期记的
// 事实交给收尾那次整理调用（见 ai_memory_auto.go），避免机器归纳出噪声。
//
// 两个「不写」的分支都是 2026-11-01 补的：以前空目录也会写一份只有标题的画像，
// 于是每条无空间任务的 MEMORY.md 都是同一段模板字——用户看到的就是「记忆全是默认的」。
func ensureMemoryProfile(cwd string) {
	sc := resolveMemoryScope(cwd)
	// 无空间任务的工作目录是系统新建的空目录：这里没有项目可基线化，
	// 写出来必然是模板。它们的记忆走共享的 ai_memory/，只由模型自己写。
	if sc.root == "" || !sc.project {
		return
	}
	if data, err := os.ReadFile(overallMemoryPath(sc.root)); err == nil {
		// 已有真实内容就别覆盖；只剩空模板时允许重生成（项目后来长出文件了）。
		if s := strings.TrimSpace(string(data)); s != "" && !isPlaceholderProfile(s) {
			return
		}
	}
	lines := scanProjectBaseline(cwd)
	if len(lines) == 0 {
		return
	}
	if err := updateMemoryBaseline(sc.root, lines); err != nil {
		logger.W("[ai] 自动生成项目基线失败 cwd=%s: %v", cwd, err)
	}
}

// isPlaceholderProfile 判断这份 MEMORY.md 是不是「只有标题、没有任何条目」的空画像。
//
// 旧的空目录画像长得正是这样：标题 + 两句说明 + 一行备注。它被当成「已有记忆」读进去，
// 每轮白白占掉提示位，还让用户以为记忆是坏的。识别出来就当没有。
func isPlaceholderProfile(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	// 旧版（2026-11 之前）：标题上带「自动生成的基线」，分区名是 技术栈/顶层目录/文件概览。
	if strings.Contains(s, "自动生成的基线") {
		for _, sec := range []string{"## 技术栈", "## 顶层目录", "## 文件概览", "## 项目基线"} {
			if strings.Contains(s, sec) {
				return false
			}
		}
		return true
	}
	d := parseMemoryDoc(s)
	for _, sec := range d.sections {
		if sec.title == sectionIndex {
			continue
		}
		if len(sec.bullets()) > 0 {
			return false
		}
	}
	// 一个条都没有：只有引言甚至只有标题，等于没有记忆。
	return true
}

// scanProjectBaseline 扫一遍顶层结构，拼出「项目基线」分区的条目。
//
// 与旧版画像的区别：去掉了「文件概览（.go ×8）」这种纯计数——它不回答任何
// 会被问到的问题；补上了**构建与校验命令**、**模块名**、**框架版本**，
// 这三样才是每次任务开头都要用到的。
func scanProjectBaseline(cwd string) []string {
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil
	}
	var dirs, files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if memoryProfileNoiseDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".") {
				continue
			}
			dirs = append(dirs, name+"/")
		} else {
			files = append(files, name)
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)

	var out []string
	if s := projectStackLine(cwd, files); s != "" {
		out = append(out, "技术栈："+s)
	}
	if s := projectModuleLine(cwd); s != "" {
		out = append(out, s)
	}
	if s := projectCommandLine(cwd, files); s != "" {
		out = append(out, s)
	}
	if len(dirs) > 0 {
		if len(dirs) > 14 {
			dirs = dirs[:14]
		}
		out = append(out, "顶层目录："+strings.Join(dirs, "、"))
	}
	// 一条有效信息都没有就别生成：只写标题的空基线比没有更糟——它会被当成
	// 「这个项目已经建好记忆了」，模型不再补充，用户看到的永远是那段模板文字。
	if len(out) == 0 {
		return nil
	}
	return out
}

// projectStackLine 技术栈：清单文件 + 框架版本。
func projectStackLine(cwd string, files []string) string {
	var parts []string
	seen := make(map[string]bool, 6)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		parts = append(parts, s)
	}
	for _, m := range memoryProfileManifests {
		if !manifestHit(files, m.name, m.exact) {
			continue
		}
		// Go 的版本号决定语法可用性（泛型、切片包），比光写「Go」有用得多。
		if m.stack == "Go" {
			if v := goDirectiveVersion(cwd); v != "" {
				add("Go " + v)
				continue
			}
		}
		add(m.stack)
	}
	for _, pkg := range readProjectPackages(cwd) {
		if v := pkg.dependencyVersion("vue"); v != "" {
			add("Vue " + majorVersion(v))
		}
		if v := pkg.dependencyVersion("react"); v != "" {
			add("React " + majorVersion(v))
		}
	}
	if fileExists(filepath.Join(cwd, "wails.json")) {
		add("Wails v3")
	}
	return strings.Join(parts, "、")
}

// manifestHit 顶层文件里有没有这份清单。
func manifestHit(files []string, name string, exact bool) bool {
	for _, f := range files {
		if exact && f == name {
			return true
		}
		if !exact && strings.HasSuffix(strings.ToLower(f), name) {
			return true
		}
	}
	return false
}

// projectModuleLine 模块名（Go 用 go.mod 的 module 行；其它生态用 package.json 的 name）。
func projectModuleLine(cwd string) string {
	if data, err := os.ReadFile(filepath.Join(cwd, "go.mod")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "module ") {
				if m := strings.TrimSpace(strings.TrimPrefix(line, "module ")); m != "" {
					return "模块：" + m
				}
			}
		}
	}
	for _, pkg := range readProjectPackages(cwd) {
		if pkg.Name != "" {
			return "模块：" + pkg.Name
		}
	}
	return ""
}

// projectCommandLine 常用命令：Taskfile 的任务 + package.json 里与构建/校验相关的脚本。
//
// 只挑这几类脚本：记忆的用途是「下次不用问怎么跑」，把 `lint:fix`、`prepare`
// 全列出来反而没人看。
func projectCommandLine(cwd string, files []string) string {
	var cmds []string
	seen := make(map[string]bool, 8)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		cmds = append(cmds, s)
	}
	if manifestHit(files, "Taskfile.yml", true) || manifestHit(files, "Taskfile.yaml", true) {
		for _, t := range taskfileTasks(cwd) {
			add("task " + t)
		}
	} else if manifestHit(files, "Makefile", true) {
		for _, t := range makefileTargets(cwd) {
			add("make " + t)
		}
	}
	for _, pkg := range readProjectPackages(cwd) {
		for _, name := range []string{"dev", "build", "test", "check:i18n", "check:css", "check:bridge"} {
			if _, ok := pkg.Scripts[name]; ok {
				add("npm run " + name)
			}
		}
	}
	if len(cmds) == 0 {
		return ""
	}
	if len(cmds) > 7 {
		cmds = cmds[:7]
	}
	return "常用命令：" + strings.Join(cmds, "、")
}

// taskfileTaskRe Taskfile 里「两格缩进的 key:」即一个任务（四格的是子步骤）。
var taskfileTaskRe = regexp.MustCompile(`(?m)^  ([A-Za-z0-9_:][A-Za-z0-9_:-]*):\s*$`)

func taskfileTasks(cwd string) []string {
	for _, name := range []string{"Taskfile.yml", "Taskfile.yaml"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		out := make([]string, 0, 8)
		for _, m := range taskfileTaskRe.FindAllStringSubmatch(string(data), -1) {
			switch m[1] {
			case "version", "vars", "env", "includes", "silent", "output", "run", "dotenv":
				continue
			}
			out = append(out, m[1])
			if len(out) >= 8 {
				return out
			}
		}
		return out
	}
	return nil
}

// makefileTargetRe Makefile 目标行（行首、非 .PHONY、不含变量）。
var makefileTargetRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_-]+):[^=]*$`)

func makefileTargets(cwd string) []string {
	data, err := os.ReadFile(filepath.Join(cwd, "Makefile"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, 8)
	for _, m := range makefileTargetRe.FindAllStringSubmatch(string(data), -1) {
		out = append(out, m[1])
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// readProjectPackages 读顶层与 frontend/ 下的 package.json（Wails/Tauri 这类
// 前后端分仓的项目，真正的脚本在 frontend/ 里）。
func readProjectPackages(cwd string) []projectPackageJSON {
	paths := []string{
		filepath.Join(cwd, "package.json"),
		filepath.Join(cwd, "frontend", "package.json"),
	}
	out := make([]projectPackageJSON, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pkg projectPackageJSON
		if err := json.Unmarshal(data, &pkg); err != nil {
			continue
		}
		out = append(out, pkg)
	}
	return out
}

func (p projectPackageJSON) dependencyVersion(name string) string {
	if v, ok := p.Dependencies[name]; ok {
		return v
	}
	return p.DevDependencies[name]
}

// majorVersion 从 "3.4.21" / "^3.4.0" / "~18.2.0" 里取主版本号。
func majorVersion(v string) string {
	v = strings.TrimLeft(strings.TrimSpace(v), "^~>=<v ")
	if i := strings.IndexAny(v, ".-+ "); i > 0 {
		return v[:i]
	}
	return v
}

// goDirectiveVersion 取 go.mod 的 `go 1.26` 版本号。
func goDirectiveVersion(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "go ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// itoa 数字转字符串（本包内多处拼提示语用，避免每处都 import strconv）。
func itoa(n int) string { return strconv.Itoa(n) }

// trimMemoryToCap 每日记忆文件超出 cap 时，从**最旧**的 `## ` 任务小节丢起。
//
// 为什么是整节丢：一个小节就是一个任务的完整记录（活动行 + 笔记），
// 按字符截会把最后一条笔记截成半句话，读起来比没有更糟。
// 顶部「工作重点」块永远保留——它是「今天在做什么」的索引。
func trimMemoryToCap(content string, cap int) string {
	if cap <= 0 || utf8.RuneCountInString(content) <= cap {
		return content
	}
	idx := strings.Index(content, "\n## ")
	if idx < 0 {
		// 没有分节：只能截尾，保住开头的重点块。
		return clipRunes(content, cap)
	}
	head := content[:idx]   // 标题 + 重点块
	rest := content[idx+1:] // 以 "## " 开头
	parts := strings.Split(rest, "\n## ")
	for i := 1; i < len(parts); i++ {
		parts[i] = "## " + parts[i]
	}
	kept := make([]string, 0, len(parts))
	used := utf8.RuneCountInString(head)
	for i := len(parts) - 1; i >= 0; i-- {
		sec := parts[i]
		n := utf8.RuneCountInString(sec)
		if used+n > cap && len(kept) > 0 {
			break
		}
		if used+n > cap {
			// 最新这一节自己就超了：整节丢等于把刚做完的事忘了，
			// 只能把这一节按行收一收（保标题 + 末尾若干行）。
			sec = clipSectionTail(sec, cap-used)
			n = utf8.RuneCountInString(sec)
		}
		kept = append([]string{sec}, kept...)
		used += n
	}
	return head + "\n" + strings.Join(kept, "\n")
}

// clipSectionTail 把单个小节裁到 budget 字：保留标题 + **末尾**若干行。
func clipSectionTail(section string, budget int) string {
	if budget <= 0 {
		return ""
	}
	lines := strings.Split(section, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "## ") {
		return clipRunes(section, budget)
	}
	header := lines[0]
	body := lines[1:]
	kept := make([]string, 0, len(body))
	used := utf8.RuneCountInString(header) + 1
	for i := len(body) - 1; i >= 0; i-- {
		l := body[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := utf8.RuneCountInString(l) + 1
		if used+n > budget && len(kept) > 0 {
			break
		}
		kept = append([]string{l}, kept...)
		used += n
	}
	out := header
	if len(kept) > 0 {
		out += "\n" + strings.Join(kept, "\n")
	}
	return out
}

// ---- 小工具 ----

// clipTailEllipsis clipRunesTail 截断时补的提示。单列出来是因为它也算进注入预算。
const clipTailEllipsis = "…（更早的记录已省略）\n"

// clipRunesTail 超长时保留**末尾** max 个字符（前面加省略号）。
//
// 每日记忆是只追加的，超长说明这一天记了很多条；这时候要的是「刚做完的事」，
// 而不是「今天最早那条」。整体记忆反过来——开头是标题与技术栈，用 clipRunes 截尾。
func clipRunesTail(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return clipTailEllipsis + string(r[len(r)-max:])
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// dedupKeepOrder 去重但保留首次出现顺序——每日记忆里同一个文件可能在一轮里被多次触及。
func dedupKeepOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
