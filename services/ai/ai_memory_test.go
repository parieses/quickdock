package ai

// 每日记忆的「工作重点」块：一天的文件顶部记的是「今天在做什么、各花了多少」，
// 而不是一串「用户：继续 / 工具：12 轮」。
//
// 这几条不变量改之前先确认：
//   - 同一个任务的续跑只累加轮次，不新增条目（否则一天能堆出十几条「继续」）；
//   - 条目名用任务标题，不用那句「继续」（续跑时用户诉求不是重点）；
//   - 注入时重点块必须保住，活动记录才是可以按尾巴截的那部分。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dailyFileOf(t *testing.T, cwd string) string {
	t.Helper()
	sc := resolveMemoryScope(cwd)
	if sc.root == "" {
		t.Fatal("记忆作用域为空")
	}
	return dailyMemoryPath(sc.root, time.Now())
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读文件失败 %s: %v", p, err)
	}
	return string(data)
}

// 同一个任务续跑两次：重点块只有一条，轮次累加。
func TestAppendDailyMemoryMergesSameConv(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-abc", cwd, "扫描这个项目", "扫描这个项目", nil, 12, 35)
	appendDailyMemory("conv-abc", cwd, "扫描这个项目", "继续", nil, 11, 28)

	content := readFile(t, dailyFileOf(t, cwd))
	head, body := splitDailySections(content)
	items := parseDailyFocus(head)
	if len(items) != 1 {
		t.Fatalf("重点块应有 1 条（续跑合并），实际 %d 条:\n%s", len(items), content)
	}
	if items[0].rounds != 23 || items[0].tools != 63 {
		t.Errorf("轮次未累加: rounds=%d tools=%d", items[0].rounds, items[0].tools)
	}
	if items[0].label != "扫描这个项目" {
		t.Errorf("条目名应保留任务标题，实际 %q", items[0].label)
	}
	// 活动记录仍要逐轮留痕：重点块是索引，不是替代。
	if n := strings.Count(body, "\n## "); n != 1 { // 第一行没有前导换行
		if n+1 != 2 {
			t.Errorf("活动记录应有 2 段，实际 %d", n+1)
		}
	}
	if !strings.Contains(body, "- 用户：继续") {
		t.Errorf("活动记录应保留每轮原始诉求:\n%s", body)
	}
}

// 不同任务各占一条，按首次出现排序。
func TestAppendDailyMemorySeparateConvs(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-1", cwd, "修复输入框", "数字打不进去", nil, 2, 5)
	appendDailyMemory("conv-2", cwd, "扫描这个项目", "扫描这个项目", nil, 12, 35)

	head, _ := splitDailySections(readFile(t, dailyFileOf(t, cwd)))
	items := parseDailyFocus(head)
	if len(items) != 2 {
		t.Fatalf("应各占一条，实际 %d 条", len(items))
	}
	if items[0].label != "修复输入框" || items[1].label != "扫描这个项目" {
		t.Errorf("顺序应按首次出现: %+v", items)
	}
}

// 标题里带分隔符时，解析不能把标题切碎（从右往左切）。
func TestParseFocusLineKeepsSeparatorInLabel(t *testing.T) {
	in := dailyFocus{key: "3f2a1b", label: "性能" + focusLineSep + "首页", rounds: 3, tools: 9, at: "09:55"}
	got, ok := parseFocusLine(in.line())
	if !ok {
		t.Fatalf("解析失败: %q", in.line())
	}
	if got.label != in.label || got.rounds != 3 || got.tools != 9 || got.key != "3f2a1b" || got.at != "09:55" {
		t.Errorf("往返不一致: %+v", got)
	}
}

// 重点块条数有上限；丢的是最早出现的那条，正在做的留在列表里。
func TestUpsertDailyFocusCap(t *testing.T) {
	var items []dailyFocus
	for i := 0; i < dailyFocusMaxLines+3; i++ {
		items = upsertDailyFocus(items, dailyFocus{
			key: fmt.Sprintf("k%02d", i), label: fmt.Sprintf("任务%d", i), rounds: 1, tools: 1, at: "10:00",
		})
	}
	if len(items) != dailyFocusMaxLines {
		t.Fatalf("应裁到 %d 条，实际 %d", dailyFocusMaxLines, len(items))
	}
	if items[len(items)-1].key != fmt.Sprintf("k%02d", dailyFocusMaxLines+2) {
		t.Errorf("最新的那条被丢了: %+v", items[len(items)-1])
	}
	// 已经裁掉的 key 再出现时按新条目追加，不报错。
	items = upsertDailyFocus(items, dailyFocus{key: "k00", label: "任务0", rounds: 1, tools: 1, at: "11:00"})
	if items[len(items)-1].key != "k00" {
		t.Errorf("重新出现的任务应追加到末尾: %+v", items[len(items)-1])
	}
}

// 注入时重点块必须保留：活动记录再长也不能把它挤掉。
func TestReadDailyMemoryKeepsFocus(t *testing.T) {
	cwd := t.TempDir()
	appendDailyMemory("conv-abc", cwd, "扫描这个项目", "扫描这个项目", nil, 12, 35)
	p := dailyFileOf(t, cwd)

	// 往里堆一堆活动记录，把文件撑到远超注入上限。
	var b strings.Builder
	b.WriteString(readFile(t, p))
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "\n## 2026-11-02 09:%02d · 3f2a1b\n- 用户：继续\n- 工具：12 轮 / 35 次调用\n", i%60)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readDailyMemory(p, memoryCapDaily)
	if got == "" {
		t.Fatal("读到空内容")
	}
	if !strings.Contains(got, "工作重点") || !strings.Contains(got, "扫描这个项目") {
		t.Errorf("重点块应始终保留:\n%s", got)
	}
	if n := len([]rune(got)); n > memoryCapDaily {
		t.Errorf("注入超长: %d > %d", n, memoryCapDaily)
	}
}

// 旧格式（没有 # 标题）也要能读：不能因为格式升级就把历史日志读丢。
func TestReadDailyMemoryLegacyFormat(t *testing.T) {
	cwd := t.TempDir()
	p := dailyFileOf(t, cwd)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "\n## 2026-11-01 21:10\n- 用户：搭个单页站点\n- 工具：3 轮 / 7 次调用\n"
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readDailyMemory(p, memoryCapDaily)
	if !strings.Contains(got, "搭个单页站点") {
		t.Errorf("旧格式内容读丢了:\n%s", got)
	}

	// 追加新记录后应自动补上重点块，旧内容仍在。
	appendDailyMemory("conv-xyz", cwd, "扫描这个项目", "扫描这个项目", nil, 1, 2)
	head, body := splitDailySections(readFile(t, p))
	if !strings.Contains(head, "工作重点") {
		t.Errorf("应补上重点块:\n%s", head)
	}
	if !strings.Contains(body, "搭个单页站点") {
		t.Errorf("旧活动记录应保留:\n%s", body)
	}
}

// 重点块占满上限时也不能把文件撑爆：活动记录按最旧的小节丢。
func TestDailyMemoryFileCap(t *testing.T) {
	cwd := t.TempDir()
	p := dailyFileOf(t, cwd)
	for i := 0; i < 60; i++ {
		appendDailyMemory(fmt.Sprintf("conv-%02d", i), cwd, fmt.Sprintf("任务%d", i),
			strings.Repeat("很长的诉求", 40), nil, 1, 1)
	}
	content := readFile(t, p)
	if n := len([]rune(content)); n > dailyMemoryFileCap {
		t.Errorf("文件超上限: %d > %d", n, dailyMemoryFileCap)
	}
	head, _ := splitDailySections(content)
	if !strings.Contains(head, "工作重点") {
		t.Errorf("裁剪不应伤到重点块:\n%s", head)
	}
}
