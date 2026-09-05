package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const traefikBaseRel = "runtime/traefik"
const traefikWebPort = 8080

// 默认静态配置：用非特权端口（8080）避免与 80 冲突，开启 insecure dashboard 便于本地预览。
// 用户可在环境页「编辑配置」直接改写 traefik.yml。
const defaultTraefikConfig = `entryPoints:
  web:
    address: ":8080"
api:
  dashboard: true
  insecure: true
log:
  filePath: traefik.log
  level: INFO
`

// TraefikRuntime 管理便携 Traefik 边缘路由器（traefik/traefik），单文件 traefik.exe。
// 服务型（serve），支持配置校验（validate --configFile）与配置编辑（ConfigProvider）。
type TraefikRuntime struct {
	baseDir string
}

func NewTraefikRuntime() *TraefikRuntime {
	return &TraefikRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), traefikBaseRel)}
}

func (t *TraefikRuntime) Kind() Runtime                 { return RuntimeTraefik }
func (t *TraefikRuntime) DetectArgs() []string          { return []string{"version"} }
func (t *TraefikRuntime) ParseVersion(out string) (string, error) {
	// 输出形如 "Version: 3.7.13\nCodename: ..."，取首个语义化版本号
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") && !strings.EqualFold(v, "Version") {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeTraefik))
}
func (t *TraefikRuntime) DisplayName() string          { return DisplayName(RuntimeTraefik) }
func (t *TraefikRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (t *TraefikRuntime) Recommended() []string        { return Versions(RuntimeTraefik) }

func (t *TraefikRuntime) versionDir(version string) string { return filepath.Join(t.baseDir, version) }

func (t *TraefikRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(t.versionDir(version), "traefik.exe")
	}
	return filepath.Join(t.versionDir(version), "traefik")
}

func (t *TraefikRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(t.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !fileExists(t.ExeFor(e.Name())) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: t.versionDir(e.Name())})
	}
	return out
}

func (t *TraefikRuntime) DeleteVersion(version string) error {
	dir := t.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (t *TraefikRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeTraefik)[0]
	}
	dir := t.versionDir(version)
	exe := t.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("Traefik " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeTraefik, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Traefik 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-traefik-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Traefik "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Traefik " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Traefik 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Traefik…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Traefik 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Traefik 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s", exe)
	}
	if err := t.ensureConfig(version); err != nil {
		logger.W("[env][traefik] 生成默认 traefik.yml 失败: %v", err)
	}
	if cb.OnLog != nil {
		cb.OnLog("Traefik " + version + " 解压完成（默认 traefik.yml 已生成，可编辑后启动）")
	}
	return nil
}

// ConfigPath 返回某版本 traefik.yml 绝对路径（供通用「编辑配置」弹窗复用）。
func (t *TraefikRuntime) ConfigPath(version string) string {
	return filepath.Join(t.versionDir(version), "traefik.yml")
}

// LogPath 返回某版本 traefik.log 绝对路径（运行日志落盘位置）。
func (t *TraefikRuntime) LogPath(version string) string {
	return filepath.Join(t.versionDir(version), "traefik.log")
}

func (t *TraefikRuntime) ensureConfig(version string) error {
	p := t.ConfigPath(version)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(defaultTraefikConfig), 0644)
}

func (t *TraefikRuntime) DefaultPort() int { return traefikWebPort }

func (t *TraefikRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := t.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 Traefik 版本")
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
			exe, wd = t.ExeFor(version), t.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	// 单实例：本会话已拉起则提示先停止
	running, _ := svcMgr.info(RuntimeTraefik)
	if running != "" && running != version {
		return fmt.Errorf("Traefik 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	// 端口被非 traefik 占用时提前提示
	if running == "" && isPortOpen(traefikWebPort) {
		if pid := findListenPID(traefikWebPort); pid != 0 && !processImageMatches(pid, "traefik.exe") {
			return fmt.Errorf("端口 %d 已被占用，无法启动 Traefik", traefikWebPort)
		}
	}
	if err := t.ensureConfig(version); err != nil {
		return fmt.Errorf("生成 traefik.yml 失败: %w", err)
	}
	if onLog != nil {
		onLog("启动 Traefik " + version + " …")
	}
	return svcMgr.start(RuntimeTraefik, version, exe, wd,
		[]string{"--configFile", "traefik.yml"}, t.LogPath(version), onLog)
}

// ValidateConfig 启动前校验 traefik.yml 合法性（错误直接返回可读信息）。
func (t *TraefikRuntime) ValidateConfig(version string) error {
	if err := t.ensureConfig(version); err != nil {
		return fmt.Errorf("生成 traefik.yml 失败: %w", err)
	}
	exe := t.ExeFor(version)
	cmd := sysutil.Command(exe, "validate", "--configFile", "traefik.yml")
	cmd.Dir = t.versionDir(version)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// LogGet 读取某版本运行日志尾部（实现 LogProvider）。
func (t *TraefikRuntime) LogGet(version string) (string, error) {
	data, err := os.ReadFile(t.LogPath(version))
	if err != nil {
		return "", fmt.Errorf("读取日志失败: %w", err)
	}
	if len(data) > 8192 {
		data = data[len(data)-8192:]
	}
	return strings.TrimSpace(string(data)), nil
}

func (t *TraefikRuntime) Stop(version string) error {
	stopByPort(traefikWebPort, "traefik.exe")
	svcMgr.forget(RuntimeTraefik)
	return nil
}

func (t *TraefikRuntime) Status(version string) ServiceStatus {
	port := traefikWebPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeTraefik); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeTraefik)
		return st
	}
	if isPortOpen(port) {
		st.Running = true
		st.Version = version
		if pid := findListenPID(port); pid != 0 {
			st.PID = pid
		}
	}
	return st
}
