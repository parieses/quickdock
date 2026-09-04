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

const apacheBaseRel = "runtime/apache"

const apacheDefaultPort = 80

// apacheSrvRootRe 匹配 Apache Lounge 默认 httpd.conf 中的 `Define SRVROOT "c:/Apache24"` 指令，
// 安装后改写为 QuickDock 的版本目录，使 modules/ 等相对路径正确解析（否则 httpd 会去 c:/Apache24 找模块）。
var apacheSrvRootRe = regexp.MustCompile(`(?i)^(\s*Define\s+SRVROOT\s+").*(")`)

// ApacheRuntime 管理便携 Apache HTTP Server 运行时（Apache Lounge VS17 构建）。
// 实现 ServiceController：以 `httpd -d <dir>`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
// 安装后改写 conf/httpd.conf 的 SRVROOT 指向版本目录，保证模块/日志相对路径正确。
type ApacheRuntime struct {
	baseDir string
}

func NewApacheRuntime() *ApacheRuntime {
	return &ApacheRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), apacheBaseRel)}
}

func (a *ApacheRuntime) Kind() Runtime                 { return RuntimeApache }
func (a *ApacheRuntime) DisplayName() string          { return DisplayName(RuntimeApache) }
func (a *ApacheRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (a *ApacheRuntime) Recommended() []string        { return Versions(RuntimeApache) }

func (a *ApacheRuntime) versionDir(version string) string {
	return filepath.Join(a.baseDir, version)
}

func (a *ApacheRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(a.versionDir(version), "bin", "httpd.exe")
	}
	return filepath.Join(a.versionDir(version), "bin", "httpd")
}

func (a *ApacheRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(a.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(a.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: a.versionDir(v)})
				dirs.record(filepath.Dir(a.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("httpd"); err == nil {
		if v := parseApacheVersion(RunVersion(p, "-v")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (a *ApacheRuntime) DeleteVersion(version string) error {
	dir := a.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (a *ApacheRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeApache)[0]
	}
	dir := a.versionDir(version)
	if _, err := os.Stat(a.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Apache " + version + " 已安装: " + a.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeApache, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Apache 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-apache-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Apache "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Apache " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Apache 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Apache…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Apache 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Apache 失败: %w", err)
	}
	if _, err := os.Stat(a.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", a.ExeFor(version))
	}
	// 改写 SRVROOT → 版本目录（Apache Lounge 默认指向 c:/Apache24），使模块/日志相对路径正确
	if err := a.ensureConfig(version); err != nil {
		logger.W("[env][apache] 改写 SRVROOT 失败（启动前需手动指定 -d）: %v", err)
	}
	if cb.OnLog != nil {
		cb.OnLog("Apache " + version + " 解压完成")
	}
	return nil
}

// ensureConfig 把 httpd.conf 的 SRVROOT 改写为版本目录（正斜杠），失败返回错误（不影响安装本身）。
func (a *ApacheRuntime) ensureConfig(version string) error {
	p := filepath.Join(a.versionDir(version), "conf", "httpd.conf")
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	srvroot := strings.ReplaceAll(a.versionDir(version), "\\", "/")
	replaced := apacheSrvRootRe.ReplaceAllStringFunc(string(data), func(line string) string {
		m := apacheSrvRootRe.FindStringSubmatch(line)
		if len(m) < 3 {
			return line
		}
		return m[1] + srvroot + m[2]
	})
	return os.WriteFile(p, []byte(replaced), 0644)
}

// ---- ServiceController ----

func (a *ApacheRuntime) DefaultPort() int { return apacheDefaultPort }

func (a *ApacheRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := a.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 Apache 版本")
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
			exe, wd = a.ExeFor(version), a.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	running, _ := svcMgr.info(RuntimeApache)
	if running != "" && running != version {
		return fmt.Errorf("Apache 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(apacheDefaultPort) {
		logger.W("[env][apache] Start 拒绝：端口 %d 已被占用（可能是 IIS/Skype）", apacheDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口或更改 Apache 监听端口", apacheDefaultPort)
	}
	if err := a.ensureConfig(version); err != nil {
		logger.W("[env][apache] SRVROOT 改写失败: %v", err)
	}
	if onLog != nil {
		onLog("启动 Apache " + version + " …")
	}
	return svcMgr.start(RuntimeApache, version, exe, wd, []string{"-d", wd}, a.LogPath(version), onLog)
}

func (a *ApacheRuntime) LogPath(version string) string {
	return filepath.Join(a.versionDir(version), "logs", "apache.log")
}

func (a *ApacheRuntime) Stop(version string) error {
	stopByPort(apacheDefaultPort, "httpd.exe")
	svcMgr.forget(RuntimeApache)
	return nil
}

func (a *ApacheRuntime) Status(version string) ServiceStatus {
	port := apacheDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeApache); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeApache)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "httpd.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseApacheVersion 解析 `httpd -v` 输出（如 "Server version: Apache/2.4.62 (Win64)"）。
func parseApacheVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "Apache/") {
			return strings.TrimPrefix(tok, "Apache/")
		}
	}
	return ""
}
