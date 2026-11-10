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
	"time"

	"quickdock/internal/aria2"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const aria2BaseRel = "runtime/aria2"

// reAria2Version `aria2c --version` 输出形如 "aria2 version 1.37.0"。
var reAria2Version = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// Aria2Runtime 托管 aria2c 可执行文件：单版本语义（解压到 runtime/aria2），
// 以 daemon 形态常驻，QuickDock 通过 JSON-RPC 发指令。文件字节由 aria2c 自己写盘。
type Aria2Runtime struct {
	baseDir string
}

func NewAria2Runtime() *Aria2Runtime {
	return &Aria2Runtime{baseDir: filepath.Join(platform.DefaultDataDir(), aria2BaseRel)}
}

func (a *Aria2Runtime) Kind() Runtime       { return RuntimeAria2 }
func (a *Aria2Runtime) DisplayName() string { return DisplayName(RuntimeAria2) }
func (a *Aria2Runtime) DetectArgs() []string {
	return []string{"--version"}
}

func (a *Aria2Runtime) ParseVersion(out string) (string, error) {
	if v := reAria2Version.FindString(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 aria2 版本")
}

// 官方 Windows 便携 zip 无需额外 DLL。macOS/Linux 走系统包管理器（brew/apt）自行安装，
// 本适配器只做探测与接管，不托管下载，故 SupportedPlatforms 只声明 windows。
func (a *Aria2Runtime) SupportedPlatforms() []string { return []string{"windows"} }
func (a *Aria2Runtime) Recommended() []string        { return Versions(RuntimeAria2) }

func (a *Aria2Runtime) exeName() string {
	if runtime.GOOS == "windows" {
		return "aria2c.exe"
	}
	return "aria2c"
}

// ExeFor 单版本语义：忽略 version，目录恒为 runtime/aria2。
func (a *Aria2Runtime) ExeFor(version string) string {
	return filepath.Join(a.baseDir, a.exeName())
}

func (a *Aria2Runtime) LogPath(version string) string {
	return filepath.Join(a.baseDir, "aria2.log")
}

func (a *Aria2Runtime) InstalledVersions() []Install {
	var out []Install
	if _, err := os.Stat(a.ExeFor("")); err == nil {
		v := reAria2Version.FindString(RunVersion(a.ExeFor(""), "--version"))
		if v == "" {
			v = "unknown"
			if b, e := os.ReadFile(filepath.Join(a.baseDir, "VERSION")); e == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					v = s
				}
			}
		}
		out = append(out, Install{Version: v, Scope: "portable", Path: a.baseDir})
	}
	if p, err := exec.LookPath("aria2c"); err == nil {
		v := reAria2Version.FindString(RunVersion(p, "--version"))
		if v == "" {
			return out
		}
		// 便携版被写进 PATH 时 LookPath 会命中自己，别重复登记成 system。
		for _, ins := range out {
			if strings.EqualFold(filepath.Clean(filepath.Dir(ins.Path)), filepath.Clean(filepath.Dir(p))) {
				return out
			}
		}
		out = append(out, Install{Version: v, Scope: "system", Path: p})
	}
	return out
}

func (a *Aria2Runtime) DeleteVersion(version string) error {
	if _, err := os.Stat(a.baseDir); err != nil {
		return fmt.Errorf("未安装 aria2")
	}
	return os.RemoveAll(a.baseDir)
}

// Install 从官方 Release 下载 Windows 便携 zip（约 2.4 MB）并解压。
// 归档顶层是单一目录 aria2-<version>-win-64bit-build1/，Extract 会自动剥离，
// 解压后 aria2c.exe 直接落在 runtime/aria2 下。
func (a *Aria2Runtime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeAria2)[0]
	}
	urls := CandidateURLs(RuntimeAria2, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 aria2 下载源（当前平台暂未支持）")
	}
	// 下载包放进数据目录而不是 os.TempDir()：系统盘写满时连 2.4MB 临时文件都建不出来，
	// 安装会毫无提示地失败。baseDir 与解压暂存区同盘，moveDirContents 不需要跨盘搬运。
	if err := os.MkdirAll(a.baseDir, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(a.baseDir, ".aria2-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 aria2 "+version+"（约 2.4 MB）…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 aria2 " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 aria2 失败: %w", err)
	}
	defer os.Remove(zipPath)

	staging := filepath.Join(filepath.Dir(a.baseDir), ".aria2-staging")
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 aria2…")
	}
	if err := Extract(zipPath, staging); err != nil {
		return fmt.Errorf("解压 aria2 失败: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, a.exeName())); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", a.exeName())
	}
	if err := os.MkdirAll(a.baseDir, 0o755); err != nil {
		return err
	}
	if err := moveDirContents(staging, a.baseDir); err != nil {
		return fmt.Errorf("安装 aria2 失败: %w", err)
	}
	_ = os.WriteFile(filepath.Join(a.baseDir, "VERSION"), []byte(version), 0o644)
	if cfg, err := aria2.LoadConfig(); err == nil {
		cfg.Version = version
		_ = aria2.SaveConfig(cfg)
	}
	if cb.OnLog != nil {
		cb.OnLog("aria2 " + version + " 安装完成")
	}
	return nil
}

// ---- ServiceController ----

func (a *Aria2Runtime) DefaultPort() int {
	if c, err := aria2.LoadConfig(); err == nil {
		return c.Port
	}
	return aria2.PortScanStart
}

func (a *Aria2Runtime) Start(ctx context.Context, version string, onLog func(string)) error {
	exe := a.ExeFor("")
	wd := a.baseDir
	if _, err := os.Stat(exe); err != nil {
		// 系统里已装 aria2（brew/apt/winget）时也能直接接管，不强求便携版。
		if p, e := exec.LookPath("aria2c"); e == nil {
			exe, wd = p, filepath.Dir(p)
		} else {
			return fmt.Errorf("请先安装 aria2")
		}
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return err
	}
	if err := aria2.EnsureSessionFile(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return err
	}
	if isPortOpen(cfg.Port) {
		// 大文件把盘写满 / 刷 write cache 阻塞时，aria2 的单线程事件循环会卡在磁盘 I/O：
		// 端口仍在 Listen，但所有 RPC 都超时（日志表现为 timeout awaiting response headers）。
		// 这种「假活」实例会让看门狗误判为运行中、重启又一律被端口占用挡回来，必须区分处理。
		healthy, stuck := daemonProbe(cfg)
		if healthy {
			// 健康的 daemon（另一个 QuickDock 实例刚拉起的、或用户自己启动的）直接复用。
			// 早期实现无条件报「端口已占用」+ 孤儿回收杀进程，会造成「刚启动 1 秒就被杀
			// → connectex: actively refused」的重启风暴。
			logger.I("[env][aria2] 端口 %d 上已有健康 daemon，直接复用", cfg.Port)
			return nil
		}
		if !stuck {
			// 有服务在响应、但不是我们配置里的 aria2（secret 不符 / 别的 HTTP 服务）：
			// 保持原有的「端口被占用」语义，绝不能去杀用户自己的进程。
			return fmt.Errorf("端口 %d 已被其它服务占用，且不是当前配置的 aria2 daemon", cfg.Port)
		}
		logger.W("[env][aria2] 端口 %d 上的 daemon 无响应，先清理再重启", cfg.Port)
		stopByPort(cfg.Port, "aria2c.exe")
		for i := 0; i < 10 && isPortOpen(cfg.Port); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		if isPortOpen(cfg.Port) {
			return fmt.Errorf("端口 %d 上的无响应 aria2 进程未能清理，请手动结束 aria2c.exe", cfg.Port)
		}
	}
	args := aria2.DaemonArgs(cfg)
	logger.I("[env][aria2] Start exe=%s port=%d dir=%s", exe, cfg.Port, cfg.DownloadDir)
	if onLog != nil {
		onLog(fmt.Sprintf("启动 aria2 daemon（127.0.0.1:%d）…", cfg.Port))
	}
	return svcMgr.start(RuntimeAria2, version, exe, wd, args, a.LogPath(version), onLog)
}

// daemonProbe 用一次短超时 RPC 判断端口上实例的真实状态。只看 TCP 端口会把
// 「卡死但仍 Listen」的实例当成健康：大文件下载时 aria2 单线程卡在写盘，
// RPC 全超时而端口照常接受连接，是长时间起不来的关键误判。
//   - healthy: RPC 正常，直接复用
//   - stuck:   端口在但网络层无响应，可安全清理后重启
//   - 两者都否: 有服务响应但鉴权/协议不符，不能动（可能是用户自己的 aria2）
func daemonProbe(cfg *aria2.Config) (healthy, stuck bool) {
	c := aria2.NewClient(cfg)
	c.HTTP.Timeout = 1500 * time.Millisecond
	_, err := c.GetVersion()
	if err == nil {
		return true, false
	}
	return false, aria2.IsTransportError(err)
}

func (a *Aria2Runtime) Stop(version string) error {
	if cfg, err := aria2.LoadConfig(); err == nil {
		stopByPort(cfg.Port, "aria2c.exe")
	}
	svcMgr.forget(RuntimeAria2)
	return nil
}

func (a *Aria2Runtime) Status(version string) ServiceStatus {
	port := aria2.PortScanStart
	if cfg, err := aria2.LoadConfig(); err == nil {
		port = cfg.Port
	}
	st := ServiceStatus{Running: false, Port: port}
	r := Probe(RuntimeRunningProbe{
		Kind:     RuntimeAria2,
		Port:     port,
		ExeNames: []string{"aria2c.exe", "aria2c"},
		Installs: a.InstalledVersions(),
	})
	if !r.Running {
		return st
	}
	st.Running = true
	st.PID = r.PID
	st.Version = r.Version
	return st
}

// 编译期断言：环境管理的服务契约必须完整实现，漏一个方法会在编译期暴露。
var _ RuntimeAdapter = (*Aria2Runtime)(nil)
var _ ServiceController = (*Aria2Runtime)(nil)
