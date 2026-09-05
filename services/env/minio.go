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

const minioBaseRel = "runtime/minio"

const minioDefaultPort = 9000    // S3 API；Console 固定 9001
const minioConsolePort = 9001

var minioRe = regexp.MustCompile(`RELEASE\.[\dTZ.-]+`)

// MinioRuntime 管理便携 MinIO 运行时（S3 兼容对象存储，单文件 minio.exe）。
// MinIO 为滚动发布（RELEASE 日期版本），无 semver；版本列表仅占位 "latest"，
// 安装后由 `minio --version` 探测真实 RELEASE 号。实现 ServiceController。
type MinioRuntime struct {
	baseDir string
}

func NewMinioRuntime() *MinioRuntime {
	return &MinioRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), minioBaseRel)}
}

func (m *MinioRuntime) Kind() Runtime                 { return RuntimeMinIO }
func (m *MinioRuntime) DetectArgs() []string          { return []string{"--version"} }
func (m *MinioRuntime) ParseVersion(out string) (string, error) {
	if v := parseMinioVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMinIO))
}
func (m *MinioRuntime) DisplayName() string          { return DisplayName(RuntimeMinIO) }
func (m *MinioRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (m *MinioRuntime) Recommended() []string        { return Versions(RuntimeMinIO) }

func (m *MinioRuntime) versionDir(version string) string {
	return filepath.Join(m.baseDir, version)
}

func (m *MinioRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "minio.exe")
	}
	return filepath.Join(m.versionDir(version), "minio")
}

func (m *MinioRuntime) dataDir(version string) string {
	return filepath.Join(m.versionDir(version), "data")
}

// DataDir 返回 MinIO 数据目录（卸载时可选清理）。
func (m *MinioRuntime) DataDir(version string) string { return m.dataDir(version) }

func (m *MinioRuntime) InstalledVersions() []Install {
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
	if p, err := exec.LookPath("minio"); err == nil {
		if v := parseMinioVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (m *MinioRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (m *MinioRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMinIO)[0]
	}
	dir := m.versionDir(version)
	if _, err := os.Stat(m.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("MinIO " + version + " 已安装: " + m.ExeFor(version))
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	urls := CandidateURLs(RuntimeMinIO, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 MinIO 下载源")
	}
	// MinIO 为单文件 exe，直接下载到目标路径（无需解压）。
	exePath := m.ExeFor(version)
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 MinIO "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 MinIO " + version + "…")
	}
	if err := Download(ctx, exePath, urls, cb.OnProgress); err != nil {
		os.Remove(exePath)
		return fmt.Errorf("下载 MinIO 失败: %w", err)
	}
	if cb.OnLog != nil {
		cb.OnLog("MinIO " + version + " 下载完成（单文件）")
	}
	return nil
}

// ---- ServiceController ----

func (m *MinioRuntime) DefaultPort() int { return minioDefaultPort }

func (m *MinioRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := m.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 MinIO 版本")
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
	running, _ := svcMgr.info(RuntimeMinIO)
	if running != "" && running != version {
		return fmt.Errorf("MinIO 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(minioDefaultPort) {
		logger.W("[env][minio] Start 拒绝：端口 %d 已被占用", minioDefaultPort)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 MinIO", minioDefaultPort)
	}
	if onLog != nil {
		onLog("启动 MinIO " + version + " …")
	}
	return svcMgr.start(RuntimeMinIO, version, exe, wd,
		[]string{"server", m.dataDir(version), "--address", "127.0.0.1:" + fmt.Sprintf("%d", minioDefaultPort), "--console-address", "127.0.0.1:" + fmt.Sprintf("%d", minioConsolePort)}, m.LogPath(version), onLog)
}

func (m *MinioRuntime) LogPath(version string) string {
	return filepath.Join(m.versionDir(version), "minio.log")
}

func (m *MinioRuntime) Stop(version string) error {
	stopByPort(minioDefaultPort, "minio.exe")
	svcMgr.forget(RuntimeMinIO)
	return nil
}

func (m *MinioRuntime) Status(version string) ServiceStatus {
	port := minioDefaultPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeMinIO); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeMinIO)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), "minio.exe") {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// parseMinioVersion 解析 `minio --version` 输出（如 "RELEASE.2024-06-13T22-53-53Z (commit: ...)"）。
func parseMinioVersion(out string) string {
	return minioRe.FindString(out)
}
