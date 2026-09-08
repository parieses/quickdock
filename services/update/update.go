package update

import (
	"context"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

// UpdateStatus 返回给前端的更新状态
type UpdateStatus struct {
	CurrentVersion   string  `json:"currentVersion"`
	State            string  `json:"state"` // idle / checking / available / up-to-date / downloading / ready / error
	AvailableVersion string  `json:"availableVersion,omitempty"`
	ReleaseNotes     string  `json:"releaseNotes,omitempty"`
	DownloadProgress float64 `json:"downloadProgress,omitempty"` // 0-100
	Error            string  `json:"error,omitempty"`
}

// GetAppVersion 返回当前应用版本号
func (a *UpdateService) GetAppVersion() string {
	if a.App.AppVersion != "" {
		return a.App.AppVersion
	}
	return "0.0.0"
}

// runCheck 执行一次更新探测（阻塞，串行于 updateCheckMu）。
// 手动"检测更新"与后台定时检查共用此方法，保证两条路径返回完全一致的
// UpdateStatus（含版本号与更新说明），从而消除自动/手动流程不一致的问题。
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

	// 该版本已被守卫判定为"替换后版本号不变"（发布包版本号注入异常），不再提示
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

// emitUpdateStatus 把检测结果推给前端，复用 SettingsModal 里手动检测那套 UI。
func (a *UpdateService) emitUpdateStatus(st *UpdateStatus) {
	if a.App.App() == nil {
		return
	}
	a.App.App().Event.Emit("quickdock:update:status", st)
}

// CheckForUpdates 手动检查更新（阻塞直到检查完成）。与后台定时检查共用 runCheck。
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

	// 检查当前状态
	state := a.App.App().Updater.State()
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
	if err := a.App.App().Updater.DownloadAndInstall(ctx); err != nil {
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

// RestartApp 在下载并验签完成后执行"就地替换"更新——无需安装器、无需 UAC。
//
// 前提：QuickDock 采用 per-user 安装（程序目录普通权限可写，见 build/windows Taskfile 的
// INSTALL_SCOPE=user），更新 payload 即"裸 exe"本身（CI 用 Ed25519 对裸二进制签名，
// 见 main.initUpdater 与 release.yml 的 updater manifest）。因此不再像旧实现那样 UAC 提权
// 拉起 NSIS 安装器，而是直接复用 Wails 更新框架内置的就地替换流程 Updater.Restart：
//
//	1. 框架以当前 exe 派生一个 helper-mode 子进程（经环境变量 WAILS_UPDATER_HELPER 标记，
//	   见 updater.HandleHelperMode），随后调用 host.Quit() 令主进程走正常退出路径释放 exe 文件锁；
//	2. helper 等到父进程退出后，把已下载并验签的新 exe 覆盖到程序目录
//	   （先备份 target→.bak，再替换，失败则回滚备份；Windows 上还会处理"进程退出后
//	   镜像文件仍被内核短暂持有"的换名重试与 .old 清扫、跨卷复制等边界，见 updater 包实现）；
//	3. helper 清除自身 env 哨兵后以普通权限重新拉起新版本，并清理备份与暂存目录后退出。
//
// 框架已内置全部替换/回滚/重试逻辑，本包无需再自研 --swapself / quickswap 等替身。
func (a *UpdateService) RestartApp() error {
	logger.I("[update] 就地替换更新：派生 helper，父进程退出后覆盖 exe 并重新拉起（免安装器/UAC）")
	if a.App.App() == nil || a.App.App().Updater == nil {
		return fmt.Errorf("更新器未初始化")
	}

	// 先打"真退出"标记（与托盘"退出"同一路径，main.go 注入 PrepareQuitFn → trayQuitRequested），
	// 让主窗口的 WindowClosing 钩子放行。Updater.Restart 内部会同步调用 host.Quit()(app.Quit)，
	// 若不打此标记，钩子会把 app.Quit 拦截成"隐藏到托盘"，主进程不退出，helper 在超时后中止替换。
	if a.App.PrepareQuitFn != nil {
		a.App.PrepareQuitFn()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 记录本次要替换到的目标版本，供下次启动核对是否真的生效（详见 guard.go）
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



// GetUpdateState 获取当前更新器状态。当已探测到新版本时回填版本号与更新说明，
// 否则 UI 只会拿到一个光秃秃的 "available" 状态而看不到任何内容。
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
func (a *UpdateService) StartAutoUpdateChecker() {
	if a.App.App() == nil || a.App.App().Updater == nil {
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

	// 自动发现新版本时发一条系统通知，让更新真正"被看见"（点击仍走设置页的下载/重启）。
	if status.State == "available" && a.App.Notifier != nil {
		_ = a.App.Notifier.SendNotification(notifications.NotificationOptions{
			Title: "QuickDock 更新可用",
			Body:  "发现新版本 " + status.AvailableVersion + "，打开设置即可下载安装。",
		})
	}
}

