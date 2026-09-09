// Package update 自动更新服务
// 合并了 update_service.go (门面) 和 update.go (核心逻辑)
package update

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/services"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

// UpdateService 承载自动更新领域方法。
type UpdateService struct {
	App *services.AppService

	// 更新检查缓存
	lastUpdateCheck   *UpdateStatus
	lastUpdateCheckMu sync.RWMutex
	updateCheckMu     sync.Mutex
}

// NewUpdateService 创建更新门面服务
func NewUpdateService(app *services.AppService) *UpdateService {
	return &UpdateService{App: app}
}

// ===== 内部辅助 =====

// recoverPanic 兜底 recover 打日志
func recoverPanic(context string) {
	logger.RecoverPanic(context)
}

// ===== UpdateStatus =====

// UpdateStatus 返回给前端的更新状态
type UpdateStatus struct {
	CurrentVersion   string  `json:"currentVersion"`
	State            string  `json:"state"` // idle / checking / available / up-to-date / downloading / ready / error
	AvailableVersion string  `json:"availableVersion,omitempty"`
	ReleaseNotes     string  `json:"releaseNotes,omitempty"`
	DownloadProgress float64 `json:"downloadProgress,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// ===== UpdateService 方法 =====

// GetAppVersion 返回当前应用版本号
func (a *UpdateService) GetAppVersion() string {
	if a.App.AppVersion != "" {
		return a.App.AppVersion
	}
	return "0.0.0"
}

// runCheck 执行一次更新探测（阻塞，串行于 updateCheckMu）
func (a *UpdateService) runCheck(ctx context.Context) *UpdateStatus {
	a.updateCheckMu.Lock()
	defer a.updateCheckMu.Unlock()

	release, err := a.App.App().Updater.Check(ctx)
	if err != nil {
		st := &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          friendlyError(err),
		}
		a.setLastCheck(st)
		return st
	}
	if release == nil {
		st := &UpdateStatus{CurrentVersion: a.GetAppVersion(), State: "up-to-date"}
		a.setLastCheck(st)
		return st
	}

	// 该版本已被守卫判定为"替换后版本号不变"，不再提示
	if IsVersionSkipped(release.Version) {
		logger.W("[update] 版本 %s 已被忽略（连续替换后版本号未变化），本次按已是最新处理", release.Version)
		st := &UpdateStatus{CurrentVersion: a.GetAppVersion(), State: "up-to-date"}
		a.setLastCheck(st)
		return st
	}

	st := &UpdateStatus{
		CurrentVersion:   a.GetAppVersion(),
		State:            "available",
		AvailableVersion: release.Version,
		ReleaseNotes:     release.Notes,
	}
	a.setLastCheck(st)
	return st
}

func (a *UpdateService) setLastCheck(st *UpdateStatus) {
	a.lastUpdateCheckMu.Lock()
	a.lastUpdateCheck = st
	a.lastUpdateCheckMu.Unlock()
}

func (a *UpdateService) getLastCheck() *UpdateStatus {
	a.lastUpdateCheckMu.RLock()
	defer a.lastUpdateCheckMu.RUnlock()
	return a.lastUpdateCheck
}

func (a *UpdateService) emitUpdateStatus(st *UpdateStatus) {
	if a.App.App() == nil {
		return
	}
	a.App.App().Event.Emit("quickdock:update:status", st)
}

// CheckForUpdates 手动检查更新（阻塞直到检查完成）
func (a *UpdateService) CheckForUpdates() *UpdateStatus {
	if a.App.App() == nil || a.App.App().Updater == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          "更新器未初始化",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return a.runCheck(ctx)
}

// DownloadUpdate 下载发现的更新（阻塞直到下载完成）
func (a *UpdateService) DownloadUpdate() *UpdateStatus {
	if a.App.App() == nil || a.App.App().Updater == nil {
		return &UpdateStatus{
			State: "error",
			Error: "更新器未初始化",
		}
	}

	state := a.App.App().Updater.State()
	if state != updater.StateAvailable {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          string(state),
			Error:          "没有待下载的更新",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := a.App.App().Updater.DownloadAndInstall(ctx); err != nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          friendlyError(err),
		}
	}

	return &UpdateStatus{
		CurrentVersion: a.GetAppVersion(),
		State:          "ready",
	}
}

// RestartApp 在下载并验签完成后执行"就地替换"更新
func (a *UpdateService) RestartApp() error {
	logger.I("[update] 就地替换更新：派生 helper，父进程退出后覆盖 exe 并重新拉起（免安装器/UAC）")
	if a.App.App() == nil || a.App.App().Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}

	if a.App.PrepareQuitFn != nil {
		a.App.PrepareQuitFn()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	targetVersion := ""
	if lc := a.getLastCheck(); lc != nil {
		targetVersion = lc.AvailableVersion
	}
	MarkPendingUpdate(targetVersion)

	if err := a.App.App().Updater.Restart(ctx); err != nil {
		ClearPendingUpdate()
		return fmt.Errorf("启动就地替换更新失败: %w", err)
	}
	return nil
}

// GetUpdateState 获取当前更新器状态
func (a *UpdateService) GetUpdateState() *UpdateStatus {
	if a.App.App() == nil || a.App.App().Updater == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "unavailable",
		}
	}

	live := a.App.App().Updater.State()
	st := &UpdateStatus{
		CurrentVersion: a.GetAppVersion(),
		State:          string(live),
	}
	if live == updater.StateAvailable {
		if lc := a.getLastCheck(); lc != nil {
			st.AvailableVersion = lc.AvailableVersion
			st.ReleaseNotes = lc.ReleaseNotes
		}
	}
	return st
}

// SkipUpdate 跳过指定版本的更新
func (a *UpdateService) SkipUpdate(version string) error {
	if a.App.App() == nil || a.App.App().Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}
	a.App.App().Updater.SkipVersion(version)
	return nil
}

// StartAutoUpdateChecker 启动后台定时检查
func (a *UpdateService) StartAutoUpdateChecker() {
	if a.App.App() == nil || a.App.App().Updater == nil {
		return
	}
	go func() {
		defer recoverPanic("auto update checker")
		time.Sleep(30 * time.Second)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			a.backgroundCheck()
		}
	}()
}

// backgroundCheck 单次后台检查
func (a *UpdateService) backgroundCheck() {
	u := a.App.App().Updater
	switch u.State() {
	case updater.StateReady, updater.StateDownloading, updater.StateVerifying,
		updater.StateInstalling, updater.StateAvailable:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	status := a.runCheck(ctx)
	if status == nil {
		return
	}
	a.emitUpdateStatus(status)

	if status.State == "available" && a.App.Notifier != nil {
		_ = a.App.Notifier.SendNotification(notifications.NotificationOptions{
			Title: "QuickDock 更新可用",
			Body:  "发现新版本 " + status.AvailableVersion + "，打开设置即可下载安装。",
		})
	}
}

// friendlyError 将底层网络错误转为用户友好的中文提示
func friendlyError(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "connectex") || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "no route to host"):
		return "网络连接失败，无法访问 GitHub。请检查网络或配置代理（HTTPS_PROXY），也可手动从 GitHub Releases 下载。"
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "dns lookup failed"):
		return "DNS 解析失败，无法解析 GitHub 域名。请检查网络连接或 DNS 配置。"
	case strings.Contains(lower, "tls") || strings.Contains(lower, "certificate"):
		return "TLS/SSL 连接错误。请检查系统时间或网络环境。"
	case strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client.timeout") || strings.Contains(lower, "exceeded while reading"):
		return "下载超时：安装包体积较大或网络连接较慢导致下载未完成。请检查网络，配置代理（HTTPS_PROXY 环境变量），或手动从 GitHub Releases 下载安装。"
	case strings.Contains(lower, "github: download"):
		return "下载安装包失败：直连 GitHub 与加速镜像均无法访问。请检查网络或配置代理（HTTPS_PROXY），也可手动从 GitHub Releases 页面下载安装。"
	default:
		return msg
	}
}
