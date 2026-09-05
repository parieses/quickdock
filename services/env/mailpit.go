package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const mailpitBaseRel = "runtime/mailpit"

const mailpitDefaultPort = 8025 // Web UI；SMTP 固定 1025
const mailpitSmtpPort = 1025

var mailpitRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// MailpitRuntime 管理便携 Mailpit 运行时（本地 SMTP + Web 收件箱，单文件 mailpit.exe）。
// 实现 ServiceController：以 `mailpit`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
type MailpitRuntime struct {
	baseDir string
}

func NewMailpitRuntime() *MailpitRuntime {
	return &MailpitRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), mailpitBaseRel)}
}

func (m *MailpitRuntime) Kind() Runtime                 { return RuntimeMailpit }
func (m *MailpitRuntime) DetectArgs() []string          { return []string{"--version"} }
func (m *MailpitRuntime) ParseVersion(out string) (string, error) {
	if v := parseMailpitVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMailpit))
}
func (m *MailpitRuntime) DisplayName() string          { return DisplayName(RuntimeMailpit) }
func (m *MailpitRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (m *MailpitRuntime) Recommended() []string        { return Versions(RuntimeMailpit) }

func (m *MailpitRuntime) versionDir(version string) string {
	return filepath.Join(m.baseDir, version)
}

func (m *MailpitRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "mailpit.exe")
	}
	return filepath.Join(m.versionDir(version), "mailpit")
}

func (m *MailpitRuntime) dataDir(version string) string {
	return filepath.Join(m.versionDir(version), "mailpit.db")
}

// DataDir 返回 Mailpit 数据目录（卸载时可选清理）。
func (m *MailpitRuntime) DataDir(version string) string { return m.dataDir(version) }

func (m *MailpitRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(m.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(m.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: m.versionDir(v)})
				dirs.record(filepath.Dir(m.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("mailpit"); err == nil {
		if v := parseMailpitVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (m *MailpitRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (m *MailpitRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMailpit)[0]
	}
	dir := m.versionDir(version)
	if _, err := os.Stat(m.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Mailpit " + version + " 已安装: " + m.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeMailpit, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Mailpit 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-mailpit-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Mailpit "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Mailpit " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Mailpit 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Mailpit…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Mailpit 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Mailpit 失败: %w", err)
	}
	if _, err := os.Stat(m.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", m.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Mailpit " + version + " 解压完成")
	}
	return nil
}

// ---- ServiceController ----

func (m *MailpitRuntime) DefaultPort() int { return mailpitDefaultPort }

func (m *MailpitRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := m.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 Mailpit 版本")
		}
		version = installs[0].Version
	}
	var exe, wd string
	for _, ins := range installs {
		if ins.Version != version {
			continue
		}
		if ins.Scope == "system" {
			exe, wd = ins.Path, filepath.Dir(ins.Path)
		} else {
			exe, wd = m.ExeFor(version), m.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	running, _ := svcMgr.info(RuntimeMailpit)
	if running != "" && running != version {
		return fmt.Errorf("Mailpit 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(mailpitDefaultPort) {
		logger.W("[env][mailpit] Start 拒绝：端口 %d 已被占用", mailpitDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 Mailpit", mailpitDefaultPort)
	}
	if onLog != nil {
		onLog("启动 Mailpit " + version + " …")
	}
	return svcMgr.start(RuntimeMailpit, version, exe, wd,
		[]string{"--smtp", fmt.Sprintf("%d", mailpitSmtpPort), "--listen", "127.0.0.1:" + fmt.Sprintf("%d", mailpitDefaultPort), "--database", m.dataDir(version)}, m.LogPath(version), onLog)
}

func (m *MailpitRuntime) LogPath(version string) string {
	return filepath.Join(m.versionDir(version), "mailpit.log")
}

func (m *MailpitRuntime) Stop(version string) error {
	stopByPort(mailpitDefaultPort, "mailpit.exe")
	svcMgr.forget(RuntimeMailpit)
	return nil
}

func (m *MailpitRuntime) Status(version string) ServiceStatus {
	port := mailpitDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeMailpit); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeMailpit)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "mailpit.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseMailpitVersion 解析 `mailpit --version` 输出（如 "Version: v1.31.0" / "v1.31.0"）。
func parseMailpitVersion(out string) string {
	return strings.TrimPrefix(mailpitRe.FindString(out), "v")
}
