package services

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
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

// runCheck 执行一次更新探测（阻塞，串行于 updateCheckMu）。
// 手动"检测更新"与后台定时检查共用此方法，保证两条路径返回完全一致的
// UpdateStatus（含版本号与更新说明），从而消除自动/手动流程不一致的问题。
func (a *AppService) runCheck(ctx context.Context) *UpdateStatus {
	a.updateCheckMu.Lock()
	defer a.updateCheckMu.Unlock()

	release, err := a.app.Updater.Check(ctx)
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

	st := &UpdateStatus{
		CurrentVersion:   a.GetAppVersion(),
		State:            "available",
		AvailableVersion: release.Version,
		ReleaseNotes:     release.Notes,
	}
	a.setLastCheck(st)
	return st
}

func (a *AppService) setLastCheck(st *UpdateStatus) {
	a.lastUpdateCheckMu.Lock()
	a.lastUpdateCheck = st
	a.lastUpdateCheckMu.Unlock()
}

func (a *AppService) getLastCheck() *UpdateStatus {
	a.lastUpdateCheckMu.RLock()
	defer a.lastUpdateCheckMu.RUnlock()
	return a.lastUpdateCheck
}

// emitUpdateStatus 把检测结果推给前端，复用 SettingsModal 里手动检测那套 UI。
func (a *AppService) emitUpdateStatus(st *UpdateStatus) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit("quickdock:update:status", st)
}

// CheckForUpdates 手动检查更新（阻塞直到检查完成）。与后台定时检查共用 runCheck。
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
	return a.runCheck(ctx)
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

	// 以管理员权限（UAC 提权）拉起 NSIS 安装器。
	// QuickDock 平时以普通权限运行；只有装到 Program Files / 覆盖程序文件这一步需要
	// 管理员权限，故通过 ShellExecute(verb="runas") 触发 Windows UAC 确认框，用户点是
	// 即以管理员身份启动安装器。安装器 .onInit 会自行关闭 QuickDock，完成后提供"运行"勾选项。
	//
	// 为什么不用 exec.Command：exec 不会提权，直接启动需管理员权限的安装器会返回
	// "The requested operation requires elevation"。
	if err := launchElevated(installerPath); err != nil {
		return fmt.Errorf("启动更新安装器失败（需要管理员权限，请在 UAC 弹窗中点「是」）: %w", err)
	}

	// 退出主程序；安装器接管后续的安装向导与重新运行
	a.app.Quit()
	return nil
}

// launchElevated 通过 ShellExecuteW(verb="runas") 以管理员权限拉起程序（触发 UAC 确认）。
// 不依赖额外的依赖库，直接用 shell32 的 ShellExecuteW。
func launchElevated(path string) error {
	procShellExecuteW := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(path)
	params, _ := windows.UTF16PtrFromString("")
	dir, _ := windows.UTF16PtrFromString("")
	const (
		swShowNormal   = 1
		errorCancelled = 1223 // ERROR_CANCELLED：用户点了 UAC 的"否"
	)
	r1, _, callErr := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)), uintptr(unsafe.Pointer(dir)), swShowNormal)
	// ShellExecuteW 成功时返回 >32；失败返回 <=32 的错误代码。
	if int(uintptr(r1)) <= 32 {
		if int(uintptr(r1)) == errorCancelled {
			return fmt.Errorf("已取消提权（未授予管理员权限）")
		}
		return fmt.Errorf("ShellExecute 失败 (code=%d): %v", uint32(r1), callErr)
	}
	return nil
}

// GetUpdateState 获取当前更新器状态。当已探测到新版本时回填版本号与更新说明，
// 否则 UI 只会拿到一个光秃秃的 "available" 状态而看不到任何内容。
func (a *AppService) GetUpdateState() *UpdateStatus {
	if a.app == nil || a.app.Updater == nil {
		return &UpdateStatus{
			CurrentVersion: a.GetAppVersion(),
			State:          "unavailable",
		}
	}

	live := a.app.Updater.State()
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

// StartAutoUpdateChecker 启动后台定时检查（取代 Wails 内置的 CheckInterval 自动下载）。
//
// 为什么不用 Wails 的 CheckInterval：它的周期检查走 CheckAndInstall —— 会自动把安装包
// 下载并暂存，但永远不会主动重启应用（Restart 仅由 Wails 内置窗口的"重启"按钮触发，而
// 我们用的是自定义 UI，不挂内置窗口）。结果就是自动下载了一堆安装包却永远不生效，且与手动
// "检测更新"的下载/重启路径完全脱节。
//
// 这里只做"检查 + 通知"：发现新版本后通过 quickdock:update:status 事件把同一份 UpdateStatus
// 推给前端，复用 SettingsModal 里手动检测那套 UI 与下载/重启逻辑，两条路径完全一致。
func (a *AppService) StartAutoUpdateChecker() {
	if a.app == nil || a.app.Updater == nil {
		return
	}
	go func() {
		// 启动后延迟 30s 首检，避免拖慢冷启动；之后每 24h 一次。
		defer recoverPanic("auto update checker") // 长驻循环内 panic 会崩进程，必须兜底
		time.Sleep(30 * time.Second)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			a.backgroundCheck()
		}
	}()
}

// backgroundCheck 单次后台检查：仅在空闲/已最新/出错时探测，避免重复网络请求与弹窗；
// 已处于"有更新/下载中/就绪"状态时跳过，保持现状不骚扰用户。
func (a *AppService) backgroundCheck() {
	u := a.app.Updater
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

	// 自动发现新版本时发一条系统通知，让更新真正"被看见"（点击仍走设置页的下载/重启）。
	if status.State == "available" && a.Notifier != nil {
		_ = a.Notifier.SendNotification(notifications.NotificationOptions{
			Title: "QuickDock 更新可用",
			Body:  "发现新版本 " + status.AvailableVersion + "，打开设置即可下载安装。",
		})
	}
}
