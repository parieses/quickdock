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
	"quickdock/internal/logger"
	"quickdock/internal/platform"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// scheduleHTTPClient 共享 HTTP 客户端（定时任务 HTTP 请求，连接复用）
var scheduleHTTPClient = &http.Client{Timeout: 30 * time.Second}

const schedTimeLayout = "2006-01-02 15:04:05"

// schedCommandTimeout 命令类任务的等待上限，与 HTTP 动作的 30s 对齐：
// 到点仍未退出按「仍在运行」收尾，不杀进程（见 platform.RunCommandCapture）。
const schedCommandTimeout = 30 * time.Second

// schedResultMaxRunes 执行结果串的最大长度（按 rune，中文不吃亏）。
// 结果会进三处消费面：任务卡与运行历史（单行省略显示）、机器人通知
// （企微 text 消息上限 2048 字节）。必须自己收紧——DB 层虽还有 500 字节
// 兜底，但等到那一步已是按字节硬切，没必要把摘要先撑到那儿。
const schedResultMaxRunes = 300

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
	a.schedWG.Add(1)
	// 常驻循环：panic 落盘后不重抛，避免一次异常让整个调度器静默停摆
	go logger.CapturePanicInGo("schedule:loop", func() {
		defer a.schedWG.Done()
		a.scheduleLoop()
	})()
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

// RescheduleAfterRestore 定时任务从回收站恢复后重排下次运行时间。
//
// 墓碑存的是「删除那一刻」的整行快照，next_run 通常是早已过去的时间点。
// 原样写回会被下一轮 checkScheduledTasks 判成到期而在恢复瞬间补跑一次——
// 用户只是把任务找回来，不代表要它现在执行（更别说可能是一次性之外的向下单/重启类动作）。
// 因此这里统一按当前时刻重算下一次；已过期的才会被重排，未到期的不动。
func (a *AppService) RescheduleAfterRestore(id string) {
	id = strings.TrimSpace(id)
	if a.DB == nil || id == "" {
		return
	}
	t, err := a.DB.GetScheduledTask(id)
	if err != nil || t == nil {
		return
	}
	now := nowStr()
	if t.NextRun != "" && t.NextRun > now {
		a.wakeScheduler() // 还没到点，快照仍然有效，只需让调度器知道它回来了
		return
	}
	next := computeNextRun(t, now)
	if err := a.DB.SetTaskEnabled(t.ID, t.Enabled, next); err != nil {
		logger.W("QuickDock: 恢复定时任务后重排失败 (id=%s): %v", id, err)
	}
	a.wakeScheduler()
}

func (a *AppService) scheduleLoop() {
	defer logger.RecoverPanic("schedule runner")
	time.Sleep(3 * time.Second)
	for {
		a.checkScheduledTasks()
		dur := a.nextScheduleWait()
		timer := time.NewTimer(dur)
		select {
		case <-a.schedulerQuit:
			timer.Stop()
			return
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
		logger.W("QuickDock: 定时任务查询失败: %v", err)
		return
	}
	for i := range tasks {
		t := &tasks[i]
		// inflight 标记：慢任务（http 阻塞 30s / command·open 挂起）在 SetTaskRunResult 重排前，
		// 可能被下一轮调度循环再次选为「到期」并启动第二个重叠协程。LoadOrStore 原子保证同一时刻
		// 仅一个执行协程通过，避免重复执行与重复通知（参照 monitor_checker 的 monitorInflight）。
		if _, loaded := a.schedInflight.LoadOrStore(t.ID, struct{}{}); loaded {
			continue
		}
		// 纳入 taskWG：这些协程写回执行结果时会访问 DB，退出流程必须等它们
		// 收尾才能真正 Close（见 waitForSchedulers）。Add 必须在启动前调用，
		// 否则协程可能先 Done 于 Add。
		a.taskWG.Add(1)
		go func(t *db.ScheduledTask) {
			defer a.taskWG.Done()
			defer a.schedInflight.Delete(t.ID)
			defer func() {
				if r := recover(); r != nil {
					logger.E("QuickDock: schedule runner panic: %v", r)
				}
			}()
			startedAt := time.Now()
			status, result := a.executeTask(t)
			durationMs := time.Since(startedAt).Milliseconds()
			// 执行结果原先只写进 DB 的 run result 字段，日志里毫无痕迹——
			// 用户问「定时任务怎么没执行」时无法判断是没触发还是触发了但失败。
			if status == "ok" {
				logger.I("[schedule] 任务「%s」执行成功 耗时=%v", t.Name, time.Since(startedAt).Round(time.Millisecond))
			} else {
				logger.W("[schedule] 任务「%s」执行失败 status=%s 耗时=%v: %s",
					t.Name, status, time.Since(startedAt).Round(time.Millisecond), logger.Trunc(result, 200))
			}

			// 计算下次运行时间与启用状态：用「任务完成时刻」而非循环开头的陈旧 now，
			// 否则慢任务（HTTP 阻塞 30s / command·open 挂起）执行耗时 ≥ 间隔时，写入的
			// next_run 早已过去，下一轮立即再次命中 → 以「尽可能快」的频率反复执行而非按间隔节拍。
			nextRun := computeNextRun(t, nowStr())
			enabled := t.Enabled
			if t.ScheduleKind == "once" || nextRun == "" {
				// 一次性任务或无法再排期 → 自动停用
				nextRun = ""
				enabled = false
			}
			_ = a.DB.SetTaskRunResult(t.ID, now, status, result, nextRun, enabled)
			// 追加一条执行历史（含耗时），供「运行历史」回看逐次执行情况
			_ = a.DB.InsertScheduleRun(t.ID, t.Name, status, result, durationMs)

			// 执行结果按 t.Notify 推送给系统通知 + 已配置机器人（结果/耗时统一拼进消息体）
			a.notifyScheduleRun(t, status, result, durationMs)
		}(t)
	}
}

// hasNotifyChannel 判断某个通知渠道是否被勾选。
// 空值按「两个都要」处理 —— 与 db 层 normalizeNotifyChannels 同一口径，
// 老库刚补列、或调用方漏传时都不会静默吞掉通知。
func hasNotifyChannel(csv, want string) bool {
	if strings.TrimSpace(csv) == "" {
		return true
	}
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == want {
			return true
		}
	}
	return false
}

// notifyScheduleRun 定时任务执行后，按 t.Notify 总开关与 t.NotifyChannels 渠道推送结果。
//
// 把原来散在自动/手动两条执行路径里的重复通知逻辑收口到一处：二者都曾各自拼 title、
// 各自调 Notifier / sendWebhookNotify，一旦一处改了另一处忘了，就出现「手动跑有通知、
// 到点跑没通知」之类的漂移。结果（含失败原因）与耗时统一在这里拼进消息体，
// 确保机器人收到的消息一定带「执行结果」。
func (a *AppService) notifyScheduleRun(t *db.ScheduledTask, status, result string, durationMs int64) {
	if !t.Notify {
		return
	}
	icon := "✅"
	if status != "ok" {
		icon = "⚠️"
	}
	title := icon + " 定时任务：" + t.Name

	// 结果 + 耗时明确拼进 body：机器人消息一定带「执行结果」，而不是只看到任务名。
	res := strings.TrimSpace(result)
	var b strings.Builder
	b.WriteString("结果：")
	if res == "" {
		b.WriteString(status) // 结果为空兜底，至少给个 ok/fail
	} else {
		b.WriteString(res)
	}
	b.WriteString("\n耗时：")
	if durationMs > 0 {
		b.WriteString((time.Duration(durationMs) * time.Millisecond).String())
	} else {
		b.WriteString("—")
	}

	// 分渠道：系统通知与机器人各自可关。此前是一个 notify 布尔同时决定两者，
	// 于是「只想在桌面看到、不想往手机机器人推」这种最普通的诉求无处表达。
	if hasNotifyChannel(t.NotifyChannels, "system") && a.Notifier != nil {
		_ = a.Notifier.SendNotification(notifications.NotificationOptions{
			ID:    "schedtask-" + t.ID + "-" + time.Now().Format("150405"),
			Title: title,
			Body:  b.String(),
		})
	}
	if hasNotifyChannel(t.NotifyChannels, "webhook") {
		a.sendWebhookNotify(title, b.String())
	}
	// 进通知中心：定时任务是「没人盯着的时候发生的事」，正是最需要事后留档的一类。
	level := "success"
	if status != "ok" {
		level = "warn"
	}
	a.PushNotification("schedule", level, title, b.String(), "schedule", t.ID)
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
		return a.executeCommandTask(t)

	case "http":
		return a.executeHTTP(t)

	case "todo":
		return a.executeRecurringTodo(t)
	}
	return "fail", "未知的动作类型：" + t.Action
}

// executeCommandTask 执行命令类任务：等进程结束，捕获 stdout/stderr 与退出码，
// 把「输出摘要 + 退出码」作为执行结果——与 HTTP 动作的「状态码 + 正文预览」
// 同一契约。于是 ls / dir / git status 的输出会进任务卡、运行历史和机器人
// 通知，而不是只看到一句「命令已执行」。
//
// 与旧「即发即忘」的差异：命令要等到结束（最长 schedCommandTimeout）才产出
// 结果。启动 GUI/常驻程序的场景请用「打开软件」动作，那里本就是即发即忘。
func (a *AppService) executeCommandTask(t *db.ScheduledTask) (string, string) {
	out := platform.RunCommandCapture(t.Target, t.WorkingDir, schedCommandTimeout)
	if out.Err != nil {
		return "fail", "命令执行失败：" + out.Err.Error()
	}
	// 输出摘要：优先 stdout（ls/dir 的正常输出走 stdout），为空退回 stderr
	// （失败原因通常在 stderr）。压成单行——撑坏三种消费面没有任何好处。
	text := flattenResult(out.Stdout)
	if text == "" {
		text = flattenResult(out.Stderr)
	}
	switch {
	case out.TimedOut:
		if text != "" {
			return "ok", fmt.Sprintf("命令已执行，%d 秒内未退出（仍在运行）· %s", int(schedCommandTimeout.Seconds()), text)
		}
		return "ok", fmt.Sprintf("命令已执行，%d 秒内未退出（仍在运行）：%s", int(schedCommandTimeout.Seconds()), t.Target)
	case out.ExitCode != 0:
		if text != "" {
			return "fail", fmt.Sprintf("命令执行失败（退出码 %d）· %s", out.ExitCode, text)
		}
		return "fail", fmt.Sprintf("命令执行失败（退出码 %d）：%s", out.ExitCode, t.Target)
	default:
		if text != "" {
			return "ok", fmt.Sprintf("命令已执行（退出码 %d）· %s", out.ExitCode, text)
		}
		return "ok", "命令已执行：" + t.Target
	}
}

// flattenResult 把可能多行/超长的输出压成单行摘要：空白折叠为单个空格，
// 按 rune 截断（不切坏中文）。空结果（纯空白）返回 ""，调用方据此回退到
// 「命令已执行：<目标>」这类不带输出的文案。
func flattenResult(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > schedResultMaxRunes {
		return string(r[:schedResultMaxRunes]) + "…（已截断）"
	}
	return s
}

// executeRecurringTodo 由重复待办触发生成一条具体待办
func (a *AppService) executeRecurringTodo(t *db.ScheduledTask) (string, string) {
	var p struct {
		TodoID   string `json:"todoId"`
		Title    string `json:"title"`
		Priority string `json:"priority"`
		DueDate  string `json:"dueDate"`
		Note     string `json:"note"`
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
	// 截止日按「本次生成的那一天」重算：payload 里的 dueDate 是**建这条重复规则当天**
	// 的快照，原样沿用会让每天的实例都堆在同一个旧日期上（每天新增一条"上周到期"的任务）。
	// 原截止日在未来（提前建好的）则保留，用户显式设的日期不该被覆盖。
	due := p.DueDate
	if today := time.Now().Format("2006-01-02"); due != "" && due < today {
		due = today
	}
	if _, err := a.DB.CreateTodo(p.Title, p.Priority, due, p.Note, "", "", "", "", tags); err != nil {
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
		"todoId":   m.ID,
		"title":    m.Title,
		"priority": m.Priority,
		"dueDate":  m.DueDate,
		"note":     m.Note,
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

// SyncTodoSchedule 导出转发：供 services/todo 门面在新建/更新待办后同步重复调度记录。
func (a *AppService) SyncTodoSchedule(m *db.Todo) {
	a.syncTodoSchedule(m)
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

	client := scheduleHTTPClient
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
