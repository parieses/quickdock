package ai

// 「工程笔记」这条链路的守卫：模型输出怎么解析、笔记怎么落进当天的小节。
//
// 这条链路的价值全在「下次能直接用」：现象、根因、修法、门禁。
// 下面每条都对着一个真实的失效模式：
//   - 模型输出格式漂了 → 宁可只记笔记，也不要把散文灌进常青记忆；
//   - 笔记写错小节 → 一个任务的历史里混进别人的记录；
//   - 模型没按标记输出 → 不能整份丢掉（活动行之外就一点信息都没有了）。

import (
	"strings"
	"testing"
	"time"

	"quickdock/internal/db"
)

func TestParseTaskMemoryOutput(t *testing.T) {
	raw := "以下是提炼结果：\n" +
		"<笔记>\n" +
		"- 需求：修好笔记编辑器的 504\n" +
		"- 做了什么：utils/aiCodeEditor.ts 的 importWithRetry 改成 dev 下整页 reload\n" +
		"- 结论：504 不再复现\n" +
		"- 坑：dev 期 pre-bundle 换过 hash，已打开的窗口拿着旧模块图\n" +
		"- 门禁：vue-tsc EXIT=0；vitest 34/473 全绿\n" +
		"</笔记>\n" +
		"<长期>\n" +
		"- [坑] 编辑器狂刷 CodeMirror 504 ｜ 根因：?v= 写死在说明符里 ｜ 修法：识别错误后整页 reload\n" +
		"- [规则] 记忆整理调用必须记进用量 ｜ 为什么：不记会让统计系统性少算\n" +
		"- [事实] 前端门禁脚本在 frontend/scripts/\n" +
		"- [取代] 笔记编辑器不支持 CodeMirror → 笔记编辑器已支持 CodeMirror（懒加载 + 失败降级纯文本）\n" +
		"- [作废] 旧的门禁脚本放在根目录 scripts/\n" +
		"- 随便一句没有标签的话\n" +
		"</长期>"

	note, entries := parseTaskMemoryOutput(raw)
	if len(note) != 5 {
		t.Fatalf("笔记应有 5 行，实际 %d: %v", len(note), note)
	}
	if !strings.HasPrefix(note[0], "需求：") {
		t.Errorf("第一行应是需求，实际 %q", note[0])
	}
	if len(entries) != 5 {
		t.Fatalf("长期应有 5 条（无标签那行被丢），实际 %d: %+v", len(entries), entries)
	}
	if entries[0].Category != sectionPits || entries[1].Category != sectionRules || entries[2].Category != sectionFacts {
		t.Errorf("类别映射不对: %+v", entries)
	}
	if entries[3].Replace == "" || !strings.Contains(entries[3].Text, "已支持 CodeMirror") {
		t.Errorf("[取代] 解析不对: %+v", entries[3])
	}
	if entries[4].Drop == "" {
		t.Errorf("[作废] 解析不对: %+v", entries[4])
	}
}

// 没有任何标记时：只当笔记用，绝不往 MEMORY.md 里写无法归类的长句。
func TestParseTaskMemoryOutputNoMarkers(t *testing.T) {
	raw := "- 需求：看看这个按钮在哪\n- 结论：在设置页的底部\n"
	note, entries := parseTaskMemoryOutput(raw)
	if len(note) != 2 {
		t.Fatalf("应退回笔记模式，实际 %d 行", len(note))
	}
	if len(entries) != 0 {
		t.Errorf("没有标记时不该写常青条目: %+v", entries)
	}
}

// 两块都可能只在说「没有」。
func TestParseTaskMemoryOutputNone(t *testing.T) {
	note, entries := parseTaskMemoryOutput("<笔记>\n- 无\n</笔记>\n<长期>\nNONE\n</长期>")
	if len(note) != 0 || len(entries) != 0 {
		t.Errorf("NONE 应解析为空: note=%v entries=%+v", note, entries)
	}
}

// 撞上 max_tokens 被截断（有开标签、没闭合）时整份丢弃。
//
// 旧写法不闭合也返回 rest,true，于是半截输出的最后一行（往往是被切开的残句）
// 会被硬解析成条目写进 MEMORY.md，一次截断就永久钉进文件。
func TestParseTaskMemoryOutputTruncated(t *testing.T) {
	raw := "<笔记>\n- 需求：改缓存策略\n\n<长期>\n- [规则] 缓存前缀必须逐字节稳定 ｜ 为什么：一旦"
	note, entries := parseTaskMemoryOutput(raw)
	if len(note) != 0 || len(entries) != 0 {
		t.Errorf("截断输出应整份丢弃，实际 note=%v entries=%+v", note, entries)
	}
}

// 类别词必须明确：认不出来的标签丢弃，不要兜底成「项目事实」灌进去。
func TestParseMemoryEntryRejectsUnknownTag(t *testing.T) {
	got := parseMemoryEntryLines("- [随想] 今天天气不错\n- [规则] 删文件走回收站 ｜ 为什么：os.Remove 不可恢复\n")
	if len(got) != 1 || got[0].Category != sectionRules {
		t.Fatalf("只应留下规则那条: %+v", got)
	}
}

// 笔记落进对应任务小节，且不影响别的任务。
func TestAppendDailyNote(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-a", cwd, "修笔记编辑器", "504 又来了", nil, 3, 9)
	appendDailyMemory("conv-b", cwd, "另一个任务", "别的事", nil, 1, 2)

	appendDailyNote("conv-a", cwd, "修笔记编辑器", "504 又来了", time.Now(), []string{
		"需求：修好 504",
		"根因：pre-bundle 换了 hash",
	})

	content := readFile(t, dailyFileOf(t, cwd))
	if !strings.Contains(content, "#### ") || !strings.Contains(content, "· 笔记") {
		t.Fatalf("缺少笔记小节:\n%s", content)
	}
	// 笔记必须落在 conv-a 的小节里（在下一个 `## ` 之前）。
	iNote := strings.Index(content, "根因：pre-bundle 换了 hash")
	iOther := strings.Index(content, "## 另一个任务")
	if iNote < 0 || iOther < 0 || iNote > iOther {
		t.Fatalf("笔记写错小节:\n%s", content)
	}
	// 重点块还在，两轮的活动行也还在。
	head, _ := splitDailySections(content)
	if !strings.Contains(head, "工作重点") || !strings.Contains(head, "修笔记编辑器") {
		t.Errorf("重点块被破坏了:\n%s", head)
	}
}

// 只有笔记、没有活动行（活动行那次写失败）时，也要补出一个完整文件。
func TestAppendDailyNoteWithoutActivity(t *testing.T) {
	cwd := t.TempDir()
	appendDailyNote("conv-x", cwd, "只有笔记", "问了一句", time.Now(), []string{"结论：直接可用"})
	content := readFile(t, dailyFileOf(t, cwd))
	if !strings.Contains(content, "工作重点") {
		t.Errorf("应补出重点块标题:\n%s", content)
	}
	if !strings.Contains(content, "## 只有笔记 · conv-x") {
		t.Errorf("应新建任务小节:\n%s", content)
	}
	if !strings.Contains(content, "直接可用") {
		t.Errorf("笔记内容丢了:\n%s", content)
	}
}

// 工具活动摘要：跳过被去重/被拒绝的调用，带失败原因，超量折叠。
func TestSummarizeToolActivity(t *testing.T) {
	records := []aiToolRecord{
		{Name: "file_edit", Path: "a.go"},
		{Name: "file_edit", Path: "a.go"}, // 重复的同一行 → 去重
		{Name: "run_command", Args: `{"command":"go test ./..."}`},
		{Name: "file_read", Path: "b.go", Dedup: true},
		{Name: "spawn_agent", Path: "c.go", Denied: true},
		{Name: "file_write", Path: "d.go", Error: "permission denied"},
	}
	got := summarizeToolActivity(records, 10)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "b.go") || strings.Contains(joined, "c.go") {
		t.Errorf("被去重/被拒绝的调用不该进摘要:\n%s", joined)
	}
	if !strings.Contains(joined, "go test ./...") {
		t.Errorf("命令没被抠出来:\n%s", joined)
	}
	if !strings.Contains(joined, "失败：permission denied") {
		t.Errorf("失败原因应带上:\n%s", joined)
	}
	if n := len(got); n != 3 {
		t.Errorf("应压成 3 行，实际 %d: %s", n, joined)
	}
	// 超量折叠。
	got = summarizeToolActivity(records, 2)
	if !strings.Contains(got[len(got)-1], "…还有") {
		t.Errorf("超出上限应折叠: %v", got)
	}
	if len(got) != 3 {
		t.Errorf("折叠后应是 2 条 + 1 条省略，实际 %v", got)
	}
}

// 记忆整理的用量并进它所属那条回复：输入框下方的胶囊是按消息行求和的。
func TestRecordMemoryUsageAddsToMessage(t *testing.T) {
	svc, d := newTestAIService(t)
	conv, err := d.CreateAIConversation(db.NewAIConversation{Mode: "chat", Title: "记用量"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := d.SaveAssistantTurn(conv.ID, db.AssistantTurn{
		Content: "x", Usage: db.TokenUsage{Prompt: 100, Completion: 10, Cached: 20},
	})
	if err != nil || msg == nil {
		t.Fatalf("建消息失败: %v", err)
	}
	svc.recordMemoryUsage(taskMemoryInput{ConvID: conv.ID, MessageID: msg.ID},
		db.TokenUsage{Prompt: 300, Completion: 40})

	msgs, err := d.ListAIMessages(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got *db.AIMessage
	for i := range msgs {
		if msgs[i].ID == msg.ID {
			got = &msgs[i]
		}
	}
	if got == nil {
		t.Fatal("找不到那条消息")
	}
	if got.PromptTokens != 400 || got.CompletionTokens != 50 || got.CachedTokens != 20 {
		t.Errorf("用量没并进消息: prompt=%d completion=%d cached=%d",
			got.PromptTokens, got.CompletionTokens, got.CachedTokens)
	}
	// 空 messageID / 空用量时安静跳过，不能把 0 加坏也不能报错。
	svc.recordMemoryUsage(taskMemoryInput{ConvID: conv.ID}, db.TokenUsage{Prompt: 9})
	svc.recordMemoryUsage(taskMemoryInput{ConvID: conv.ID, MessageID: msg.ID}, db.TokenUsage{})
}

// 记忆文件本身要可读：落盘内容里不能出现 JSON 转义、全路径。
func TestAppendDailyNoteSanitizes(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-s", cwd, "清洗", "清洗一下", nil, 1, 1)
	appendDailyNote("conv-s", cwd, "清洗", "清洗一下", time.Now(), []string{"- 需求：去掉多余的项目符号"})
	content := readFile(t, dailyFileOf(t, cwd))
	if strings.Contains(content, "- - 需求") {
		t.Errorf("重复的项目符号没清掉:\n%s", content)
	}
	if !strings.Contains(content, "- 需求：去掉多余的项目符号") {
		t.Errorf("笔记行格式不对:\n%s", content)
	}
}

// 每日文件超上限时整节丢最旧的，顶部重点块永远保留。
func TestDailyTrimKeepsFocus(t *testing.T) {
	cwd := t.TempDir()
	for i := 0; i < 40; i++ {
		appendDailyMemory(itoa(i), cwd, "任务"+itoa(i), strings.Repeat("很长的诉求", 30), nil, 1, 1)
	}
	content := readFile(t, dailyFileOf(t, cwd))
	if len([]rune(content)) > dailyMemoryFileCap {
		t.Errorf("文件超上限: %d > %d", len([]rune(content)), dailyMemoryFileCap)
	}
	head, _ := splitDailySections(content)
	if !strings.Contains(head, "工作重点") {
		t.Errorf("重点块被裁掉:\n%s", head)
	}
	// 最新那条任务必须还在（丢的是最旧的）。
	if !strings.Contains(content, "任务39") {
		t.Errorf("最新的任务被丢了:\n%s", content[len(content)-200:])
	}
}

// 读取时按字符上限裁剪，但重点块优先。
func TestReadDailyMemoryStillCapped(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-cap", cwd, "扫描项目", "扫描一下项目", nil, 12, 35)
	got := readDailyMemory(dailyFileOf(t, cwd), 200)
	if len([]rune(got)) > 200 {
		t.Errorf("超上限: %d", len([]rune(got)))
	}
}
