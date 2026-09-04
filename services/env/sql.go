package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

// sqlFlavor 描述 MySQL 与 MariaDB 的差异（二进制名、默认端口、展示名）。
// 两者数据目录初始化（`--initialize-insecure`）与启动方式（前台 `mysqld`/`mariadbd`）一致，故共用 SQLRuntime。
type sqlFlavor struct {
	kind      Runtime
	display   string
	baseRel   string
	defPort   int
	serverBin string // 主二进制名：MySQL=mysqld.exe，MariaDB=mariadbd.exe
}

// SQLRuntime 管理便携 MySQL / MariaDB 运行时（官方 Windows zip，含 mysqld/mariadbd）。
// 实现 ServiceController：首次启动若数据目录缺失则惰性 initdb（`--initialize-insecure`，root 空密码），
// 再以 `mysqld --datadir`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
type SQLRuntime struct {
	flavor sqlFlavor
}

func NewMySQLRuntime() *SQLRuntime {
	return &SQLRuntime{flavor: sqlFlavor{
		kind: RuntimeMySQL, display: "MySQL", baseRel: "runtime/mysql",
		defPort: 3306, serverBin: "mysqld.exe",
	}}
}

func NewMariaDBRuntime() *SQLRuntime {
	return &SQLRuntime{flavor: sqlFlavor{
		kind: RuntimeMariaDB, display: "MariaDB", baseRel: "runtime/mariadb",
		defPort: 3306, serverBin: "mariadbd.exe",
	}}
}

func (s *SQLRuntime) Kind() Runtime                 { return s.flavor.kind }
func (s *SQLRuntime) DisplayName() string          { return s.flavor.display }
func (s *SQLRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (s *SQLRuntime) Recommended() []string        { return Versions(s.flavor.kind) }

// ExeFor 实现 RuntimeAdapter 接口：返回版本目录下的服务二进制（MySQL=mysqld / MariaDB=mariadbd）。
func (s *SQLRuntime) ExeFor(version string) string {
	return s.serverPath(version)
}

func (s *SQLRuntime) baseDir() string {
	return filepath.Join(platform.DefaultDataDir(), s.flavor.baseRel)
}

func (s *SQLRuntime) versionDir(version string) string {
	return filepath.Join(s.baseDir(), version)
}

// dataDir 数据目录（与 exe 目录分离，便于删除版本时清掉数据；initdb 落在此处）。
func (s *SQLRuntime) dataDir(version string) string {
	return filepath.Join(s.versionDir(version), "data")
}

// serverPath 返回版本目录下的服务二进制；MariaDB 优先 mariadbd.exe、MySQL 优先 mysqld.exe，
// 二者若存在其一即返回（兼容不同构建命名）。
func (s *SQLRuntime) serverPath(version string) string {
	dir := s.versionDir(version)
	cands := []string{s.flavor.serverBin}
	if s.flavor.kind == RuntimeMariaDB {
		cands = append(cands, "mysqld.exe")
	} else {
		cands = append(cands, "mariadbd.exe")
	}
	for _, c := range cands {
		p := filepath.Join(dir, "bin", c)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Join(dir, c)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, "bin", s.flavor.serverBin)
}

func (s *SQLRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(s.baseDir()); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(s.serverPath(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: s.versionDir(v)})
				dirs.record(filepath.Dir(s.serverPath(v)))
			}
		}
	}
	if p, err := exec.LookPath(strings.TrimSuffix(s.flavor.serverBin, ".exe")); err == nil {
		if v := parseSQLVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (s *SQLRuntime) DeleteVersion(version string) error {
	dir := s.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (s *SQLRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(s.flavor.kind)[0]
	}
	dir := s.versionDir(version)
	if _, err := os.Stat(s.serverPath(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog(s.flavor.display + " " + version + " 已安装: " + s.serverPath(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(s.flavor.kind, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 %s 下载源", s.flavor.display)
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-"+s.flavor.baseRel+"-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 "+s.flavor.display+" "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 " + s.flavor.display + " " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 %s 失败: %w", s.flavor.display, err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 "+s.flavor.display+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 " + s.flavor.display + " 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 %s 失败: %w", s.flavor.display, err)
	}
	if _, err := os.Stat(s.serverPath(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", s.serverPath(version))
	}
	if cb.OnLog != nil {
		cb.OnLog(s.flavor.display + " " + version + " 解压完成（首次启动将自动初始化数据目录）")
	}
	return nil
}

// ---- ServiceController ----

func (s *SQLRuntime) DefaultPort() int { return s.flavor.defPort }

// initDataDir 首次启动前惰性初始化数据目录（--initialize-insecure，root 空密码），失败返回错误。
func (s *SQLRuntime) initDataDir(version, serverExe, datadir string) error {
	if _, err := os.Stat(datadir); err == nil {
		return nil // 已初始化
	}
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	cmd := sysutil.Command(serverExe, "--initialize-insecure", "--datadir="+datadir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.E("[env][%s] initdb 失败 version=%s err=%v out=%s", s.flavor.kind, version, err, string(out))
		return fmt.Errorf("初始化数据目录失败: %w", err)
	}
	logger.I("[env][%s] initdb 完成 version=%s datadir=%s", s.flavor.kind, version, datadir)
	return nil
}

func (s *SQLRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := s.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 %s 版本", s.flavor.display)
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
			exe, wd = s.serverPath(version), s.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	running, _ := svcMgr.info(s.flavor.kind)
	if running != "" && running != version {
		return fmt.Errorf("%s 已在运行（%s），请先停止当前版本再启动 %s", s.flavor.display, running, version)
	}
	if running == "" && isPortOpen(s.flavor.defPort) {
		logger.W("[env][%s] Start 拒绝：端口 %d 已被占用", s.flavor.kind, s.flavor.defPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 %s", s.flavor.defPort, s.flavor.display)
	}
	// 首次启动惰性初始化数据目录（便携版本）
	if wd != "" && filepath.Dir(exe) != "" {
		datadir := s.dataDir(version)
		if err := s.initDataDir(version, exe, datadir); err != nil {
			return err
		}
	}
	if onLog != nil {
		onLog("启动 " + s.flavor.display + " " + version + " …")
	}
	return svcMgr.start(s.flavor.kind, version, exe, wd,
		[]string{"--datadir=" + s.dataDir(version), "--console"}, s.LogPath(version), onLog)
}

func (s *SQLRuntime) LogPath(version string) string {
	return filepath.Join(s.versionDir(version), string(s.flavor.kind)+".log")
}

func (s *SQLRuntime) Stop(version string) error {
	port := s.flavor.defPort
	// 1) 尝试原生优雅关闭（mysqladmin shutdown / mariadb-admin shutdown），覆盖孤儿/多版本
	for _, ins := range s.InstalledVersions() {
		admin := filepath.Join(filepath.Dir(s.serverPath(ins.Version)), strings.TrimSuffix(s.flavor.serverBin, "d.exe")+"admin.exe")
		if _, err := os.Stat(admin); err == nil {
			cmd := sysutil.Command(admin, "-uroot", "-h", "127.0.0.1", "-P", fmt.Sprintf("%d", port), "shutdown")
			_ = cmd.Run()
		}
	}
	// 2) 端口兜底：杀掉占用默认端口且镜像确为该服务二进制的进程树
	stopByPort(port, s.flavor.serverBin)
	svcMgr.forget(s.flavor.kind)
	return nil
}

func (s *SQLRuntime) Status(version string) ServiceStatus {
	port := s.flavor.defPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(s.flavor.kind); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(s.flavor.kind)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), s.flavor.serverBin) {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseSQLVersion 解析 `mysqld/mariadbd --version` 输出（如 "mysqld  Ver 8.4.3 ..." 或 "mariadbd  Ver 11.5.2-MariaDB"）。
func parseSQLVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "Ver") {
			continue
		}
		// 去掉 -MariaDB / -Win64 等后缀
		if i := strings.IndexByte(tok, '-'); i > 0 && strings.Contains(tok, ".") {
			return tok[:i]
		}
		if strings.Contains(tok, ".") {
			return tok
		}
	}
	return ""
}
