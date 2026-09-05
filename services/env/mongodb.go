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

const mongoBaseRel = "runtime/mongodb"

const mongoDefaultPort = 27017

// MongoRuntime 管理便携 MongoDB 运行时（官方 Windows zip，含 bin/mongod.exe）。
// 实现 ServiceController：以 `mongod --dbpath`（前台阻塞）拉起；
// ⚠️ MongoDB 7.0 不会自动创建 data 目录（老版本会），故 Start 前必须显式 MkdirAll；
// 由 svcMgr 记录 PID 并捕获日志。
type MongoRuntime struct {
	baseDir string
}

func NewMongoRuntime() *MongoRuntime {
	return &MongoRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), mongoBaseRel)}
}

func (m *MongoRuntime) Kind() Runtime                 { return RuntimeMongoDB }
func (m *MongoRuntime) DetectArgs() []string          { return []string{"--version"} }
func (m *MongoRuntime) ParseVersion(out string) (string, error) {
	if v := parseMongoVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMongoDB))
}
func (m *MongoRuntime) DisplayName() string          { return DisplayName(RuntimeMongoDB) }
func (m *MongoRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (m *MongoRuntime) Recommended() []string        { return Versions(RuntimeMongoDB) }

func (m *MongoRuntime) versionDir(version string) string {
	return filepath.Join(m.baseDir, version)
}

func (m *MongoRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "bin", "mongod.exe")
	}
	return filepath.Join(m.versionDir(version), "bin", "mongod")
}

func (m *MongoRuntime) dataDir(version string) string {
	return filepath.Join(m.versionDir(version), "data")
}

// DataDir 返回 MongoDB 数据目录（卸载时可选清理）。
func (m *MongoRuntime) DataDir(version string) string { return m.dataDir(version) }

func (m *MongoRuntime) InstalledVersions() []Install {
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
	if p, err := exec.LookPath("mongod"); err == nil {
		if v := parseMongoVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (m *MongoRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (m *MongoRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMongoDB)[0]
	}
	dir := m.versionDir(version)
	if _, err := os.Stat(m.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("MongoDB " + version + " 已安装: " + m.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeMongoDB, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 MongoDB 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-mongodb-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 MongoDB "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 MongoDB " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 MongoDB 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 MongoDB…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 MongoDB 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 MongoDB 失败: %w", err)
	}
	if _, err := os.Stat(m.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", m.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("MongoDB " + version + " 解压完成（首次启动自动创建数据目录）")
	}
	return nil
}

// ---- ServiceController ----

func (m *MongoRuntime) DefaultPort() int { return mongoDefaultPort }

func (m *MongoRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := m.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 MongoDB 版本")
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
	logger.I("[env][mongodb] Start version=%s exe=%s wd=%s", version, exe, wd)
	running, _ := svcMgr.info(RuntimeMongoDB)
	if running != "" && running != version {
		return fmt.Errorf("MongoDB 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(mongoDefaultPort) {
		logger.W("[env][mongodb] Start 拒绝：端口 %d 已被占用", mongoDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 MongoDB", mongoDefaultPort)
	}
	if onLog != nil {
		onLog("启动 MongoDB " + version + " …")
	}
	// MongoDB 7.0 不会自动创建 dbpath 目录（老版本会），必须显式建好，否则启动报 NonExistentPath → exitCode 100。
	if err := os.MkdirAll(m.dataDir(version), 0755); err != nil {
		return fmt.Errorf("创建 MongoDB 数据目录失败: %w", err)
	}
	// 注意：logPath 传空。mongod 自己通过 --logpath 管理 mongodb.log，
	// 若 QuickDock 再用 os.OpenFile 打开同一文件会与 mongod 抢句柄
	// （mongod 以更严格的共享标志打开 → SHARING_VIOLATION → "Failed to open" 启动失败）。
	// 实时日志仍由 onLog 通过管道回调，不依赖此落盘文件。
	if err := svcMgr.start(RuntimeMongoDB, version, exe, wd,
		[]string{"--dbpath=" + m.dataDir(version), "--logpath=" + m.LogPath(version), "--logappend"}, "", onLog); err != nil {
		if onLog != nil {
			onLog("MongoDB 启动失败: " + err.Error())
		}
		return err
	}
	return nil
}

func (m *MongoRuntime) LogPath(version string) string {
	return filepath.Join(m.versionDir(version), "mongodb.log")
}

// LogGet 读取某版本运行日志尾部（实现 LogProvider）。日志由 mongod 经 --logpath 管理，路径即 LogPath。
func (m *MongoRuntime) LogGet(version string) (string, error) {
	return readLogTail(m.LogPath(version))
}

func (m *MongoRuntime) Stop(version string) error {
	logger.I("[env][mongodb] Stop version=%s", version)
	stopByPort(mongoDefaultPort, "mongod.exe")
	svcMgr.forget(RuntimeMongoDB)
	logger.I("[env][mongodb] Stop 完成")
	return nil
}

func (m *MongoRuntime) Status(version string) ServiceStatus {
	port := mongoDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeMongoDB); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeMongoDB)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "mongod.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseMongoVersion 解析 `mongod --version` 输出（如 "db version v7.0.14"）。
func parseMongoVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "v") {
			return strings.TrimPrefix(tok, "v")
		}
	}
	return ""
}
