package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	// mu 串行化同一运行时的 Start（含初始化）。否则两次 Start 并发时，
	// 第一次的 mysqld --initialize 仍锁着数据目录文件，第二次会尝试清空重建并删掉前者正在用的文件，
	// 导致初始化崩溃（exit 0x80000003）。TryLock 在并发时立即返回，不阻塞等待。
	mu sync.Mutex
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
func (s *SQLRuntime) DetectArgs() []string          { return []string{"--version"} }
func (s *SQLRuntime) ParseVersion(out string) (string, error) {
	if v := parseSQLVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(s.flavor.kind))
}
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

// DataDir 返回 SQL（MySQL/MariaDB）数据目录（卸载时可选清理）。
func (s *SQLRuntime) DataDir(version string) string { return s.dataDir(version) }

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

// isInitialized 判断数据目录是否已完整初始化（含系统库）。
// 不能仅判断 datadir 或 mysql/ 是否存在：初始化中途崩溃会留下 mysql/ 子目录及部分 InnoDB 文件，
// 但 performance_schema / sys 等后续系统库尚未建出，此时拉起服务器会报数据损坏（MY-012960）直接 abort。
// 故要求 mysql/ 与至少一个后续系统库目录同时存在，才视为初始化完成。
func (s *SQLRuntime) isInitialized(datadir string) bool {
	if _, err := os.Stat(filepath.Join(datadir, "mysql")); err != nil {
		return false // 连系统库目录都没有
	}
	if _, err := os.Stat(filepath.Join(datadir, "performance_schema")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(datadir, "sys")); err == nil {
		return true
	}
	return false
}

// initCommand 返回数据目录初始化所用的可执行文件与参数。
// MariaDB 优先 mariadb-install-db.exe（Windows 官方推荐、最可靠；实测 mariadbd --initialize-insecure 在 MariaDB 11.x 上静默失败且不建 mysql 库）；
// 缺失时回退到 mysqld/mariadbd --initialize-insecure。注意 mariadb-install-db.exe 不识别 --auth-root-authentication-method 等参数，仅传 --datadir 即可。
func (s *SQLRuntime) initCommand(serverExe, datadir string) (string, []string) {
	if s.flavor.kind == RuntimeMariaDB {
		if p := filepath.Join(filepath.Dir(serverExe), "mariadb-install-db.exe"); fileExists(p) {
			return p, []string{"--datadir=" + datadir}
		}
	}
	return serverExe, []string{"--initialize-insecure", "--datadir=" + datadir}
}

// initDataDir 首次启动前惰性初始化数据目录（root 空密码），失败返回错误。
func (s *SQLRuntime) initDataDir(version, serverExe, datadir string) error {
	if s.isInitialized(datadir) {
		return nil
	}
	// datadir 存在但缺少系统库（脏数据）→ 清空重建。便携场景数据目录仅含系统库，无用户数据，清空安全。
	if fi, err := os.Stat(datadir); err == nil && fi.IsDir() {
		if entries, err := os.ReadDir(datadir); err == nil && len(entries) > 0 {
			logger.W("[env][%s] initdb 发现 datadir 非空但缺少系统库，清空重建 version=%s datadir=%s", s.flavor.kind, version, datadir)
			// 先解除可能的占用：首次初始化中途崩溃、或上一次残留的 mysqld/mariadbd 仍锁着
			// #ib_16384_0.dblwr 等文件，直接 RemoveAll 会因「文件被另一进程占用」失败。
			// 仅杀该版本目录下的镜像，不影响其他版本或独立实例。
			killSQLHolders(s.versionDir(version), s.flavor.serverBin)
			stopByPort(s.flavor.defPort, s.flavor.serverBin)
			var rmErr error
			for i := 0; i < 5; i++ {
				if rmErr = os.RemoveAll(datadir); rmErr == nil {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			if rmErr != nil {
				return fmt.Errorf("清理脏数据目录失败: %w", rmErr)
			}
		}
	}
	if err := os.MkdirAll(datadir, 0755); err != nil {
		return err
	}
	initExe, initArgs := s.initCommand(serverExe, datadir)
	cmd := sysutil.Command(initExe, initArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.E("[env][%s] initdb 失败 version=%s err=%v out=%s", s.flavor.kind, version, err, string(out))
		return fmt.Errorf("初始化数据目录失败: %w\n%s", err, string(out))
	}
	logger.I("[env][%s] initdb 完成 version=%s datadir=%s", s.flavor.kind, version, datadir)
	return nil
}

func (s *SQLRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	// 串行化同运行时：初始化（mysqld --initialize，耗时数秒）期间进程尚未记入 svcMgr，
	// 若无此锁，第二次 Start 会插进来清空数据目录，与正在跑的初始化互相破坏。
	if !s.mu.TryLock() {
		return fmt.Errorf("%s 正在启动或初始化中，请稍候再试", s.flavor.display)
	}
	defer s.mu.Unlock()
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
	logger.I("[env][%s] Start version=%s exe=%s wd=%s", s.flavor.kind, version, exe, wd)
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
			if onLog != nil {
				onLog(s.flavor.display + " 初始化数据目录失败: " + err.Error())
			}
			return err
		}
	}
	if onLog != nil {
		onLog("启动 " + s.flavor.display + " " + version + " …")
	}
	if err := svcMgr.start(s.flavor.kind, version, exe, wd,
		[]string{"--datadir=" + s.dataDir(version), "--console"}, s.LogPath(version), onLog); err != nil {
		if onLog != nil {
			onLog(s.flavor.display + " 启动失败: " + err.Error())
		}
		return err
	}
	return nil
}

func (s *SQLRuntime) LogPath(version string) string {
	return filepath.Join(s.versionDir(version), string(s.flavor.kind)+".log")
}

func (s *SQLRuntime) Stop(version string) error {
	logger.I("[env][%s] Stop version=%s", s.flavor.kind, version)
	port := s.flavor.defPort
	// 1) 尝试原生优雅关闭（mysqladmin shutdown / mariadb-admin shutdown），覆盖孤儿/多版本
	for _, ins := range s.InstalledVersions() {
		admin := filepath.Join(filepath.Dir(s.serverPath(ins.Version)), strings.TrimSuffix(s.flavor.serverBin, "d.exe")+"admin.exe")
		if _, err := os.Stat(admin); err == nil {
			logger.I("[env][%s] Stop 调用 %s shutdown (port=%d)", s.flavor.kind, filepath.Base(admin), port)
			cmd := sysutil.Command(admin, "-uroot", "-h", "127.0.0.1", "-P", fmt.Sprintf("%d", port), "shutdown")
			if err := cmd.Run(); err != nil {
				logger.W("[env][%s] Stop %s shutdown 失败（将走端口兜底）: %v", s.flavor.kind, filepath.Base(admin), err)
			}
		}
	}
	// 2) 端口兜底：杀掉占用默认端口且镜像确为该服务二进制的进程树
	stopByPort(port, s.flavor.serverBin)
	svcMgr.forget(s.flavor.kind)
	logger.I("[env][%s] Stop 完成", s.flavor.kind)
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
