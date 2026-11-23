package script

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"quickdock/internal/db"
	"quickdock/internal/scriptrun"
	"quickdock/services"
)

// LogHit 一条历史日志命中。
type LogHit struct {
	RunID string `json:"runId"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Time  string `json:"time"`
}

// 搜索预算。日志文件按 runID 命名，一次运行一个文件，所以「最近的 N 个文件」
// 就是「最近的 N 次运行」，正常脚本翻一两百个足够定位问题。
const (
	logSearchFiles  = 80
	logSearchBudget = 32 << 20 // 总读取预算 32MB
	logSearchHits   = 200
	logSearchLine   = 64 << 10 // 单行上限（超长行截断，别把内存吃穿）
)

// SearchScriptLogs 在某个脚本的历史日志里跨 run 搜关键字。
//
// 为什么不建 FTS 表：日志是「一次性消费品」，搜的场景是「昨天那次失败到底报
// 什么错」，也就是最近几十次运行。扫 80 个文件、32MB 预算在这个量级上是几十
// 毫秒的事，而 FTS 要为每次运行增量写索引、还要处理重建与裁剪（留下的索引
// 指向已删除的日志），复杂度与收益完全不匹配。
func (s *ScriptService) SearchScriptLogs(id, keyword string, limit int) *services.ApiResult {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return services.FailMsg("请输入要搜索的关键字")
	}
	if limit <= 0 || limit > logSearchHits {
		limit = logSearchHits
	}
	root := scriptrun.LogRoot()
	if root == "" {
		return services.Ok(map[string]any{"hits": []LogHit{}, "scanned": 0})
	}
	dir := filepath.Join(root, id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return services.Ok(map[string]any{"hits": []LogHit{}, "scanned": 0})
		}
		return services.Fail(err)
	}

	// runID 带时间前缀（20060102-150405-xxxxxx），按文件名倒序即时间倒序。
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > logSearchFiles {
		names = names[:logSearchFiles]
	}

	needle := strings.ToLower(keyword)
	hits := make([]LogHit, 0, 32)
	budget := int64(logSearchBudget)
	scanned := 0
	for _, name := range names {
		if budget <= 0 || len(hits) >= limit {
			break
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		scanned++
		when := info.ModTime().Format("2006-01-02 15:04:05")
		runID := strings.TrimSuffix(name, ".log")

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 32<<10), logSearchLine)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			budget -= int64(len(line)) + 1
			if budget <= 0 {
				break
			}
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, LogHit{RunID: runID, Line: lineNo, Text: line, Time: when})
				if len(hits) >= limit {
					break
				}
			}
		}
		f.Close()
	}
	return services.Ok(map[string]any{
		"hits":      hits,
		"scanned":   scanned,
		"truncated": len(hits) >= limit,
	})
}

// ScriptBinding 该脚本被哪些定时任务引用。
type ScriptBinding struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ScheduleKind string `json:"scheduleKind"`
	Expr         string `json:"expr"`
	Enabled      bool   `json:"enabled"`
	NextRun      string `json:"nextRun"`
	LastRun      string `json:"lastRun"`
	LastStatus   string `json:"lastStatus"`
}

// GetScriptBindings 列出引用本脚本的定时任务。
//
// 脚本页需要这个：一个脚本「为什么每天九点自己跑了」的唯一解释就在定时任务
// 那边，而用户改完脚本之后最常见的疑问是「现在到底还有没有在跑」。在脚本
// 页直接给出来，比让他去另一个页面翻列表快得多。
func (s *ScriptService) GetScriptBindings(id string) *services.ApiResult {
	tasks, err := s.App.DB.ListScheduledTasks()
	if err != nil {
		return services.Fail(err)
	}
	out := make([]ScriptBinding, 0, 4)
	for _, t := range tasks {
		if t.Action != "script" || t.Target != id {
			continue
		}
		out = append(out, ScriptBinding{
			ID:           t.ID,
			Name:         t.Name,
			ScheduleKind: t.ScheduleKind,
			Expr:         describeSchedule(t),
			Enabled:      t.Enabled,
			NextRun:      t.NextRun,
			LastRun:      t.LastRun,
			LastStatus:   t.LastStatus,
		})
	}
	return services.Ok(out)
}

// describeSchedule 把调度配置转成一句人话（前端直接显示，不必再写一套格式化）。
func describeSchedule(t db.ScheduledTask) string {
	switch t.ScheduleKind {
	case "cron":
		if t.CronExpr != "" {
			return t.CronExpr
		}
		return "cron"
	case "interval":
		sec := t.IntervalSec
		if sec >= 3600 && sec%3600 == 0 {
			return "每 " + strconv.Itoa(sec/3600) + " 小时"
		}
		if sec >= 60 && sec%60 == 0 {
			return "每 " + strconv.Itoa(sec/60) + " 分钟"
		}
		return "每 " + strconv.Itoa(sec) + " 秒"
	case "daily":
		return "每天 " + t.TimeOfDay
	case "weekly":
		return "每周 " + t.Weekdays + " " + t.TimeOfDay
	case "monthly":
		return "每月 " + strconv.Itoa(t.DayOfMonth) + " 号 " + t.TimeOfDay
	case "once":
		return "一次 " + t.RunAt
	}
	return t.ScheduleKind
}
