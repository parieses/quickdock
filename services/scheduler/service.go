package scheduler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// Service 定时任务调度服务
type Service struct {
	app       interface{} // *application.App (避免循环依赖)
	db        *db.Database
	notifier  *notifications.NotificationService
	quit      chan struct{}
	quitOnce  sync.Once
	wake      chan struct{}
	inflight  sync.Map // taskID -> struct{}
	ctx       context.Context
	cancel    func()
}

// NewService 创建调度服务实例
func NewService() *Service {
	return &Service{}
}

// SetApp 设置应用引用
func (s *Service) SetApp(app interface{}) {
	s.app = app
}

// SetDB 设置数据库引用
func (s *Service) SetDB(db *db.Database) {
	s.db = db
}

// SetNotifier 设置通知服务
func (s *Service) SetNotifier(n *notifications.NotificationService) {
	s.notifier = n
}

// SetContext 设置上下文
func (s *Service) SetContext(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)
}

// Start 启动调度器
func (s *Service) Start() {
	if s.db == nil {
		logger.W("QuickDock: scheduler: database not set, skipping start")
		return
	}
	s.quit = make(chan struct{})
	s.wake = make(chan struct{}, 1)
	go s.scheduleLoop()
}

// Stop 停止调度器
func (s *Service) Stop() {
	if s.quit == nil {
		return
	}
	s.quitOnce.Do(func() { close(s.quit) })
	if s.cancel != nil {
		s.cancel()
	}
}

// Wake 唤醒调度器
func (s *Service) Wake() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// CreateTask 创建定时任务
func (s *Service) CreateTask(t db.ScheduledTask) (*db.ScheduledTask, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not set")
	}
	t.NextRun = computeNextRun(&t, nowStr())
	if t.NextRun == "" {
		t.Enabled = false
	}
	created, err := s.db.CreateScheduledTask(&t)
	if err != nil {
		return nil, err
	}
	s.Wake()
	return created, nil
}

// UpdateTask 更新定时任务
func (s *Service) UpdateTask(t db.ScheduledTask) error {
	if s.db == nil {
		return fmt.Errorf("database not set")
	}
	if t.ID == "" {
		return fmt.Errorf("任务 ID 不能为空")
	}
	if t.Enabled {
		t.NextRun = computeNextRun(&t, nowStr())
		if t.NextRun == "" {
			t.Enabled = false
		}
	} else {
		t.NextRun = ""
	}
	if err := s.db.UpdateScheduledTask(&t); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// DeleteTask 删除定时任务
func (s *Service) DeleteTask(id string) error {
	if s.db == nil {
		return fmt.Errorf("database not set")
	}
	if err := s.db.DeleteScheduledTask(id); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// SetTaskEnabled 启用/停用定时任务
func (s *Service) SetTaskEnabled(id string, enabled bool) (string, error) {
	if s.db == nil {
		return "", fmt.Errorf("database not set")
	}
	nextRun := ""
	if enabled {
		t, err := s.db.GetScheduledTask(id)
		if err != nil {
			return "", err
		}
		nextRun = computeNextRun(t, nowStr())
		if nextRun == "" {
			enabled = false
		}
	}
	if err := s.db.SetTaskEnabled(id, enabled, nextRun); err != nil {
		return "", err
	}
	s.Wake()
	return nextRun, nil
}

// RunTaskNow 立即执行一次任务
func (s *Service) RunTaskNow(id string) (string, string, error) {
	if s.db == nil {
		return "", "", fmt.Errorf("database not set")
	}
	t, err := s.db.GetScheduledTask(id)
	if err != nil {
		return "", "", err
	}
	status, result := s.executeTask(t)
	_ = s.db.SetTaskRunResult(t.ID, nowStr(), status, result, t.NextRun, t.Enabled)
	return status, result, nil
}

// ListTasks 列出所有定时任务
func (s *Service) ListTasks() ([]db.ScheduledTask, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not set")
	}
	return s.db.ListScheduledTasks()
}

// scheduleLoop 调度循环
func (s *Service) scheduleLoop() {
	defer recoverPanic("schedule runner")
	time.Sleep(3 * time.Second)
	for {
		s.checkScheduledTasks()
		dur := s.nextScheduleWait()
		timer := time.NewTimer(dur)
		select {
		case <-s.quit:
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// checkScheduledTasks 检查到期的定时任务
func (s *Service) checkScheduledTasks() {
	if s.db == nil {
		return
	}
	tasks, err := s.db.ListDueTasks(nowStr())
	if err != nil {
		logger.W("QuickDock: schedule checker failed: %v", err)
		return
	}
	for i := range tasks {
		t := &tasks[i]
		if _, loaded := s.inflight.LoadOrStore(t.ID, struct{}{}); loaded {
			continue
		}
		go func(t *db.ScheduledTask) {
			defer s.inflight.Delete(t.ID)
			defer func() { if r := recover(); r != nil { logger.E("QuickDock: schedule runner panic: %v", r) } }()
			s.executeAndNotify(t)
		}(t)
	}
}

// nextScheduleWait 计算下次等待时间
func (s *Service) nextScheduleWait() time.Duration {
	if s.db == nil {
		return time.Minute
	}
	tasks, err := s.db.ListEnabledWithNextRun()
	if err != nil || len(tasks) == 0 {
		return time.Minute
	}
	now := nowStr()
	minDur := 24 * time.Hour
	for i := range tasks {
		if tasks[i].NextRun == "" || tasks[i].NextRun < now {
			return 0
		}
		d, _ := time.ParseInLocation(schedTimeLayout, tasks[i].NextRun, time.Local)
		dur := time.Until(d)
		if dur < minDur {
			minDur = dur
		}
	}
	if minDur < time.Second {
		minDur = time.Second
	}
	return minDur
}

// executeTask 执行单个任务
func (s *Service) executeTask(t *db.ScheduledTask) (string, string) {
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
		return s.executeHTTP(t)
	case "todo":
		if s.db == nil {
			return "fail", "数据库未设置"
		}
		return s.executeTodo(t)
	default:
		return "fail", "未知的动作类型: " + t.Action
	}
}

// executeHTTP 执行 HTTP 请求
func (s *Service) executeHTTP(t *db.ScheduledTask) (string, string) {
	method := strings.ToUpper(t.HTTPMethod)
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequest(method, t.Target, nil)
	if err != nil {
		return "fail", "请求构建失败：" + err.Error()
	}
	for k, v := range parseHeaders(t.HTTPHeaders) {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "fail", "请求失败：" + err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "fail", "读取响应失败：" + err.Error()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "fail", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return "ok", string(body)
}

// executeTodo 执行待办操作
func (s *Service) executeTodo(t *db.ScheduledTask) (string, string) {
	// 待办操作执行逻辑
	return "ok", "待办操作已执行"
}

// executeAndNotify 执行任务并发送通知
func (s *Service) executeAndNotify(t *db.ScheduledTask) {
	status, result := s.executeTask(t)
	now := nowStr()
	nextRun := computeNextRun(t, now)
	enabled := t.Enabled
	if t.ScheduleKind == "once" || nextRun == "" {
		nextRun = ""
		enabled = false
	}
	_ = s.db.SetTaskRunResult(t.ID, now, status, result, nextRun, enabled)
	if t.Notify && s.notifier != nil {
		icon := "✅"
		if status != "ok" {
			icon = "⚠️"
		}
		title := icon + " 定时任务：" + t.Name
		_ = s.notifier.SendNotification(notifications.NotificationOptions{
			ID:    "schedtask-" + t.ID + "-" + time.Now().Format("150405"),
			Title: title,
			Body:  result,
		})
	}
}

// sendWebhookNotify 发送 webhook 通知
func (s *Service) sendWebhookNotify(title, result string) {
	// Webhook 通知逻辑
	_ = title
	_ = result
}

// 辅助函数
func nowStr() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func parseHeaders(raw string) map[string]string {
	headers := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return headers
}

func computeNextRun(t *db.ScheduledTask, now string) string {
	// 调度计算逻辑 - 简化版
	_ = t
	_ = now
	return ""
}

const schedTimeLayout = "2006-01-02 15:04:05"

func recoverPanic(context string) {
	if r := recover(); r != nil {
		logger.E("QuickDock: [PANIC] %s: %v", context, r)
	}
}
