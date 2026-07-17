package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

const schedTimeLayout = "2006-01-02 15:04:05"

func nowStr() string {
	return time.Now().Format(schedTimeLayout)
}

func parseSchedTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// 兼容 datetime-local 的 'T' 分隔与缺省秒
	s = strings.Replace(s, "T", " ", 1)
	for _, layout := range []string{schedTimeLayout, "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseTimeOfDay 解析 HH:MM 或 HH:MM:SS，返回 时/分/秒
func parseTimeOfDay(s string) (int, int, int, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	sec := 0
	if len(parts) >= 3 {
		sec, _ = strconv.Atoi(parts[2])
	}
	if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, 0, false
	}
	return h, m, sec, true
}

// computeNextRun 根据调度规则计算严格晚于 from 的下一次运行时间；无有效时间返回 ""
func computeNextRun(t *db.ScheduledTask, fromStr string) string {
	from, ok := parseSchedTime(fromStr)
	if !ok {
		from = time.Now()
	}
	switch t.ScheduleKind {
	case "once":
		at, ok := parseSchedTime(t.RunAt)
		if !ok || !at.After(from) {
			return ""
		}
		return at.Format(schedTimeLayout)

	case "interval":
		if t.IntervalSec < 5 {
			return ""
		}
		return from.Add(time.Duration(t.IntervalSec) * time.Second).Format(schedTimeLayout)

	case "daily":
		h, m, s, ok := parseTimeOfDay(t.TimeOfDay)
		if !ok {
			return ""
		}
		cand := time.Date(from.Year(), from.Month(), from.Day(), h, m, s, 0, time.Local)
		if !cand.After(from) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand.Format(schedTimeLayout)

	case "weekly":
		h, m, s, ok := parseTimeOfDay(t.TimeOfDay)
		if !ok {
			return ""
		}
		days := parseWeekdays(t.Weekdays)
		if len(days) == 0 {
			return ""
		}
		// 从今天起最多向后找 7 天
		for i := 0; i < 8; i++ {
			day := from.AddDate(0, 0, i)
			wd := int(day.Weekday()) // 0=Sun..6=Sat
			if !days[wd] {
				continue
			}
			cand := time.Date(day.Year(), day.Month(), day.Day(), h, m, s, 0, time.Local)
		if cand.After(from) {
			return cand.Format(schedTimeLayout)
		}
	}
	return ""

	case "monthly":
		h, m, s, ok := parseTimeOfDay(t.TimeOfDay)
		if !ok {
			return ""
		}
		// 从本月或下月找第一个严格晚于 from 的同日（处理月末越界，如 1/31 → 2/28）
		for i := 0; i < 2; i++ {
			y, mo, d := from.Year(), from.Month(), from.Day()
			if i == 1 {
				mo++
				if mo > 12 {
					mo = 1
					y++
				}
			}
			lastDay := daysInMonth(y, mo)
			dom := d
			if dom > lastDay {
				dom = lastDay
			}
			cand := time.Date(y, mo, dom, h, m, s, 0, time.Local)
			if cand.After(from) {
				return cand.Format(schedTimeLayout)
			}
		}
		return ""
	}
	return ""
}

// daysInMonth 返回给定年月的天数（处理闰年与月末越界）
func daysInMonth(year int, month time.Month) int {
	switch month {
	case time.January, time.March, time.May, time.July, time.August, time.October, time.December:
		return 31
	case time.April, time.June, time.September, time.November:
		return 30
	case time.February:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	}
	return 30
}

func parseWeekdays(csv string) map[int]bool {
	out := map[int]bool{}
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 6 {
			out[n] = true
		}
	}
	return out
}

// StartScheduleRunner 启动定时任务调度器。
// 采用「精确定时器」而非固定 10s 轮询：每次执行后计算最近的 next_run，
// 用 time.Timer 精确 sleep 到该时刻（秒级精度，不再有最多 10s 的延迟触发），
// 任务被增删改/启停时用 schedWake 立即唤醒重排。这正是调度库内部的核心机制，
// 但无需引入额外依赖，且与进程内状态天然一致。
func (a *AppService) StartScheduleRunner() {
	a.schedWake = make(chan struct{}, 1)
	go a.scheduleLoop()
}

// wakeScheduler 非阻塞唤醒调度循环，使其立即重新计算并扫描到期任务
func (a *AppService) wakeScheduler() {
	if a.schedWake == nil {
		return
	}
	select {
	case a.schedWake <- struct{}{}:
	default:
	}
}

func (a *AppService) scheduleLoop() {
	time.Sleep(3 * time.Second)
	for {
		a.checkScheduledTasks()
		dur := a.nextScheduleWait()
		timer := time.NewTimer(dur)
		select {
		case <-timer.C:
		case <-a.schedWake:
			timer.Stop()
		}
	}
}

// nextScheduleWait 计算距离「最近的已排期任务」还需等待的时长；
// 无任务或全为空时返回保守兜底间隔（1 分钟），靠 schedWake 在变更时即时唤醒。
func (a *AppService) nextScheduleWait() time.Duration {
	if a.DB == nil {
		return time.Minute
	}
	tasks, err := a.DB.ListEnabledWithNextRun()
	if err != nil || len(tasks) == 0 {
		return time.Minute
	}
	minDur := 24 * time.Hour
	for i := range tasks {
		t, ok := parseSchedTime(tasks[i].NextRun)
		if !ok {
			continue
		}
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		if d < minDur {
			minDur = d
		}
	}
	if minDur < time.Second {
		minDur = time.Second
	}
	return minDur
}

// checkScheduledTasks 执行所有到期任务并重新排期
func (a *AppService) checkScheduledTasks() {
	if a.DB == nil {
		return
	}
	now := nowStr()
	tasks, err := a.DB.ListDueTasks(now)
	if err != nil {
		fmt.Println("QuickDock: 定时任务查询失败:", err)
		return
	}
	for i := range tasks {
		t := &tasks[i]
		status, result := a.executeTask(t)

		// 计算下次运行时间与启用状态
		nextRun := computeNextRun(t, now)
		enabled := t.Enabled
		if t.ScheduleKind == "once" || nextRun == "" {
			// 一次性任务或无法再排期 → 自动停用
			nextRun = ""
			enabled = false
		}
		_ = a.DB.SetTaskRunResult(t.ID, now, status, result, nextRun, enabled)

		// 可选：执行后发系统通知 + 机器人 Webhook（钉钉/企业微信/飞书，全局共用配置）
		if t.Notify {
			icon := "✅"
			if status != "ok" {
				icon = "⚠️"
			}
			title := icon + " 定时任务：" + t.Name
			if a.Notifier != nil {
				_ = a.Notifier.SendNotification(notifications.NotificationOptions{
					ID:    "schedtask-" + t.ID + "-" + time.Now().Format("150405"),
					Title: title,
					Body:  result,
				})
			}
			a.sendWebhookNotify(title, result)
		}
	}
}

// executeTask 执行单个任务，返回 (status, result)
// status: "ok" | "fail"
func (a *AppService) executeTask(t *db.ScheduledTask) (string, string) {
	switch t.Action {
	case "app", "dir", "url":
		if err := platform.ShellOpen(t.Target, t.WorkingDir); err != nil {
			return "fail", "打开失败：" + err.Error()
		}
		return "ok", "已打开：" + t.Target

	case "command":
		if err := platform.RunCommand(t.Target, t.WorkingDir); err != nil {
			return "fail", "命令执行失败：" + err.Error()
		}
		return "ok", "命令已执行：" + t.Target

	case "http":
		return a.executeHTTP(t)

	case "todo":
		return a.executeRecurringTodo(t)
	}
	return "fail", "未知的动作类型：" + t.Action
}

// executeRecurringTodo 由重复待办触发生成一条具体待办
func (a *AppService) executeRecurringTodo(t *db.ScheduledTask) (string, string) {
	var p struct {
		TodoID  string `json:"todoId"`
		Title   string `json:"title"`
		Priority string `json:"priority"`
		DueDate string `json:"dueDate"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal([]byte(t.Target), &p); err != nil {
		return "fail", "解析待办负载失败：" + err.Error()
	}
	if p.Title == "" {
		return "fail", "待办标题为空"
	}
	// 生成的实例携带原待办的标签（不再带 recurrence，避免无限递归）
	tags := ""
	if orig, gErr := a.DB.GetTodo(p.TodoID); gErr == nil {
		tags = orig.Tags
	}
	if _, err := a.DB.CreateTodo(p.Title, p.Priority, p.DueDate, p.Note, "", "", "", "", tags); err != nil {
		return "fail", "创建待办失败：" + err.Error()
	}
	return "ok", "已生成待办：" + p.Title
}

// syncTodoSchedule 根据待办的 recurrence 配置同步对应的调度记录。
// 调度记录 ID 固定为 "recur-<todoId>"，便于幂等 upsert 与删除。
func (a *AppService) syncTodoSchedule(m *db.Todo) {
	if a.DB == nil {
		return
	}
	schedID := "recur-" + m.ID
	var rc struct {
		Kind      string `json:"kind"`
		TimeOfDay string `json:"timeOfDay"`
		Weekdays  string `json:"weekdays"`
	}
	if m.Recurrence != "" {
		_ = json.Unmarshal([]byte(m.Recurrence), &rc)
	}
	if rc.Kind == "" || rc.Kind == "none" {
		_ = a.DB.DeleteScheduledTask(schedID)
		return
	}
	if rc.TimeOfDay == "" {
		rc.TimeOfDay = "09:00"
	}
	payload, _ := json.Marshal(map[string]string{
		"todoId":  m.ID,
		"title":   m.Title,
		"priority": m.Priority,
		"dueDate": m.DueDate,
		"note":    m.Note,
	})
	nextRun := computeNextRun(&db.ScheduledTask{
		ScheduleKind: rc.Kind,
		TimeOfDay:    rc.TimeOfDay,
		Weekdays:     rc.Weekdays,
		RunAt:        nowStr(),
	}, nowStr())

	st := &db.ScheduledTask{
		ID:           schedID,
		Name:         m.Title,
		Action:       "todo",
		Target:       string(payload),
		ScheduleKind: rc.Kind,
		TimeOfDay:    rc.TimeOfDay,
		Weekdays:     rc.Weekdays,
		Enabled:      true,
		NextRun:      nextRun,
	}
	if existing, err := a.DB.GetScheduledTask(schedID); err == nil {
		st.Sort = existing.Sort
		st.CreatedAt = existing.CreatedAt
		_ = a.DB.UpdateScheduledTask(st)
	} else {
		_, _ = a.DB.CreateScheduledTask(st)
	}
}

// executeHTTP 发起 HTTP 请求（curl 能力），返回状态码与简要正文
func (a *AppService) executeHTTP(t *db.ScheduledTask) (string, string) {
	method := strings.ToUpper(strings.TrimSpace(t.HTTPMethod))
	if method == "" {
		method = "GET"
	}
	var bodyReader io.Reader
	if t.HTTPBody != "" {
		bodyReader = strings.NewReader(t.HTTPBody)
	}
	req, err := http.NewRequest(method, t.Target, bodyReader)
	if err != nil {
		return "fail", "请求构建失败：" + err.Error()
	}
	// 解析 headers：每行 "Key: Value"
	for _, line := range strings.Split(t.HTTPHeaders, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			req.Header.Set(key, val)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "fail", "请求失败：" + err.Error()
	}
	defer resp.Body.Close()
	// 只读取前 200 字节用于结果预览
	preview, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	previewStr := strings.TrimSpace(string(preview))
	summary := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if previewStr != "" {
		summary += " · " + previewStr
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return "ok", summary
	}
	return "fail", summary
}
