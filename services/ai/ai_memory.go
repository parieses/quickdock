package ai

// 记忆：把「在做什么、改过什么、踩过什么坑、问过什么」沉淀到磁盘，
// 让 AI 在后续轮次/会话里不必反复重读项目，也不必让用户重复交代背景。
//
// 两个文件：
//
//   - MEMORY.md          整体记忆（长期）：项目任务里是「项目是什么、技术栈、关键文件、
//     约定、决策与踩过的坑」；无空间任务里是「用户是谁、偏好、问过什么、给过的结论」。
//     由 AI 通过 project_memory 工具主动维护——这些需要「判断哪些值得长期记住」，
//     机器归纳不了，交给模型更准。
//   - memory/YYYY-MM-DD.md  每日记忆（当天的活动日志）：顶部是「今天在做什么」的
//     工作重点（按任务合并、跨轮累加），下面是逐轮的活动记录（谁在几点要了什么、
//     改了哪些文件、跑了几轮）。两者都是纯确定性提取，不调模型，所以一定存在、
//     且不会漏。AI 只读不写它（手动写会破坏自动日志的口径）。
//
// 为什么要有「工作重点」这一层：原来只有逐轮记录，用户说一句「继续」就多一段
// 「- 用户：继续 / - 工具：12 轮」——一整天看下来读不出今天干了什么，注入给模型
// 也只是一串没有主语的碎片。重点块按会话去重，同一个任务续跑几次也只是把轮次
// 累加上去。
//
// **记忆跟着「工作区」走，但工作区不等于每个任务的临时目录**（2026-11-01 修）：
//
//   - 属于空间的任务 → 记忆在 <空间目录>/.quickDock，项目事实跟着项目走。
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
	memoryDirName = ".quickDock"
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
const (
	memoryCapOverall     = 6000
	memoryCapDaily       = 2400
	memoryCapDailyShared = 4800
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
	root := sc.root
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		logger.W("[ai] 创建记忆目录失败 root=%s: %v", root, err)
		return
	}
	now := time.Now()
	path := dailyMemoryPath(root, now)
	key := shortConvID(convID)

	// 读-改-写必须串行：两个任务同时结束时会各自读到旧文件、各自覆盖，
	// 丢的是对方那条重点与活动记录（与 MEMORY.md 的 memoryAutoMu 同理）。
	dailyMemoryMu.Lock()
	defer dailyMemoryMu.Unlock()

	cur := ""
	if data, err := os.ReadFile(path); err == nil {
		cur = string(data)
	}
	head, body := splitDailySections(cur)

	// 条目名优先用任务标题：续跑时用户只打了「继续」，那不是一个重点。
	label := strings.TrimSpace(title)
	if label == "" {
		label = strings.TrimSpace(userMsg)
	}
	items := upsertDailyFocus(parseDailyFocus(head), dailyFocus{
		key:    key,
		label:  clipRunes(collapseSpaces(label), dailyFocusLabelRune),
		rounds: rounds,
		tools:  tools,
		at:     now.Format("15:04"),
	})

	var b strings.Builder
	b.WriteString(buildDailyHead(now.Format("2006-01-02"), items))
	b.WriteString("\n")
	if s := strings.TrimSpace(body); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}

	headTime := now.Format("2006-01-02 15:04")
	if !sc.project {
		// 共享日志里所有任务写同一个文件：不标来源的话，看回日志根本分不清
		// 「这条是谁问的」，而「记得问过什么」正是这份记忆存在的理由。
		headTime += " · " + key
	}
	fmt.Fprintf(&b, "\n## %s\n", headTime)
	if q := strings.TrimSpace(userMsg); q != "" {
		b.WriteString("- 用户：")
		b.WriteString(clipRunes(q, 300))
		b.WriteString("\n")
	}
	if uniq := dedupKeepOrder(files); len(uniq) > 0 {
		b.WriteString("- 改动文件：")
		b.WriteString(strings.Join(uniq, "、"))
		b.WriteString("\n")
	}
	if rounds > 0 || tools > 0 {
		fmt.Fprintf(&b, "- 工具：%d 轮 / %d 次调用\n", rounds, tools)
	}
	// 一天下来活动记录能堆很长：超了从**最旧**的小节丢起，重点块在文件顶部不受影响
	// （trimMemoryToCap 保留第一个 "## " 之前的内容）。
	content := trimMemoryToCap(b.String(), dailyMemoryFileCap)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		logger.W("[ai] 写每日记忆失败 root=%s: %v", root, err)
	}
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

// memoryGuidance 写进系统提示的指引：记忆在哪、该往里记什么、读文件别整篇重读。
//
// 两个作用域分开写，因为「值得长期记住的事实」不是一类东西：项目里是技术栈与坑，
// 跨任务的助手记忆里是用户是谁、偏好什么、问过什么、上次给了什么结论。
// 混着讲模型只会把项目细节写进共享记忆，或者反过来什么都不写。
func memoryGuidance(sc memoryScope) string {
	read := "在改动一个你已经碰过的文件前，先用 file_read 的 offset/limit 只读相关片段，或用 file_grep 定位，" +
		"不要整文件重读——一整页 HTML/JS 重读一次就会吞掉大量 token，而且那份内容还会留在历史里每轮重发。"
	if !sc.project {
		return "\n\n[记忆与读文件指引]\n这个任务没有绑定项目目录，记忆不在工作目录里，而在 " + sc.root +
			"：MEMORY.md 是长期记忆，memory/ 下是按天的活动日志（由系统自动维护，你只读不写）。" +
			"**记忆只存在这里**：用 project_memory 工具读写 MEMORY.md；绝不要在任务目录或项目根目录创建 " +
			"AGENTS.md / README.md / memory.md 充当记忆——AGENTS.md 是用户维护的「项目规则文件」，去改它会污染规则。" +
			"这份记忆是跨任务共享的，所以**只记跨任务还有用的事**：用户怎么称呼、习惯与偏好、在做什么事、" +
			"问过哪些问题且结论是什么、明确拒绝过的做法。一次性的闲聊、临时路径、随手查的资料不要写。" +
			"用户说「记住」时，就把它写进去。" + read
	}
	return "\n\n[记忆与读文件指引]\n当前工作目录下有 .quickDock/ 项目记忆：" + sc.root +
		"：MEMORY.md 是长期整体记忆，memory/ 是按天的活动日志（由系统自动维护，你只读不写）。" +
		"MEMORY.md 已由系统在首次对话时自动生成基线画像，你用 project_memory 工具补充与修正即可。" +
		"**记忆只存在 .quickDock/ 里**：用 project_memory 工具读写 MEMORY.md；" +
		"绝不要在项目根目录创建或修改 AGENTS.md / README.md / memory.md——AGENTS.md 是「项目规则文件」，" +
		"由用户在设置里维护、每轮自动注入系统提示，不是你的记忆，你去改它会污染用户的规则。" +
		"遇到值得长期记住的项目事实（技术栈、关键文件路径、约定、踩过的坑、已做的决策），用 project_memory 写入 MEMORY.md。" + read
}

// ---- 自动生成基线项目画像 ----

// memoryProfileNoiseDirs 扫描顶层时跳过这些目录：要么是依赖/构建产物（node_modules、
// dist），要么是本工具自己的数据目录（.quickDock），要么是以点开头的隐藏目录。
// 写进基线画像只会稀释有效信息，而且每次扫描都把它们数一遍毫无意义。
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

// ensureMemoryProfile 在没有整体记忆时，确定性扫描项目并写一份基线画像到
// <cwd>/.quickDock/MEMORY.md。这样无论用户有没有显式说「建记忆」，首次对话就能
// 把 .quickDock 建起来——而不是让模型自己在项目根目录生成 AGENTS.md 当记忆。
//
// 确定性、不调模型：画像只描述「这是什么项目、长什么样」，判断哪些是值得长期记的
// 事实交给模型（通过 project_memory 工具），避免机器归纳出噪声。
//
// 两个「不写」的分支都是 2026-11-01 补的：以前空目录也会写一份只有标题的画像，
// 于是每条无空间任务的 MEMORY.md 都是同一段模板字——用户看到的就是「记忆全是默认的」。
func ensureMemoryProfile(cwd string) {
	sc := resolveMemoryScope(cwd)
	// 无空间任务的工作目录是系统新建的空目录：这里没有项目可画像，
	// 写出来必然是模板。它们的记忆走共享的 ai_memory/，只由模型自己写。
	if sc.root == "" || !sc.project {
		return
	}
	overall := overallMemoryPath(sc.root)
	if data, err := os.ReadFile(overall); err == nil {
		// 已有真实内容就别覆盖；只剩空模板时允许重生成（项目后来长出文件了）。
		if s := strings.TrimSpace(string(data)); s != "" && !isPlaceholderProfile(s) {
			return
		}
	}
	profile := scanProjectProfile(cwd)
	if strings.TrimSpace(profile) == "" {
		return
	}
	if err := writeOverallMemory(sc.root, profile); err != nil {
		logger.W("[ai] 自动生成项目画像失败 cwd=%s: %v", cwd, err)
	}
}

// isPlaceholderProfile 判断这份 MEMORY.md 是不是「只有标题、没有任何条目」的自动空画像。
//
// 旧的空目录画像长得正是这样：标题 + 两句说明 + 一行备注。它被当成「已有记忆」读进去，
// 每轮白白占掉提示位，还让用户以为记忆是坏的。识别出来就当没有。
func isPlaceholderProfile(s string) bool {
	if !strings.Contains(s, "自动生成的基线") {
		return false
	}
	for _, sec := range []string{"## 技术栈", "## 顶层目录", "## 文件概览"} {
		if strings.Contains(s, sec) {
			return false
		}
	}
	return true
}

// scanProjectProfile 扫一遍顶层结构 + 一层子目录，拼出一段结构化的基线画像。
func scanProjectProfile(cwd string) string {
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return ""
	}
	var dirs, files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if memoryProfileNoiseDirs[name] || strings.HasPrefix(name, ".") {
				continue
			}
			dirs = append(dirs, name)
		} else {
			files = append(files, name)
		}
	}
	sortStrings(dirs)
	sortStrings(files)

	// 技术栈：命中清单文件即记。
	var stacks []string
	for _, m := range memoryProfileManifests {
		if m.exact {
			for _, f := range files {
				if f == m.name {
					stacks = append(stacks, m.stack)
					break
				}
			}
		} else {
			for _, f := range files {
				if strings.HasSuffix(strings.ToLower(f), m.name) {
					stacks = append(stacks, m.stack)
					break
				}
			}
		}
	}

	// 文件类型计数：顶层 + 一层子目录，限量避免扫进 node_modules 这类巨目录。
	extCount := map[string]int{}
	countExt := func(p string) {
		info, e2 := os.Stat(p)
		if e2 != nil || info.IsDir() {
			return
		}
		if ext := strings.ToLower(filepath.Ext(p)); ext != "" {
			extCount[ext]++
		}
	}
	for _, f := range files {
		countExt(filepath.Join(cwd, f))
	}
	capDirs := dirs
	if len(capDirs) > 12 {
		capDirs = capDirs[:12]
	}
	for _, d := range capDirs {
		sub, e3 := os.ReadDir(filepath.Join(cwd, d))
		if e3 != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() {
				continue
			}
			countExt(filepath.Join(cwd, d, se.Name()))
		}
	}
	exts := make([]string, 0, len(extCount))
	for k := range extCount {
		exts = append(exts, k)
	}
	sortStrings(exts)
	if len(exts) > 10 {
		exts = exts[:10]
	}
	var extParts []string
	for _, e := range exts {
		extParts = append(extParts, e+" ×"+itoa(extCount[e]))
	}

	var b strings.Builder
	// 一条有效信息都没有就别生成：只写标题的空画像比没有更糟——它会被当成
	// 「这个项目已经建好记忆了」，模型不再补充，用户看到的永远是那段模板文字。
	if len(stacks) == 0 && len(dirs) == 0 && len(extParts) == 0 {
		return ""
	}
	b.WriteString("# 项目画像（自动生成的基线）\n\n")
	b.WriteString("> 由 AI 在首次对话时自动扫描生成，可用 `project_memory` 工具补充与修正。\n")
	b.WriteString("> 项目规则（约定、禁令）写在 AGENTS.md / .quickdock/rules 里，由用户在设置里维护、每轮自动注入，不要写在这里。\n\n")
	if len(stacks) > 0 {
		b.WriteString("## 技术栈\n- " + strings.Join(stacks, "\n- ") + "\n\n")
	}
	if len(dirs) > 0 {
		show := dirs
		if len(show) > 16 {
			show = show[:16]
		}
		b.WriteString("## 顶层目录\n- " + strings.Join(show, "\n- ") + "\n\n")
	}
	if len(extParts) > 0 {
		b.WriteString("## 文件概览\n- " + strings.Join(extParts, "、") + "\n\n")
	}
	b.WriteString("## 备注\n- 本文件是长期整体记忆：技术栈、关键文件、约定、踩过的坑、已做的决策都记在这里，让后续轮次不必重新探索整个项目。\n")
	return b.String()
}

// writeOverallMemory 写（覆盖）<root>/MEMORY.md。
func writeOverallMemory(root, content string) error {
	p := overallMemoryPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func sortStrings(s []string) { sort.Strings(s) }

func itoa(n int) string { return strconv.Itoa(n) }

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
