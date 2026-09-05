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

const postgresBaseRel = "runtime/postgresql"

const postgresDefaultPort = 5432

// PostgresRuntime 管理便携 PostgreSQL 运行时（EnterpriseDB Windows 二进制包，含 bin/postgres + bin/initdb）。
// 实现 ServiceController：首次启动若数据目录缺失则惰性 initdb（`-U postgres --auth=trust`），
// 再以 `postgres -D`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
type PostgresRuntime struct {
	baseDir string
}

func NewPostgresRuntime() *PostgresRuntime {
	return &PostgresRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), postgresBaseRel)}
}

func (p *PostgresRuntime) Kind() Runtime                 { return RuntimePostgreSQL }
func (p *PostgresRuntime) DetectArgs() []string          { return []string{"--version"} }
func (p *PostgresRuntime) ParseVersion(out string) (string, error) {
	if v := parsePostgresVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimePostgreSQL))
}
func (p *PostgresRuntime) DisplayName() string          { return DisplayName(RuntimePostgreSQL) }
func (p *PostgresRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (p *PostgresRuntime) Recommended() []string        { return Versions(RuntimePostgreSQL) }

func (p *PostgresRuntime) versionDir(version string) string {
	return filepath.Join(p.baseDir, version)
}

func (p *PostgresRuntime) ExeFor(version string) string {
	return filepath.Join(p.versionDir(version), "bin", "postgres.exe")
}

func (p *PostgresRuntime) dataDir(version string) string {
	return filepath.Join(p.versionDir(version), "data")
}

// DataDir 返回 PostgreSQL 数据目录（卸载时可选清理）。
func (p *PostgresRuntime) DataDir(version string) string { return p.dataDir(version) }

func (p *PostgresRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(p.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(p.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: p.versionDir(v)})
				dirs.record(filepath.Dir(p.ExeFor(v)))
			}
		}
	}
	if exe, err := exec.LookPath("postgres"); err == nil {
		if v := parsePostgresVersion(RunVersion(exe, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(exe) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: exe})
		}
	}
	return out
}

func (p *PostgresRuntime) DeleteVersion(version string) error {
	dir := p.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (p *PostgresRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimePostgreSQL)[0]
	}
	dir := p.versionDir(version)
	if _, err := os.Stat(p.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("PostgreSQL " + version + " 已安装: " + p.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimePostgreSQL, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 PostgreSQL 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-postgresql-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 PostgreSQL "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 PostgreSQL " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 PostgreSQL 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 PostgreSQL…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 PostgreSQL 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 PostgreSQL 失败: %w", err)
	}
	if _, err := os.Stat(p.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", p.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("PostgreSQL " + version + " 解压完成（首次启动将自动 initdb 初始化数据目录）")
	}
	return nil
}

// ---- ServiceController ----

func (p *PostgresRuntime) DefaultPort() int { return postgresDefaultPort }

func (p *PostgresRuntime) initDataDir(version, datadir string) error {
	if _, err := os.Stat(datadir); err == nil {
		return nil
	}
	if err := os.MkdirAll(datadir, 0700); err != nil {
		return err
	}
	initdb := filepath.Join(p.versionDir(version), "bin", "initdb.exe")
	cmd := sysutil.Command(initdb, "-D", datadir, "-U", "postgres", "--auth=trust")
	out, err := cmd.CombinedOutput()
	if err != nil {
		logger.E("[env][postgresql] initdb 失败 version=%s err=%v out=%s", version, err, string(out))
		return fmt.Errorf("初始化数据目录失败: %w", err)
	}
	logger.I("[env][postgresql] initdb 完成 version=%s datadir=%s", version, datadir)
	return nil
}

func (p *PostgresRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := p.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 PostgreSQL 版本")
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
			exe, wd = p.ExeFor(version), p.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	logger.I("[env][postgresql] Start version=%s exe=%s wd=%s", version, exe, wd)
	running, _ := svcMgr.info(RuntimePostgreSQL)
	if running != "" && running != version {
		return fmt.Errorf("PostgreSQL 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(postgresDefaultPort) {
		logger.W("[env][postgresql] Start 拒绝：端口 %d 已被占用", postgresDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 PostgreSQL", postgresDefaultPort)
	}
	if wd != "" {
		if err := p.initDataDir(version, p.dataDir(version)); err != nil {
			if onLog != nil {
				onLog("PostgreSQL 初始化数据目录失败: " + err.Error())
			}
			return err
		}
	}
	dataDir := p.dataDir(version)
	logPath := p.LogPath(version)
	if isElevated() {
		// 提权（完整管理员）令牌：postgres 直接启动会被其管理员检测拒绝；
		// 但本进程具备创建 Windows 服务的权限，故注册为服务、由 SCM 以 LocalSystem（非 Administrators 成员）拉起。
		if onLog != nil {
			onLog("检测到以管理员身份运行，PostgreSQL 将以 Windows 服务方式启动…")
		}
		if err := p.startService(version, dataDir, logPath, onLog); err != nil {
			return err
		}
		return nil
	}
	// 过滤令牌 / 标准用户：Administrators SID 为 deny-only，postgres 的 CheckTokenMembership 返回 FALSE，
	// 管理员检测通过，直接以前台子进程拉起即可（无需服务、也不依赖降权令牌特权）。
	if onLog != nil {
		onLog("启动 PostgreSQL " + version + " …")
	}
	if err := p.startDirect(version, exe, wd, dataDir, logPath, onLog); err != nil {
		return err
	}
	return nil
}

// startDirect 以普通子进程方式拉起 PostgreSQL（继承当前进程令牌）。
// 仅用于非提权场景：过滤令牌下 Administrators 为 deny-only，postgres 不再拒绝启动。
// 日志由 postgres 输出到 stderr，经 svcMgr.start 捕获并写入 logPath（前端读取该文件展示就绪状态）。
func (p *PostgresRuntime) startDirect(version, exe, wd, dataDir, logPath string, onLog func(string)) error {
	return svcMgr.start(RuntimePostgreSQL, version, exe, wd, []string{"-D", dataDir}, logPath, onLog)
}

// startService 以 Windows 服务方式拉起 PostgreSQL（仅用于「提权/完整管理员」场景）。
// 提权令牌下 postgres 直接启动会被其管理员检测拒绝；但提权进程具备创建 Windows 服务的权限，
// 注册为服务后由 SCM 以 LocalSystem（非 Administrators 成员）身份启动，postgres 不再拒绝。
// 日志由 postgres 直接写入 logPath 文件（经由 pg_ctl -l 指定）。
func (p *PostgresRuntime) startService(version, dataDir, logPath string, onLog func(string)) error {
	name := "QuickDock_PgSQL_" + version
	pgctl := filepath.Join(p.versionDir(version), "bin", "pg_ctl.exe")
	// 清理同名旧服务（上次未正常停止 / QuickDock 崩溃残留）。忽略错误：可能不存在。
	_ = sysutil.Command(pgctl, "stop", "-N", name, "-m", "fast", "-W").Run()
	_ = sysutil.Command(pgctl, "unregister", "-N", name).Run()
	// 注册为 Windows 服务：默认以 LocalSystem 运行（非 Administrators 成员），
	// PostgreSQL 不再拒绝启动；日志写入 logPath 文件。
	reg := sysutil.Command(pgctl, "register", "-N", name, "-D", dataDir, "-l", logPath)
	reg.Dir = p.versionDir(version)
	if out, err := reg.CombinedOutput(); err != nil {
		return fmt.Errorf("注册 PostgreSQL 服务失败: %w (%s)", err, string(out))
	}
	// 非阻塞启动（-W）：服务就绪由前端端口探测判断；启动失败会记到 logPath。
	start := sysutil.Command(pgctl, "start", "-N", name, "-W")
	start.Dir = p.versionDir(version)
	if out, err := start.CombinedOutput(); err != nil {
		if onLog != nil {
			onLog("PostgreSQL 启动失败: " + err.Error())
		}
		logger.E("[env][postgresql] pg_ctl start 失败: %v out=%s", err, string(out))
		_ = sysutil.Command(pgctl, "unregister", "-N", name).Run()
		return fmt.Errorf("启动 PostgreSQL 服务失败: %w (%s)", err, string(out))
	}
	logger.I("[env][postgresql] 服务已启动 name=%s version=%s", name, version)
	return nil
}

func (p *PostgresRuntime) LogPath(version string) string {
	return filepath.Join(p.versionDir(version), "postgresql.log")
}

func (p *PostgresRuntime) Stop(version string) error {
	logger.I("[env][postgresql] Stop version=%s", version)
	if version == "" {
		if ins := p.InstalledVersions(); len(ins) > 0 {
			version = ins[0].Version
		}
	}
	// 1) 停止并卸载 QuickDock 注册的服务（优雅关闭）
	if version != "" {
		name := "QuickDock_PgSQL_" + version
		pgctl := filepath.Join(p.versionDir(version), "bin", "pg_ctl.exe")
		if _, err := os.Stat(pgctl); err == nil {
			stop := sysutil.Command(pgctl, "stop", "-N", name, "-m", "fast", "-W")
			stop.Dir = p.versionDir(version)
			if out, err := stop.CombinedOutput(); err != nil {
				logger.W("[env][postgresql] pg_ctl stop 失败（将走端口兜底）: %v %s", err, string(out))
			} else {
				logger.I("[env][postgresql] pg_ctl stop 成功 version=%s", version)
			}
			unreg := sysutil.Command(pgctl, "unregister", "-N", name)
			unreg.Dir = p.versionDir(version)
			if out, err := unreg.CombinedOutput(); err != nil {
				logger.W("[env][postgresql] pg_ctl unregister 失败: %v %s", err, string(out))
			} else {
				logger.I("[env][postgresql] pg_ctl unregister 成功 version=%s", version)
			}
		}
	}
	// 2) 端口兜底：杀掉占用默认端口且镜像确为 postgres.exe 的进程树（覆盖外部实例/残留）
	stopByPort(postgresDefaultPort, "postgres.exe")
	svcMgr.forget(RuntimePostgreSQL)
	logger.I("[env][postgresql] Stop 完成")
	return nil
}

func (p *PostgresRuntime) Status(version string) ServiceStatus {
	port := postgresDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimePostgreSQL); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimePostgreSQL)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "postgres.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parsePostgresVersion 解析 `postgres --version` 输出（如 "postgres (PostgreSQL) 16.4"）。
func parsePostgresVersion(out string) string {
	if i := strings.Index(out, "PostgreSQL"); i >= 0 {
		rest := strings.TrimSpace(out[i+len("PostgreSQL"):])
		if sp := strings.Fields(rest); len(sp) > 0 {
			return sp[0]
		}
	}
	return ""
}
