package ai

// 每日记忆的格式守卫。
//
// 用户贴出的真实文件长这样（改前）：
//
//	## 2026-11-18 09:53
//	- 用户：完整得扫描下这个项目
//	- 改动文件：D:\project\quickdock-v3\.tmp_scan\stats.ps1、…（七个全路径）
//	## 2026-11-18 10:02
//	- 用户：继续
//	…
//
// 一次长任务 → 八个小节、全是全路径与「继续」。原话是「这记忆根本不能用」。
// 下面每一条都对着那个形态钉。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func dailyTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), memoryDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("建记忆目录失败: %v", err)
	}
	return root
}

// 纯问答轮次：**要留下记忆文件**，但不写「0 轮 / 0 次调用」这种空统计。
//
// 这条测试的契约变过一次，原因值得记下：早先为了治好「记忆很垃圾」，
// 纯问答直接 return——结果新用户跑几天都看不到任何记忆文件，整条链路
// 看起来「没生效」（用户实测反馈）。现在的口径是「记，但只记有意义的部分」。
func TestAppendDailyMemoryChatOnlyWritesWithoutStats(t *testing.T) {
	root := dailyTestRoot(t)
	appendDailyMemory("conv-chat", filepath.Dir(root), "随便问问", "这个按钮在哪", nil, 0, 0)
	dir := filepath.Join(root, "memory")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("纯问答也应留下当日记忆文件（否则看起来功能没生效），实际 %d 个", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("读记忆文件失败: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "这个按钮在哪") {
		t.Error("应记下用户诉求")
	}
	if strings.Contains(s, "0 轮 / 0 次调用") {
		t.Errorf("不该写空统计（这正是「垃圾记忆」的来源）:\n%s", s)
	}
}

// 同一个任务的多轮（含「继续」）合并进一个小节，不按时间开新节。
func TestAppendDailyMemoryMergesIntoOneSection(t *testing.T) {
	root := dailyTestRoot(t)
	cwd := filepath.Dir(root)

	appendDailyMemory("conv-merge", cwd, "扫描这个项目", "完整扫描下这个项目", []string{filepath.Join(cwd, "src", "a.go")}, 57, 127)
	appendDailyMemory("conv-merge", cwd, "扫描这个项目", "继续", nil, 48, 98)
	appendDailyMemory("conv-merge", cwd, "扫描这个项目", "再完整得扫描下", []string{filepath.Join(cwd, "src", "b.go")}, 64, 94)

	path := dailyMemoryPath(root, time.Now())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读每日记忆失败: %v", err)
	}
	content := string(data)

	if n := strings.Count(content, "\n## "); n != 1 {
		t.Fatalf("同一任务应只有一个小节，实际 %d 个:\n%s", n, content)
	}
	// 「继续」不该作为用户诉求出现（它不是任务内容）。
	if strings.Contains(content, "用户：继续") {
		t.Errorf("续跑不该记成任务内容:\n%s", content)
	}
	// 三轮的活动行都要在，且带时间。
	for _, want := range []string{"57 轮 / 127 次调用", "48 轮 / 98 次调用", "64 轮 / 94 次调用"} {
		if !strings.Contains(content, want) {
			t.Errorf("缺少活动行 %q:\n%s", want, content)
		}
	}
}

// 改动文件必须是相对路径、丢掉辅助目录、超量折叠。
func TestFormatDailyFiles(t *testing.T) {
	root := dailyTestRoot(t)
	cwd := filepath.Dir(root)

	got := formatDailyFiles(cwd, []string{
		filepath.Join(cwd, "src", "main.go"),
		filepath.Join(cwd, memoryDirName, "tmp", "scan.js"), // 辅助文件，不该记
		filepath.Join(cwd, "src", "main.go"),                // 重复
		filepath.Join(cwd, "docs", "a.md"),
	})
	want := []string{"src/main.go", "docs/a.md"}
	if len(got) != len(want) {
		t.Fatalf("文件列表不对: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}

	// 超过上限fold成「…等 N 个」：记忆不是提交清单。
	many := make([]string, 0, 10)
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		many = append(many, filepath.Join(cwd, n+".go"))
	}
	got = formatDailyFiles(cwd, many)
	if len(got) != dailyFilesMax+1 {
		t.Fatalf("超量应折叠成 %d+1 项，实际 %d: %v", dailyFilesMax, len(got), got)
	}
	if !strings.HasPrefix(got[len(got)-1], "…等 ") {
		t.Errorf("最后一项应是折叠提示: %q", got[len(got)-1])
	}
}

// 项目作用域下辅助目录里的东西不算项目改动。
func TestIsScratchPath(t *testing.T) {
	for _, p := range []string{memoryDirName + "/tmp/a.js", memoryDirName} {
		if !isScratchPath(p) {
			t.Errorf("%q 应识别为辅助文件", p)
		}
	}
	for _, p := range []string{"quickdock/a.js", "src/a.go", "memory/a.md"} {
		if isScratchPath(p) {
			t.Errorf("%q 不该被当成辅助文件", p)
		}
	}
}

// 活动行插入到指定小节末尾，且任务名互相包含时不会写错节。
func TestAppendDailyActivity(t *testing.T) {
	body := "\n## 扫描项目\n- 09:00 · 1 轮 / 2 次调用\n\n## 扫描项目文档\n- 10:00 · 3 轮 / 4 次调用\n"
	out, ok := appendDailyActivity(body, "扫描项目", "- 11:00 · 5 轮 / 6 次调用")
	if !ok {
		t.Fatal("应找到「扫描项目」小节")
	}
	// 新行必须落在第一个小节里，而不是第二个（子串匹配就会错）。
	iNew := strings.Index(out, "11:00")
	iSecond := strings.Index(out, "## 扫描项目文档")
	if iNew < 0 || iNew > iSecond {
		t.Fatalf("活动行插错小节:\n%s", out)
	}
	// 不存在的标题返回 false，由调用方新建小节。
	if _, ok := appendDailyActivity(body, "不存在的任务", "- x"); ok {
		t.Error("不存在的标题不该命中")
	}
}

// MEMORY.md：同一天合并进一个 `## 日期` 小节。
func TestAppendBulletsToDaySection(t *testing.T) {
	day := "2026-11-18"
	cur := "# 长期记忆\n\n## " + day + "\n- 项目用 Vue 3\n"
	out, ok := appendBulletsToDaySection(cur, day, []string{"数据库是 SQLite", "项目用 Vue 3"})
	if !ok {
		t.Fatal("应命中当天小节")
	}
	if strings.Count(out, "## "+day) != 1 {
		t.Errorf("不该重复创建当天小节:\n%s", out)
	}
	if !strings.Contains(out, "数据库是 SQLite") {
		t.Errorf("新条目没写进去:\n%s", out)
	}
	// 已存在的条目不重复写。
	if strings.Count(out, "项目用 Vue 3") != 1 {
		t.Errorf("重复条目被写了两次:\n%s", out)
	}
	// 没有当天小节时交给调用方新建。
	if _, ok := appendBulletsToDaySection("# 长期记忆\n", "2026-01-01", []string{"x"}); ok {
		t.Error("没有当天小节时不该命中")
	}
}
