package ai

// MEMORY.md 的结构：常青分区，而不是按天的流水账。
//
// 旧形态是「每整理一次就在文件末尾开一个 `## 2026-11-20` 小节、塞几条短句」，
// 结果是：越重要的东西（硬规则、踩过的坑）越容易被日期小节淹掉，
// 而且同一天里越晚写的内容越靠后，注入时按 6000 字从头截——最新的事实反而先被裁掉。
//
// 现在的口径对齐工程笔记的形态（参考 .workbuddy/memory/MEMORY.md）：
//
//	# 长期记忆
//	> 引言（记忆在哪、怎么用）
//	## 项目基线        ← 确定性扫描生成，随项目长出来而更新
//	## 硬规则          ← 一条规则 + 为什么；违反过一次的才值得写
//	## 踩过的坑        ← 现象 · 根因 · 修法
//	## 项目事实        ← 技术栈、入口、关键路径
//	## 决策            ← 已拍板且会被反复问到的取舍
//	## 分册索引        ← 自动扫描 memory/_distill/ 生成
//
// 为什么分区而不是继续用日期：日期只回答「什么时候记的」，分区回答「这是什么」。
// 注入给模型时它要的是后者（「有没有关于构建的坑」），前者还得靠模型自己再归纳一遍。
// 按天的过程记录仍然保留——在 memory/YYYY-MM-DD.md 里，那才是它们的归属。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"quickdock/internal/logger"
)

// memoryAutoFileMu 串行化 MEMORY.md 的「读 → 改 → 裁 → 写」。
//
// 只罩本地文件 IO（几十微秒），不罩那次模型调用：早先锁在发请求前加、
// 收到回复后才解，三个会话同时结束就串行 3×45 秒。锁下沉到文件这一层之后，
// 代价是去重依据可能来自几百毫秒前的快照——后果只是偶尔多一条重复条目。
var memoryAutoFileMu sync.Mutex

const (
	// memoryDocTitle / memoryDocIntro 新建 MEMORY.md 时的标题与引言。
	memoryDocTitle = "# 长期记忆"
	memoryDocIntro = "> 常青记忆：项目事实、硬规则、踩过的坑与决策。按天的工作笔记在 `memory/`，" +
		"专题细节在 `memory/_distill/`（索引见文末）。"
	// 分区名。改这些字符串等于改文件格式：解析、注入、工具读出来的都是它们。
	sectionBaseline  = "项目基线"
	sectionRules     = "硬规则"
	sectionPits      = "踩过的坑"
	sectionFacts     = "项目事实"
	sectionDecisions = "决策"
	sectionIndex     = "分册索引"

	// memorySectionMaxEntries 单个分区最多留几条。
	//
	// 有上限才有「这分区里都是精华」的效果：不封顶时一个上午就能攒出四十条
	// 「项目事实」，注入时把真正该看的规则挤到 6000 字以外。
	memorySectionMaxEntries = 24
	// memoryIndexMaxEntries 分册索引最多列几条，其余折成「…等 N 个」。
	memoryIndexMaxEntries = 20
	// memoryEntryMaxRune 单条上限。规则/坑要写清为什么，比旧版的 60 字放宽了，
	// 但仍然拒绝整段复制——记忆是索引，不是文档。
	memoryEntryMaxRune = 180

	// memoryAutoFileCap MEMORY.md 的体积上限，超出从最不常青的分区丢起。
	//
	// 必须 ≤ memoryCapOverall（注入时 clipRunes 从头截的上限）：文件比注入上限
	// 还长的话，被截掉的正好是文件末尾，也就是**最新**的那几段——与这里
	// 「宁可删旧的」的意图完全相反，症状是「刚记住的事下一轮就不见了」。
	memoryAutoFileCap = memoryCapOverall
)

// memorySectionOrder 分区渲染顺序（也是「新建分区插在哪」的依据）。
var memorySectionOrder = []string{
	sectionBaseline, sectionRules, sectionPits, sectionFacts, sectionDecisions, sectionIndex,
}

// memorySectionRank 分区的排序权重。
//
// 权重按 2 递增，留出一档给「未登记分区」：它们（模型自己写的、旧版按日期写的
// 流水账小节）稳定落在最后一个登记分区与索引之间——渲染时在索引之前，而
// trimToCap 从数组末尾往前丢条目，于是它们是第二个被淘汰的对象（第一个是
// 索引，但它被跳过）。日期流水账过期最快，这个淘汰次序是有意的；模型新增
// 分区都走 ensureSection 用规范名，不会落进这个兜底分支。
func memorySectionRank(title string) int {
	for i, s := range memorySectionOrder {
		if s == title {
			return i * 2
		}
	}
	// 原来直接返回 len-1，会与最后一个登记分区同分：稳定排序下位置取决于
	// 文件里的原顺序，既可能抢在索引前面，也可能留在中间——同一个文件两次
	// 读出来的渲染顺序因此不保证一致。
	return (len(memorySectionOrder)-1)*2 - 1
}

// memorySection 一个 `## 标题` 分区。
type memorySection struct {
	title string
	lines []string
}

// bullets 分区里的条目行（`- ` 开头）。空行与说明行不计。
func (s memorySection) bullets() []string {
	out := make([]string, 0, len(s.lines))
	for _, l := range s.lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- ") {
			out = append(out, strings.TrimPrefix(strings.TrimSpace(l), "- "))
		}
	}
	return out
}

// memoryDoc MEMORY.md 的内存模型。
type memoryDoc struct {
	head     string // 第一个 `## ` 之前的内容（标题 + 引言）
	sections []memorySection
}

// parseMemoryDoc 解析 MEMORY.md。不认识的结构不丢弃：全部留在 sections 里，
// 只是渲染回原样——记忆文件是用户可读的资产，解析器不该成为它的单点故障。
func parseMemoryDoc(content string) memoryDoc {
	var d memoryDoc
	s := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// 围栏代码块里的 `## ` 是正文（用户贴进来的一段 markdown），不是分区标题。
	// 认了它会把文件结构从中间撕开，块内的 `- ` 行还会被当成条目交给裁剪逻辑删掉。
	inFence := make([]bool, len(lines))
	fence := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			// 围栏行自身也算在代码块内：它不以 `## ` 开头，归不归都不影响解析，
			// 但标进来后「未闭合的坏文件」行为更容易推理。
			inFence[i] = true
			fence = !fence
			continue
		}
		inFence[i] = fence
	}
	isHeading := func(i int) bool {
		return !inFence[i] && strings.HasPrefix(lines[i], "## ")
	}
	i := 0
	for i < len(lines) && !isHeading(i) {
		i++
	}
	d.head = strings.TrimRight(strings.Join(lines[:i], "\n"), "\n")
	for i < len(lines) {
		title := strings.TrimSpace(strings.TrimPrefix(lines[i], "## "))
		i++
		start := i
		for i < len(lines) && !isHeading(i) {
			i++
		}
		body := strings.TrimRight(strings.Join(lines[start:i], "\n"), "\n")
		var bl []string
		if body != "" {
			bl = strings.Split(body, "\n")
		}
		d.sections = append(d.sections, memorySection{title: title, lines: bl})
	}
	return d
}

// section 取指定分区（没有时返回 nil）。
func (d *memoryDoc) section(title string) *memorySection {
	for i := range d.sections {
		if d.sections[i].title == title {
			return &d.sections[i]
		}
	}
	return nil
}

// ensureSection 取指定分区，没有就按 canonical 顺序插进去。
func (d *memoryDoc) ensureSection(title string) *memorySection {
	if s := d.section(title); s != nil {
		return s
	}
	rank := memorySectionRank(title)
	at := len(d.sections)
	for i := range d.sections {
		if memorySectionRank(d.sections[i].title) > rank {
			at = i
			break
		}
	}
	d.sections = append(d.sections, memorySection{})
	copy(d.sections[at+1:], d.sections[at:])
	d.sections[at] = memorySection{title: title}
	return &d.sections[at]
}

// sortSections 按 canonical 顺序重排（稳定：同权重保持原有相对顺序）。
func (d *memoryDoc) sortSections() {
	sort.SliceStable(d.sections, func(i, j int) bool {
		return memorySectionRank(d.sections[i].title) < memorySectionRank(d.sections[j].title)
	})
}

// setSectionLines 整段替换一个分区的正文。
func (d *memoryDoc) setSectionLines(title string, lines []string) {
	s := d.ensureSection(title)
	s.lines = lines
}

// normalizeHead 把「机器生成的空画像」换成正式标题。
//
// 只换能确定是机器产物的那种：模型自己写过的标题（比如 `# QuickDock 项目记忆`）
// 是它的排版意图，覆盖掉会让用户以为文件被重置了。
func (d *memoryDoc) normalizeHead() {
	h := strings.TrimSpace(d.head)
	if h == "" || strings.Contains(h, "自动生成的基线") || strings.Contains(h, "项目画像") {
		d.head = newMemoryHead()
	}
}

func newMemoryHead() string {
	return memoryDocTitle + "\n\n" + memoryDocIntro
}

// memoryEntry 一条待写入的常青条目。
//
// 三种形态，对应「新记一条 / 改掉旧的 / 作废旧的」：
//   - 普通条目：Category + Text。
//   - 更新：Replace 是旧条目的可识别片段，Text 是改后的内容。
//   - 作废：Drop 是旧条目的可识别片段，Text 为空。
//
// 为什么需要 Replace/Drop：项目是活的，上一轮记下的「新增了 X 功能」下一轮
// 可能就被改成别的行为。只会追加的记忆会攒出一堆互相矛盾的条目，模型读到
// 哪条全看运气——比没有记忆更糟。所以整理时必须能把旧结论改掉。
type memoryEntry struct {
	Category string
	Text     string
	Replace  string
	Drop     string
}

// addEntries 把条目并入对应分区：去重、替换、作废、限长、单分区限量。
//
// 返回真正改动过的条数（全被去重时为 0）。调用方据此决定是否还要写文件。
func (d *memoryDoc) addEntries(entries []memoryEntry) int {
	changed := 0
	for _, e := range entries {
		switch {
		case e.Drop != "":
			changed += d.dropEntriesMatching(e.Drop)
		case e.Replace != "":
			text := clipRunes(collapseSpaces(e.Text), memoryEntryMaxRune)
			if text == "" {
				continue
			}
			if d.replaceEntry(e.Replace, text) {
				changed++
				continue
			}
			// 旧条目找不到（可能是被裁掉了 / 模型记错了引用）：别把新结论丢了，
			// 退化成一条新的「项目事实」。两条并存好过一条都没有。
			if d.appendEntry(memoryEntry{Category: normalizeMemoryCategory(e.Category), Text: text}) {
				changed++
			}
		default:
			if d.appendEntry(e) {
				changed++
			}
		}
	}
	if changed > 0 {
		d.sortSections()
	}
	return changed
}

// appendEntry 追加一条（去重 + 限长 + 限量）。
func (d *memoryDoc) appendEntry(e memoryEntry) bool {
	text := clipRunes(collapseSpaces(e.Text), memoryEntryMaxRune)
	if text == "" {
		return false
	}
	sec := d.ensureSection(normalizeMemoryCategory(e.Category))
	if memoryEntryExists(d, text) {
		return false
	}
	sec.lines = append(sec.lines, "- "+text)
	trimSectionEntries(sec, memorySectionMaxEntries)
	return true
}

// replaceEntry 把包含 ref 的旧条目原地换成 text（保留它所在的分区与位置）。
func (d *memoryDoc) replaceEntry(ref, text string) bool {
	needle := normalizeMemoryText(ref)
	if utf8.RuneCountInString(needle) < 4 {
		return false
	}
	for si := range d.sections {
		sec := &d.sections[si]
		if sec.title == sectionIndex || sec.title == sectionBaseline {
			continue
		}
		for li, line := range sec.lines {
			body := strings.TrimSpace(line)
			if !strings.HasPrefix(body, "- ") {
				continue
			}
			if !strings.Contains(normalizeMemoryText(body), needle) {
				continue
			}
			sec.lines[li] = "- " + text
			return true
		}
	}
	return false
}

// dropEntriesMatching 删掉所有包含 ref 的条目，返回删掉的条数。
func (d *memoryDoc) dropEntriesMatching(ref string) int {
	needle := normalizeMemoryText(ref)
	if utf8.RuneCountInString(needle) < 4 {
		return 0
	}
	dropped := 0
	for si := range d.sections {
		sec := &d.sections[si]
		if sec.title == sectionIndex || sec.title == sectionBaseline {
			continue
		}
		kept := make([]string, 0, len(sec.lines))
		for _, line := range sec.lines {
			body := strings.TrimSpace(line)
			if strings.HasPrefix(body, "- ") && strings.Contains(normalizeMemoryText(body), needle) {
				dropped++
				continue
			}
			kept = append(kept, line)
		}
		sec.lines = kept
	}
	return dropped
}

// memoryEntryExists 判断这条是否已经记过（跨分区查：模型换个分区重复写同一件事很常见）。
//
// 用「包含」而不是「相等」：模型常把已有条目扩写一遍（加了个「为什么」）。
// 阈值 12 字是为了避免「用 Go」这种短句被误判成重复。
func memoryEntryExists(d *memoryDoc, text string) bool {
	needle := normalizeMemoryText(text)
	if utf8.RuneCountInString(needle) < 12 {
		return false
	}
	for _, s := range d.sections {
		if s.title == sectionIndex {
			continue
		}
		for _, b := range s.bullets() {
			hay := normalizeMemoryText(b)
			if hay == needle {
				return true
			}
			// 双向包含只在**短的那条也够长**时才算重复：已有条目是「用 Go」
			// 这种三字碎片时，新写的具体条目会被子串关系判成重复而丢弃，
			// 吞掉的正是这次新拿到的信息。12 字下限必须对两边同时生效。
			if utf8.RuneCountInString(hay) >= 12 &&
				(strings.Contains(hay, needle) || strings.Contains(needle, hay)) {
				return true
			}
		}
	}
	return false
}

// normalizeMemoryText 去掉排版噪声后用于比较：粗体标记、空白、大小写都不该影响「是不是同一条」。
func normalizeMemoryText(s string) string {
	s = strings.NewReplacer("**", "", "`", "", "　", " ").Replace(s)
	return strings.ToLower(collapseSpaces(s))
}

// normalizeMemoryCategory 把模型给的类别归一到分区名。
//
// 认不出来时落到「项目事实」而不是新建分区：多一个拼错的分区名会让注入的记忆
// 里混进十几个同义分区，读起来比不分区更糟。
func normalizeMemoryCategory(cat string) string {
	c := strings.ToLower(strings.TrimSpace(cat))
	switch {
	case strings.Contains(c, "规则"), strings.Contains(c, "rule"), strings.Contains(c, "约束"):
		return sectionRules
	case strings.Contains(c, "坑"), strings.Contains(c, "pitfall"), strings.Contains(c, "故障"), strings.Contains(c, "bug"):
		return sectionPits
	case strings.Contains(c, "决策"), strings.Contains(c, "decision"), strings.Contains(c, "取舍"):
		return sectionDecisions
	default:
		return sectionFacts
	}
}

// trimSectionEntries 单分区超量时丢最早的条目。
func trimSectionEntries(s *memorySection, max int) {
	if max <= 0 {
		return
	}
	n := 0
	for _, l := range s.lines {
		if strings.HasPrefix(strings.TrimSpace(l), "- ") {
			n++
		}
	}
	for n > max {
		dropped := false
		for i, l := range s.lines {
			if !strings.HasPrefix(strings.TrimSpace(l), "- ") {
				continue
			}
			s.lines = append(s.lines[:i:i], s.lines[i+1:]...)
			dropped = true
			break
		}
		if !dropped {
			return
		}
		n--
	}
}

// render 渲染回 Markdown。空分区不输出——一个只有标题的分区会让读的人以为内容丢了。
func (d memoryDoc) render() string {
	var b strings.Builder
	head := strings.TrimSpace(d.head)
	if head == "" {
		head = newMemoryHead()
	}
	b.WriteString(head)
	b.WriteString("\n")
	for _, s := range d.sections {
		body := strings.TrimRight(strings.Join(s.lines, "\n"), "\n")
		if strings.TrimSpace(body) == "" && s.title != sectionBaseline {
			continue
		}
		b.WriteString("\n## " + s.title + "\n")
		if body != "" {
			b.WriteString(body + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// trimToCap 把整份文档压进 cap：从最不常青的分区开始丢条目。
//
// 顺序是「决策 → 事实 → 坑 → 规则」：规则是踩过坑之后才写下的结论，最不该丢；
// 事实与决策会随项目演进过期，先走。「项目基线」与「分册索引」不参与裁剪
// ——它们由机器生成，删了下一轮又会被写回来，只是白忙。
func (d *memoryDoc) trimToCap(cap int) {
	if cap <= 0 {
		return
	}
	for utf8.RuneCountInString(d.render()) > cap {
		if !d.dropOldestEntry() {
			return
		}
	}
}

func (d *memoryDoc) dropOldestEntry() bool {
	for i := len(d.sections) - 1; i >= 0; i-- {
		s := &d.sections[i]
		if s.title == sectionBaseline || s.title == sectionIndex {
			continue
		}
		for j, l := range s.lines {
			if !strings.HasPrefix(strings.TrimSpace(l), "- ") {
				continue
			}
			s.lines = append(s.lines[:j:j], s.lines[j+1:]...)
			return true
		}
	}
	return false
}

// distillDir 主题分册目录（memory/_distill）。
func distillDir(root string) string { return filepath.Join(root, "memory", "_distill") }

// distillIndexLines 扫描 memory/_distill/ 生成索引行。
//
// 索引由文件系统决定，而不是让模型自己维护一份：模型会忘、会漏、会在改名后
// 留下一条指向不存在文件的条目。扫描出来的永远与磁盘一致，代价是分册标题
// 必须写在文件第一行 `# 标题`（写不出来就退回文件名）。
func distillIndexLines(root string) []string {
	entries, err := os.ReadDir(distillDir(root))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		title := ""
		if data, rerr := os.ReadFile(filepath.Join(distillDir(root), name)); rerr == nil {
			title = firstMarkdownHeading(string(data))
		}
		line := "- `memory/_distill/" + name + "`"
		if title != "" {
			line += focusLineSep + title
		}
		out = append(out, line)
	}
	return out
}

// firstMarkdownHeading 取内容里第一个 `# 标题`（只认一级标题）。
func firstMarkdownHeading(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// setDistillIndex 用磁盘扫描结果刷新「分册索引」分区。
func (d *memoryDoc) setDistillIndex(root string) {
	lines := distillIndexLines(root)
	if len(lines) == 0 {
		lines = []string{
			"（还没有分册。某个主题的细节超过十来条时，把整段历史整理到 `memory/_distill/NN-主题.md`，" +
				"这里会自动列出来；MEMORY.md 只留结论与规则。）",
		}
	} else {
		// 索引也占注入预算：分册多到二十个以上时，留在 MEMORY.md 里的只该是
		// 「有哪几本」，具体哪本要看目录。不封顶的话它会把规则挤到 7000 字以外。
		if len(lines) > memoryIndexMaxEntries {
			rest := len(lines) - memoryIndexMaxEntries
			lines = append(lines[:memoryIndexMaxEntries:memoryIndexMaxEntries],
				fmt.Sprintf("…等 %d 个分册（完整清单在 memory/_distill/）", rest))
		}
		lines = append(lines, "", "> 本节由工具扫描 `memory/_distill/` 自动生成，不要手改。")
	}
	d.setSectionLines(sectionIndex, lines)
	d.sortSections()
}

// updateOverallMemory 读-改-写 MEMORY.md：并入新条目、刷新分册索引、裁回上限。
//
// 锁只罩文件读写（不罩那次模型调用）：这一段是 read-modify-write，
// 两个任务同时结束各自读旧内容再各自覆盖就是丢条目。
func updateOverallMemory(root string, entries []memoryEntry) error {
	memoryAutoFileMu.Lock()
	defer memoryAutoFileMu.Unlock()

	p := overallMemoryPath(root)
	cur := ""
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		cur = string(data)
	case !os.IsNotExist(err):
		// 读失败绝不能当成「文件是空的」：紧接着的写入会把整份 MEMORY.md
		// 覆盖成只剩这次新加的几条。同步盘 / 杀软短暂占用、目录权限抖动
		// 都会走到这里，而记忆是用户资产，宁可这次不写。
		return err
	}
	d := parseMemoryDoc(cur)
	d.normalizeHead()
	d.addEntries(entries)
	d.setDistillIndex(root)
	d.trimToCap(memoryAutoFileCap)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(d.render()), 0o644)
}

// updateMemoryBaseline 写入/更新「项目基线」分区（确定性扫描，不调模型）。
func updateMemoryBaseline(root string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	memoryAutoFileMu.Lock()
	defer memoryAutoFileMu.Unlock()

	p := overallMemoryPath(root)
	cur := ""
	data, err := os.ReadFile(p)
	switch {
	case err == nil:
		cur = string(data)
	case !os.IsNotExist(err):
		// 同上：读失败时 cur 若当成空串，写回就等于把记忆清空。
		return err
	}
	d := parseMemoryDoc(cur)
	d.normalizeHead()
	// 基线是机器产物：整段替换，避免每次启动都追加一遍。
	d.setSectionLines(sectionBaseline, prefixBullets(lines))
	d.setDistillIndex(root)
	d.trimToCap(memoryAutoFileCap)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(d.render()), 0o644)
}

// prefixBullets 给裸文本行补上 `- `。
func prefixBullets(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "- ") {
			l = "- " + l
		}
		out = append(out, l)
	}
	return out
}

// refreshMemoryIndex 只刷新 MEMORY.md 的「分册索引」分区（模型用工具刚写了分册）。
//
// 只刷新、不新建：还没建记忆的项目不该因为写了一个分册就凭空多出一份 MEMORY.md。
func refreshMemoryIndex(cwd string) {
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		return
	}
	p := overallMemoryPath(sc.root)
	memoryAutoFileMu.Lock()
	defer memoryAutoFileMu.Unlock()
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	d := parseMemoryDoc(string(data))
	d.normalizeHead()
	d.setDistillIndex(sc.root)
	d.trimToCap(memoryAutoFileCap)
	if werr := os.WriteFile(p, []byte(d.render()), 0o644); werr != nil {
		logger.W("[ai] 刷新分册索引失败 root=%s: %v", sc.root, werr)
	}
}
