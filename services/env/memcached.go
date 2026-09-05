package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const memcachedBaseRel = "runtime/memcached"

const memcachedDefaultPort = 11211

// MemcachedRuntime 管理便携 Memcached 运行时（adamyg/memcached-win32 Windows 构建，
// 包内可执行文件为 memcached_service.exe，需用 -d run 以非服务（控制台）模式运行）。
// 实现 ServiceController：前台阻塞拉起，由 svcMgr 记录 PID 并捕获日志。
//
// ⚠️ 必须走 ConPTY（伪控制台）拉起，不能用 exec.Cmd 的管道重定向：
// 该程序会检测自身 stdout/stderr 是否为真实控制台句柄，一旦被重定向到管道或文件，
// 就立即以 code=0 退出且无任何输出，表现为「启动成功、几十毫秒后进程正常退出」。
// 故 Start 用 svcMgr.startPTY 而非 svcMgr.start。
type MemcachedRuntime struct {
	baseDir string
}

func NewMemcachedRuntime() *MemcachedRuntime {
	return &MemcachedRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), memcachedBaseRel)}
}

func (m *MemcachedRuntime) Kind() Runtime                 { return RuntimeMemcached }
func (m *MemcachedRuntime) DetectArgs() []string          { return []string{"--version"} }
func (m *MemcachedRuntime) ParseVersion(out string) (string, error) {
	if v := parseMemcachedVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMemcached))
}
func (m *MemcachedRuntime) DisplayName() string          { return DisplayName(RuntimeMemcached) }
func (m *MemcachedRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (m *MemcachedRuntime) Recommended() []string        { return Versions(RuntimeMemcached) }

func (m *MemcachedRuntime) versionDir(version string) string {
	return filepath.Join(m.baseDir, version)
}

func (m *MemcachedRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "memcached_service.exe")
	}
	return filepath.Join(m.versionDir(version), "memcached_service")
}

func (m *MemcachedRuntime) InstalledVersions() []Install {
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
	if p, err := exec.LookPath("memcached_service"); err == nil {
		if v := parseMemcachedVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (m *MemcachedRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (m *MemcachedRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMemcached)[0]
	}
	dir := m.versionDir(version)
	if _, err := os.Stat(m.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Memcached " + version + " 已安装: " + m.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeMemcached, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Memcached 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-memcached-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Memcached "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Memcached " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Memcached 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Memcached…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Memcached 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Memcached 失败: %w", err)
	}
	if _, err := os.Stat(m.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", m.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Memcached " + version + " 解压完成")
	}
	return nil
}

// ---- ServiceController ----

func (m *MemcachedRuntime) DefaultPort() int { return memcachedDefaultPort }

func (m *MemcachedRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := m.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 Memcached 版本")
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
	running, _ := svcMgr.info(RuntimeMemcached)
	if running != "" && running != version {
		return fmt.Errorf("Memcached 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(memcachedDefaultPort) {
		logger.W("[env][memcached] Start 拒绝：端口 %d 已被占用", memcachedDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 Memcached", memcachedDefaultPort)
	}
	if onLog != nil {
		onLog("启动 Memcached " + version + " …")
	}
	return svcMgr.startPTY(RuntimeMemcached, version, exe, wd,
		[]string{"-d", "run", "-p", fmt.Sprintf("%d", memcachedDefaultPort), "-l", "127.0.0.1", "-v"}, m.LogPath(version), onLog)
}

func (m *MemcachedRuntime) LogPath(version string) string {
	return filepath.Join(m.versionDir(version), "memcached.log")
}

func (m *MemcachedRuntime) Stop(version string) error {
	// 先经伪控制台句柄终止本会话跟踪的进程（精确），再做端口兜底（覆盖孤儿/残留进程）。
	svcMgr.killTracked(RuntimeMemcached)
	// 端口兜底：杀掉占用默认端口且镜像确为 memcached_service.exe 的进程树
	stopByPort(memcachedDefaultPort, "memcached_service.exe")
	svcMgr.forget(RuntimeMemcached)
	return nil
}

func (m *MemcachedRuntime) Status(version string) ServiceStatus {
	port := memcachedDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeMemcached); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeMemcached)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "memcached_service.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseMemcachedVersion 解析 `memcached --version` 输出（如 "memcached 1.6.32"）。
func parseMemcachedVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "memcached") {
			continue
		}
		if strings.Contains(tok, ".") {
			return tok
		}
	}
	return ""
}
