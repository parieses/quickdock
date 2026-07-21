package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// UpdateStatus 返回给前端的更新状态
type UpdateStatus struct {
	CurrentVersion string `json:"currentVersion"`
	State          string `json:"state"`          // idle / checking / available / up-to-date / downloading / ready / error
	AvailableVersion string `json:"availableVersion,omitempty"`
	ReleaseNotes    string `json:"releaseNotes,omitempty"`
	DownloadProgress float64 `json:"downloadProgress,omitempty"` // 0-100
	Error           string `json:"error,omitempty"`
}

// GetAppVersion 返回当前应用版本号
func (a *AppService) GetAppVersion() string {
	if a.AppVersion != "" {
		return a.AppVersion
	}
	return "0.0.0"
}

// CheckForUpdates 手动检查更新（阻塞直到检查完成）
func (a *AppService) CheckForUpdates() *UpdateStatus {
	if a.app == nil || a.app.Updater == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          "更新器未初始化",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	release, err := a.app.Updater.Check(ctx)
	if err != nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          friendlyError(err),
		}
	}

	if release == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "up-to-date",
		}
	}

	// 发现新版本——触发下载和安装
	status := &UpdateStatus{
		CurrentVersion:   a.GetAppVersion(),
		State:            "available",
		AvailableVersion: release.Version,
		ReleaseNotes:     release.Notes,
	}

	return status
}

// DownloadUpdate 下载发现的更新（阻塞直到下载完成）
func (a *AppService) DownloadUpdate() *UpdateStatus {
	if a.app == nil || a.app.Updater == nil {
		return &UpdateStatus{
			State: "error",
			Error: "更新器未初始化",
		}
	}

	// 检查当前状态
	state := a.app.Updater.State()
	if state != updater.StateAvailable {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          string(state),
			Error:          "没有待下载的更新",
		}
	}

	// 启动内置更新窗口进行下载和安装
	ctx := context.Background()
	if err := a.app.Updater.DownloadAndInstall(ctx); err != nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "error",
			Error:          friendlyError(err),
		}
	}

	// 下载安装后变为 ready 状态
	return &UpdateStatus{
		CurrentVersion: a.GetAppVersion(),
		State:          "ready",
	}
}

// RestartApp 重启应用以完成更新
func (a *AppService) RestartApp() error {
	if a.app == nil || a.app.Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}
	return a.app.Updater.Restart(context.Background())
}

// GetUpdateState 获取当前更新器状态
func (a *AppService) GetUpdateState() *UpdateStatus {
	if a.app == nil || a.app.Updater == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "unavailable",
		}
	}

	state := a.app.Updater.State()

	return &UpdateStatus{
		CurrentVersion: a.GetAppVersion(),
		State:          string(state),
	}
}

// SkipUpdate 跳过指定版本的更新
func (a *AppService) SkipUpdate(version string) error {
	if a.app == nil || a.app.Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}
	a.app.Updater.SkipVersion(version)
	return nil
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
	default:
		return msg
	}
}
