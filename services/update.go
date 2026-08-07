package services

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"golang.org/x/sys/windows"
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

	// 启动内置更新窗口进行下载和安装。整体超时由 ctx 控制：
	// HTTP 客户端已不设 30s 硬上限（否则大安装包会被掐断），这里给足
	// 30 分钟的余量以适配慢速/代理网络，同时避免永久挂起。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
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

// RestartApp 下载完成后拉起 NSIS 安装器以完成更新。
//
// 流程：Updater.DownloadAndInstall 已把安装器下载并验签到本地临时路径（通过 DownloadedPath 取得），
// 这里直接拉起安装器（非静默，展示安装向导）。安装器的 .onInit 阶段会 taskkill 仍在运行的
// quickdock.exe，向导完成后提供"运行 QuickDock"勾选项，用户点完成即可重新打开应用。
//
// 安装器用 DETACHED_PROCESS 脱离主程序作业对象独立存活——否则主程序退出时作业对象会连带杀掉它，
// 向导就弹不出来了。同时先打上"真退出"标记（PrepareQuitFn），让主窗口的 WindowClosing 钩子放行，
// 主程序走"退出"路径干净清理，再由安装器覆盖文件。
func (a *AppService) RestartApp() error {
	if a.app == nil || a.app.Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}

	// 取已下载并验签的安装器本地路径
	installerPath := a.app.Updater.DownloadedPath()
	if installerPath == "" {
		return fmt.Errorf("更新尚未就绪：请先完成下载，再点击重启")
	}

	// 让 WindowClosing 钩子放行（与托盘"退出"同一路径），主程序走干净退出流程
	if a.PrepareQuitFn != nil {
		a.PrepareQuitFn()
	}

	// 拉起 NSIS 安装器（非静默，展示安装向导）。安装器 .onInit 会自行关闭正在运行的
	// QuickDock，并在完成后提供"运行"勾选项。DETACHED_PROCESS 保证它不随主程序退出而被杀。
	cmd := exec.Command(installerPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新安装器失败: %w", err)
	}

	// 退出主程序；安装器接管后续的安装向导与重新运行
	a.app.Quit()
	return nil
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
	case strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client.timeout") || strings.Contains(lower, "exceeded while reading"):
		return "下载超时：安装包体积较大或网络连接较慢导致下载未完成。请检查网络，配置代理（HTTPS_PROXY 环境变量），或手动从 GitHub Releases 下载安装。"
	case strings.Contains(lower, "github: download"):
		return "下载安装包失败：直连 GitHub 与加速镜像均无法访问。请检查网络或配置代理（HTTPS_PROXY），也可手动从 GitHub Releases 页面下载安装。"
	default:
		return msg
	}
}
