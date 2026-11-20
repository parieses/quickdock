package ai

// MEMORY.md 的分区模型（ai_memory_doc.go）的格式守卫。
//
// 用户的原话是「这记忆还是太简陋了」，参照物是工程笔记的形态：常青分区
// （硬规则 / 踩过的坑 / 项目事实 / 决策）+ 分册索引，而不是按天的流水账。
//
// 下面每条都对着「记忆会不会变成一堆互相矛盾的碎片」这个失效模式钉：
//   - 同一条不要重复写（模型换个说法又来一遍很常见）；
//   - 功能改了要能把旧条目**改掉**，不是只在旁边追加一条；
//   - 文件涨到上限时先丢决策/事实，别把硬规则丢了。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func docTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), memoryDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建记忆目录失败: %v", err)
	}
	return root
}

// 新区块按 canonical 顺序落位，且不会因为解析/渲染丢内容。
func TestMemoryDocAddEntries(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	changed := d.addEntries([]memoryEntry{
		{Category: "决策", Text: "先做 1 期，Ask/Agent 二期作废"},
		{Category: "规则", Text: "胶囊/圆形一律用 --radius-full ｜ 为什么：--radius-pill 从未定义，会静默回退成方角"},
		{Category: "事实", Text: "前端类型检查用 vue-tsc --noEmit"},
		{Category: "坑", Text: "dev 期 pre-bundle 换 hash 后旧窗口请求 504 ｜ 根因：说明符写死 ?v= ｜ 修法：识别 Outdated Optimize Dep 后整页 reload"},
	})
	if changed != 4 {
		t.Fatalf("应写入 4 条，实际 %d", changed)
	}
	out := d.render()
	iRules := strings.Index(out, "## "+sectionRules)
	iPits := strings.Index(out, "## "+sectionPits)
	iFacts := strings.Index(out, "## "+sectionFacts)
	iDec := strings.Index(out, "## "+sectionDecisions)
	if iRules < 0 || iPits < 0 || iFacts < 0 || iDec < 0 {
		t.Fatalf("分区缺失:\n%s", out)
	}
	if !(iRules < iPits && iPits < iFacts && iFacts < iDec) {
		t.Errorf("分区顺序不对:\n%s", out)
	}
	// 往返：解析回来还是这 4 条。
	if got := len(parseMemoryDoc(out).allBullets()); got != 4 {
		t.Errorf("往返后条目数 %d，期望 4:\n%s", got, out)
	}
}

func (d memoryDoc) allBullets() []string {
	var out []string
	for _, s := range d.sections {
		out = append(out, s.bullets()...)
	}
	return out
}

// 重复条目不再写第二遍（模型常换个说法重复同一件事）。
func TestMemoryDocDedupes(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	if n := d.addEntries([]memoryEntry{{Category: "事实", Text: "项目用 Vue 3 + Wails v3"}}); n != 1 {
		t.Fatalf("第一次应写入，实际 %d", n)
	}
	// 完全一致 → 不写。
	if n := d.addEntries([]memoryEntry{{Category: "事实", Text: "项目用 Vue 3 + Wails v3"}}); n != 0 {
		t.Errorf("重复条目不该再写，实际写了 %d 条", n)
	}
	// 扩写版（包含旧条目）→ 也不写，避免两条几乎一样的记忆。
	if n := d.addEntries([]memoryEntry{{Category: "事实", Text: "项目用 Vue 3 + Wails v3（桌面端）"}}); n != 0 {
		t.Errorf("包含旧条目的扩写不该再写，实际写了 %d 条", n)
	}
}

// 核心契约：上一轮记的功能这轮被改了，记忆要跟着改，而不是留两条矛盾的。
func TestMemoryDocReplaceEntry(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	d.addEntries([]memoryEntry{
		{Category: "事实", Text: "SSH 面板的多轮历史由前端内存传给后端，不读库"},
		{Category: "规则", Text: "笔记面板没有思考开关"},
	})
	if n := d.addEntries([]memoryEntry{
		{Replace: "SSH 面板的多轮历史", Text: "SSH 面板的多轮历史改为读 ai_messages，前端只传 convId"},
	}); n != 1 {
		t.Fatalf("替换应算一次改动，实际 %d", n)
	}
	out := d.render()
	if strings.Contains(out, "由前端内存传给后端") {
		t.Errorf("旧条目应被改掉:\n%s", out)
	}
	if !strings.Contains(out, "改为读 ai_messages") {
		t.Errorf("新内容没写进去:\n%s", out)
	}
	// 只应该有一条关于 SSH 历史的记录。
	if strings.Count(out, "SSH 面板的多轮历史") != 1 {
		t.Errorf("替换后应只剩一条:\n%s", out)
	}
	// 未被引用的条目不受影响。
	if !strings.Contains(out, "笔记面板没有思考开关") {
		t.Errorf("不该动到别的条目:\n%s", out)
	}
}

// 旧结论彻底不适用时能删掉。
func TestMemoryDocDropEntry(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	d.addEntries([]memoryEntry{
		{Category: "决策", Text: "mac 发布已下架，只发 Windows"},
		{Category: "事实", Text: "官网 i18n 以内容作键"},
	})
	if n := d.addEntries([]memoryEntry{{Drop: "mac 发布已下架"}}); n != 1 {
		t.Fatalf("删除应算一次改动，实际 %d", n)
	}
	out := d.render()
	if strings.Contains(out, "mac 发布已下架") {
		t.Errorf("条目未被删除:\n%s", out)
	}
	if !strings.Contains(out, "官网 i18n") {
		t.Errorf("不该动到别的条目:\n%s", out)
	}
}

// 被替换的条目找不到时，新结论不能丢（退化成一条新事实）。
func TestMemoryDocReplaceMissKeepsNewText(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	d.addEntries([]memoryEntry{{Replace: "根本不存在的旧条目", Text: "新的结论：端口恒 443+80"}})
	if !strings.Contains(d.render(), "新的结论：端口恒 443+80") {
		t.Errorf("旧条目找不到时也不该丢新结论:\n%s", d.render())
	}
}

// 旧版日期小节不丢：升级前写的东西还在，只是不再往里追加。
func TestMemoryDocKeepsLegacySections(t *testing.T) {
	old := "# 项目画像（自动生成的基线）\n\n## 2026-11-18\n- 项目用 Vue 3\n"
	d := parseMemoryDoc(old)
	d.normalizeHead()
	d.addEntries([]memoryEntry{{Category: "规则", Text: "拉控制台程序一律走 internal/sysutil"}})
	out := d.render()
	if strings.Contains(out, "自动生成的基线") {
		t.Errorf("空画像标题应被换成正式标题:\n%s", out)
	}
	if !strings.Contains(out, "## 2026-11-18") || !strings.Contains(out, "项目用 Vue 3") {
		t.Errorf("旧版日期小节不该丢:\n%s", out)
	}
	if !strings.Contains(out, "internal/sysutil") {
		t.Errorf("新条目没写进去:\n%s", out)
	}
}

// 撑到上限时先丢决策/事实，硬规则必须留在文件里。
func TestMemoryDocTrimKeepsRules(t *testing.T) {
	d := parseMemoryDoc("")
	d.normalizeHead()
	// 每类都塞满（单分区 24 条上限），总量远超文件上限。
	long := strings.Repeat("（很长的历史细节）", 16)
	for i := 0; i < memorySectionMaxEntries; i++ {
		d.addEntries([]memoryEntry{
			{Category: "规则", Text: "规则" + itoa(i) + long},
			{Category: "坑", Text: "坑" + itoa(i) + long},
			{Category: "事实", Text: "事实" + itoa(i) + long},
			{Category: "决策", Text: "决策" + itoa(i) + long},
		})
	}
	// 最后再记一条关键硬规则：它是最新的，裁剪绝不能把它丢掉。
	d.addEntries([]memoryEntry{{Category: "规则", Text: "硬规则：删文件走回收站 platform.MoveToTrash"}})

	if n := len([]rune(d.render())); n <= memoryAutoFileCap {
		t.Fatalf("用例没撑到上限（%d ≤ %d），裁不到东西", n, memoryAutoFileCap)
	}
	d.trimToCap(memoryAutoFileCap)
	out := d.render()
	if n := len([]rune(out)); n > memoryAutoFileCap {
		t.Errorf("没裁到上限: %d > %d", n, memoryAutoFileCap)
	}
	if !strings.Contains(out, "platform.MoveToTrash") {
		t.Errorf("硬规则被裁掉了:\n%s", out)
	}
	// 决策/事实先走：这两类应该被裁到一条不剩或接近清空。
	back := parseMemoryDoc(out)
	if dec := back.section(sectionDecisions); dec != nil && len(dec.bullets()) > 2 {
		t.Errorf("先裁的应该是决策（还剩 %d 条）", len(dec.bullets()))
	}
}

// 分册索引由文件系统生成：有文件就列，删掉就消失，标题取第一行 `# `。
func TestDistillIndex(t *testing.T) {
	root := docTestRoot(t)
	dir := distillDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01-插件.md"), []byte("# 插件系统全史（决策与坑）\n\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-截图.md"), []byte("没有一级标题\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := parseMemoryDoc("")
	d.normalizeHead()
	d.setDistillIndex(root)
	out := d.render()
	if !strings.Contains(out, "`memory/_distill/01-插件.md`") || !strings.Contains(out, "插件系统全史（决策与坑）") {
		t.Errorf("分册索引缺少带标题的条目:\n%s", out)
	}
	if !strings.Contains(out, "`memory/_distill/02-截图.md`") {
		t.Errorf("没有一级标题的分册也要列出来:\n%s", out)
	}
	// 文件被删掉后，索引下一轮自动消失。
	if err := os.Remove(filepath.Join(dir, "02-截图.md")); err != nil {
		t.Fatal(err)
	}
	d.setDistillIndex(root)
	if strings.Contains(d.render(), "02-截图") {
		t.Errorf("已删除的分册还留在索引里:\n%s", d.render())
	}
}

// 落盘往返：updateOverallMemory 会补标题、写索引、裁上限。
func TestUpdateOverallMemoryWritesFile(t *testing.T) {
	root := docTestRoot(t)
	if err := os.WriteFile(overallMemoryPath(root), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := updateOverallMemory(root, []memoryEntry{
		{Category: "规则", Text: "插件禁用后仍须登记进 m.plugins"},
		{Category: "坑", Text: "sqlite PRAGMA 是连接级 ｜ 修法：走 DSN 下发"},
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	data, err := os.ReadFile(overallMemoryPath(root))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, memoryDocTitle) {
		t.Errorf("应补上正式标题:\n%s", s)
	}
	if !strings.Contains(s, "## "+sectionRules) || !strings.Contains(s, "## "+sectionPits) {
		t.Errorf("分区缺失:\n%s", s)
	}
	if !strings.Contains(s, "## "+sectionIndex) {
		t.Errorf("分册索引缺失:\n%s", s)
	}
	if isPlaceholderProfile(s) {
		t.Errorf("有内容时不该判成空画像:\n%s", s)
	}
	// 二次写入不该重复条目。
	if err := updateOverallMemory(root, []memoryEntry{{Category: "规则", Text: "插件禁用后仍须登记进 m.plugins"}}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(overallMemoryPath(root))
	if strings.Count(string(data), "插件禁用后仍须登记") != 1 {
		t.Errorf("重复写入:\n%s", string(data))
	}
}

// 项目基线：扫描出的应该是「怎么构建」这类每次任务开头都要用的信息，
// 而不是旧版那种「.go ×8」的计数。
func TestScanProjectBaseline(t *testing.T) {
	cwd := t.TempDir()
	writeTestFile(t, filepath.Join(cwd, "go.mod"), "module quickdock\n\ngo 1.26\n")
	writeTestFile(t, filepath.Join(cwd, "frontend", "package.json"),
		`{"name":"qd-front","dependencies":{"vue":"^3.4.21"},"scripts":{"build":"vite build","check:i18n":"node x.mjs"}}`)
	writeTestFile(t, filepath.Join(cwd, "Taskfile.yml"),
		"version: '3'\n\ntasks:\n  build:\n    cmds:\n      - wails3 build\n  dev:\n    cmds:\n      - wails3 dev\n")

	joined := strings.Join(scanProjectBaseline(cwd), "\n")
	for _, want := range []string{"Go 1.26", "Vue 3", "模块：quickdock", "task build", "task dev", "npm run check:i18n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("基线缺少 %q:\n%s", want, joined)
		}
	}
	// 依赖/构建产物目录不该出现。
	if strings.Contains(joined, "node_modules/") || strings.Contains(joined, "frontend/node_modules") {
		t.Errorf("噪声目录不该进基线:\n%s", joined)
	}
}

// 首次对话自动建基线：只建一次，而且写进「项目基线」分区。
func TestEnsureMemoryProfileWritesBaselineOnce(t *testing.T) {
	cwd := t.TempDir()
	writeTestFile(t, filepath.Join(cwd, "go.mod"), "module quickdock\n\ngo 1.26\n")
	ensureMemoryProfile(cwd)

	p := filepath.Join(cwd, memoryDirName, "MEMORY.md")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("应自动生成 MEMORY.md: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "## "+sectionBaseline) || !strings.Contains(s, "Go 1.26") {
		t.Errorf("基线没写进去:\n%s", s)
	}
	if !strings.HasPrefix(s, memoryDocTitle) {
		t.Errorf("标题应是正式标题:\n%s", s)
	}
	// 第二轮不再重写（已有真实内容）。
	ensureMemoryProfile(cwd)
	data, _ = os.ReadFile(p)
	if n := strings.Count(string(data), "## "+sectionBaseline); n != 1 {
		t.Errorf("基线分区应只有一个，实际 %d:\n%s", n, string(data))
	}
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 围栏代码块里的 `## ` 是正文，不是分区标题。
//
// 失效场景：用户在记忆里贴了一段 markdown 片段，解析器按行首 `## ` 切分区，
// 文件结构被从中间撕开，块内的 `- ` 行还会被 trim 逻辑当成条目删掉。
func TestParseMemoryDocIgnoresFencedHeadings(t *testing.T) {
	content := "# 长期记忆\n\n" +
		"## 硬规则\n\n- 真规则一\n\n" +
		"```markdown\n## 这不是分区标题\n- 这是示例代码里的行\n```\n\n" +
		"## 项目事实\n\n- 事实一\n"
	d := parseMemoryDoc(content)
	if len(d.sections) != 2 {
		t.Fatalf("应只有 2 个分区，实际 %d：%+v", len(d.sections), d.sections)
	}
	if d.sections[0].title != sectionRules || d.sections[1].title != sectionFacts {
		t.Fatalf("分区标题被代码块里的 ## 打乱：%v / %v", d.sections[0].title, d.sections[1].title)
	}
	body := strings.Join(d.sections[0].lines, "\n")
	if !strings.Contains(body, "## 这不是分区标题") || !strings.Contains(body, "- 这是示例代码里的行") {
		t.Errorf("代码块内容应原样留在所属分区里:\n%s", body)
	}
}

// 已有条目很短时，新写的具体条目不能被「子串包含」判成重复。
//
// 旧写法只对 needle（新条目）做 12 字下限，hay（已有条目）多短都参与包含判断，
// 于是「用 Go」三个字能把「用 Go 写 CLI 时必须先跑 gofmt」整条吞掉。
func TestMemoryEntryExistsShortBulletNotDuplicate(t *testing.T) {
	content := "## 项目事实\n\n- 用 Go\n"
	d := parseMemoryDoc(content)
	long := "用 Go 写命令行工具时必须先跑 gofmt 再提交"
	if memoryEntryExists(&d, long) {
		t.Error("短碎片条目不该让具体的新条目被判重复")
	}
	// 两边都够长时仍然要判重（换个说法又写一遍是真实存在的失效模式）。
	d2 := parseMemoryDoc("## 项目事实\n\n- 用 Go 写命令行工具必须先跑 gofmt 再提交\n")
	if !memoryEntryExists(&d2, "用 Go 写命令行工具必须先跑 gofmt 再提交，否则 CI 会红") {
		t.Error("已有条目够长时，扩写版本应判为重复")
	}
}

// 未登记分区必须稳定落在「最后一个登记分区」与「索引」之间。
//
// 原来直接返回 len-1，与最后一个登记分区同分：稳定排序下位置取决于文件里的
// 原顺序，渲染顺序不稳定，而 trimToCap 从后往前丢条目时它是第一个被丢的。
func TestMemorySectionRankUnknownBetweenLastAndIndex(t *testing.T) {
	lastRegistered := memorySectionOrder[len(memorySectionOrder)-2] // 索引之前的那一个
	unknown := memorySectionRank("## 2026-11-20 旧流水账")
	if unknown <= memorySectionRank(lastRegistered) {
		t.Errorf("未登记分区(%d)应排在最后一个登记分区(%d)之后", unknown, memorySectionRank(lastRegistered))
	}
	if unknown >= memorySectionRank(sectionIndex) {
		t.Errorf("未登记分区(%d)应排在索引(%d)之前", unknown, memorySectionRank(sectionIndex))
	}
}
