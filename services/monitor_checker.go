package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/db"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// StartMonitorChecker 启动网站监控检查器（仿 UptimeRobot）。
// 同样采用精确定时器：每次检测后按 interval 计算最近待检监控的等待时长并精确 sleep，
// 监控增删改/启停时用 monitorWake 立即唤醒重排。
func (a *AppService) StartMonitorChecker() {
	a.monitorWake = make(chan struct{}, 1)
	go a.monitorLoop()
}

func (a *AppService) wakeMonitorChecker() {
	if a.monitorWake == nil {
		return
	}
	select {
	case a.monitorWake <- struct{}{}:
	default:
	}
}

func (a *AppService) monitorLoop() {
	defer recoverPanic("monitor checker")
	time.Sleep(3 * time.Second)
	for {
		a.runDueMonitors()
		dur := a.nextMonitorWait()
		timer := time.NewTimer(dur)
		select {
		case <-timer.C:
		case <-a.monitorWake:
			timer.Stop()
		}
	}
}

// runDueMonitors 检测所有「已到点」的监控（并发执行，互不影响）
func (a *AppService) runDueMonitors() {
	if a.DB == nil {
		return
	}
	now := time.Now().Unix()
	monitors, err := a.DB.ListMonitorsDue(now)
	if err != nil {
		fmt.Println("QuickDock: 监控查询失败:", err)
		return
	}
	for i := range monitors {
		m := &monitors[i]
		go a.checkOneMonitor(m)
	}
}

// nextMonitorWait 计算距「最近一次待检」还需等待的时长
func (a *AppService) nextMonitorWait() time.Duration {
	if a.DB == nil {
		return time.Minute
	}
	ms, err := a.DB.ListEnabledMonitors()
	if err != nil || len(ms) == 0 {
		return time.Minute
	}
	now := time.Now().Unix()
	minDur := 24 * time.Hour
	for i := range ms {
		next := ms[i].LastCheckedTs + int64(ms[i].IntervalSec)
		d := time.Duration(next-now) * time.Second
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

// checkOneMonitor 对单个监控执行一次检测，写入日志与状态，并在状态翻转时发通知
// 返回 (status, summary)
func (a *AppService) checkOneMonitor(m *db.Monitor) (string, string) {
	defer recoverPanic("monitor check:" + m.ID)
	// 用户可能在调度间隙停用了该监控，重查 enabled 状态
	if current, err := a.DB.GetMonitor(m.ID); err != nil || !current.Enabled {
		return "", ""
	}

	up, code, latency, errMsg := probeMonitor(m)
	status := "down"
	if up {
		status = "up"
	}
	checkedAt := nowStr()
	checkedTs := time.Now().Unix()

	_ = a.DB.AddMonitorLog(&db.MonitorLog{
		MonitorID:  m.ID,
		CheckedAt:  checkedAt,
		CheckedTs:  checkedTs,
		Status:     status,
		StatusCode: code,
		LatencyMs:  latency,
		Error:      errMsg,
	})
	_ = a.DB.UpdateMonitorStatus(m.ID, status, checkedAt, checkedTs, latency, code, errMsg)

	summary := fmt.Sprintf("%s · %d · %dms", status, code, latency)
	if !up && errMsg != "" {
		summary = errMsg
	}

	// SSL 证书到期检查（仅 HTTPS 监控）
	if u, perr := url.Parse(m.URL); perr == nil && strings.EqualFold(u.Scheme, "https") {
		host := u.Hostname()
		port := 443
		if p, e := strconv.Atoi(u.Port()); e == nil {
			port = p
		}
		if expiresAt, cerr := fetchCertExpiry(host, port, m.TimeoutSec); cerr == nil && expiresAt > 0 {
			now := time.Now().Unix()
			warnWindow := int64(m.CertWarnDays) * 86400
			newLastWarned := m.LastCertWarned
			if expiresAt-now <= warnWindow && m.LastCertWarned == 0 {
				daysLeft := (expiresAt - now) / 86400
				title := "🔐 证书即将过期：" + m.Name
				body := fmt.Sprintf("%s\n证书将于 %s 过期（剩 %d 天）", m.URL, time.Unix(expiresAt, 0).Format("2006-01-02"), daysLeft)
				if a.Notifier != nil {
					_ = a.Notifier.SendNotification(notifications.NotificationOptions{
						ID:    "mon-cert-" + m.ID + "-" + time.Now().Format("150405"),
						Title: title,
						Body:  body,
					})
				}
				a.sendWebhookNotify(title, body)
				newLastWarned = now
			}
			_ = a.DB.UpdateMonitorCert(m.ID, expiresAt, newLastWarned)
		}
	}

	// 仅在状态翻转时通知（桌面通知 + 机器人 Webhook 共用同一套 down/up 开关）
	prev := m.LastStatus
	switch {
	case status == "down" && prev != "down" && m.NotifyDown:
		title := "🔴 监控告警：" + m.Name
		body := m.URL
		if errMsg != "" {
			body += "\n" + errMsg
		}
		if a.Notifier != nil {
			_ = a.Notifier.SendNotification(notifications.NotificationOptions{
				ID:    "mon-down-" + m.ID + "-" + time.Now().Format("150405"),
				Title: title,
				Body:  body,
			})
		}
		a.sendWebhookNotify(title, body)
	case status == "up" && prev == "down" && m.NotifyUp:
		title := "🟢 已恢复：" + m.Name
		body := fmt.Sprintf("%s\n响应 %d · %dms", m.URL, code, latency)
		if a.Notifier != nil {
			_ = a.Notifier.SendNotification(notifications.NotificationOptions{
				ID:    "mon-up-" + m.ID + "-" + time.Now().Format("150405"),
				Title: title,
				Body:  body,
			})
		}
		a.sendWebhookNotify(title, body)
	}
	return status, summary
}

// probeMonitor 实际发起 HTTP 请求并判定 up/down
func probeMonitor(m *db.Monitor) (up bool, code int, latencyMs int, errMsg string) {
	method := strings.ToUpper(strings.TrimSpace(m.Method))
	if method == "" {
		method = "GET"
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.TimeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, m.URL, nil)
	if err != nil {
		return false, 0, 0, "请求构建失败：" + err.Error()
	}
	req.Header.Set("User-Agent", "QuickDock-Monitor/1.0")

	// 默认复用 http.DefaultTransport，仅在需要跳过 TLS 校验时创建自定义 Transport
	var transport http.RoundTripper = http.DefaultTransport
	if m.SkipTLSVerify {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	client := &http.Client{
		Timeout:       time.Duration(m.TimeoutSec) * time.Second,
		Transport:     transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if m.FollowRedirects {
				return nil
			}
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return false, 0, latency, "请求失败：" + err.Error()
	}
	defer resp.Body.Close()
	// 读取响应体（带 2MB 上限，用于内容匹配；其余丢弃）
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))

	if !matchesExpected(resp.StatusCode, m.ExpectedStatus) {
		return false, resp.StatusCode, latency,
			fmt.Sprintf("状态码不符：期望 %s，实际 %d", m.ExpectedStatus, resp.StatusCode)
	}

	// 内容匹配（关键字/正则）
	if !matchesContent(body, m) {
		return false, resp.StatusCode, latency,
			fmt.Sprintf("内容不符：%s '%s'", m.ContentMatchType, m.ContentMatchPattern)
	}
	return true, resp.StatusCode, latency, ""
}

// matchesContent 按 content_match_type 判定响应体是否满足条件。
// 无匹配配置时直接通过；非法正则视为不匹配。
func matchesContent(body []byte, m *db.Monitor) bool {
	if m.ContentMatchType == "" || m.ContentMatchType == "none" || m.ContentMatchPattern == "" {
		return true
	}
	s := string(body)
	switch m.ContentMatchType {
	case "contains":
		return strings.Contains(s, m.ContentMatchPattern)
	case "not_contains":
		return !strings.Contains(s, m.ContentMatchPattern)
	case "regex":
		matched, err := regexp.MatchString(m.ContentMatchPattern, s)
		if err != nil {
			return false // 非法正则：判定为不匹配
		}
		return matched
	}
	return true
}

// fetchCertExpiry 通过独立 TLS 握手读取对端证书过期时间（unix 秒）。
// 使用 InsecureSkipVerify 确保即使证书已过期/自签也能读到 NotAfter。
func fetchCertExpiry(host string, port, timeoutSec int) (int64, error) {
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	dialer := &net.Dialer{Timeout: time.Duration(timeoutSec) * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, strconv.Itoa(port)),
		&tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return 0, fmt.Errorf("无对等证书")
	}
	return state.PeerCertificates[0].NotAfter.Unix(), nil
}

// matchesExpected 判断状态码是否满足期望（支持 2xx/3xx... 或精确码）
func matchesExpected(code int, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return code >= 200 && code < 400
	}
	if strings.HasSuffix(expected, "xx") && len(expected) == 3 {
		if digit, err := strconv.Atoi(expected[:1]); err == nil {
			return code/100 == digit
		}
	}
	if n, err := strconv.Atoi(expected); err == nil {
		return code == n
	}
	return code >= 200 && code < 400
}
