package env

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const caddyBaseRel = "runtime/caddy"

// caddyAdminPort 是 Caddy admin API 的默认监听端口（localhost:2019），恒暴露、不受 Caddyfile 站点影响，
// 作为比裸端口更语义化的健康探针（区分 running vs ready）；也是 Stop 端口兜底的识别端口。
const caddyAdminPort = 2019

// defaultCaddyfile 首次启动前自动生成的默认配置：把 admin API 锁在 localhost:2019，
// 站点默认开在 :8080（避开 80 与 IIS/Skype 冲突），用户可在环境页直接编辑 Caddyfile 托管自己的站点。
const defaultCaddyfile = `{
	admin localhost:2019
}

:8080 {
	respond "QuickDock Caddy is running"
}
`

// CaddyRuntime 管理便携 Caddy 运行时（caddyserver/caddy 的 Windows 发行 zip，含单文件 caddy.exe）。
// 与 redis/nginx 同属「svcMgr PID 句柄」监控模型：以 `caddy run`（前台阻塞）拉起，由 svcMgr 记录 PID 与
// 捕获日志；运行状态优先走 svcMgr.info()/pid()，端口仅作外部/孤儿进程的兜底探测。Caddy 默认监听 80/443，
// 且恒暴露 admin API 于 :2019（localhost），可作为比 redis 端口更干净的健康探针；如需按 Caddyfile 的
// listen 地址判定状态，可仿 redis.configPort() 解析 Caddyfile。因此完全可实现 ServiceController。
type CaddyRuntime struct {
	baseDir string
}

func NewCaddyRuntime() *CaddyRuntime {
	return &CaddyRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), caddyBaseRel)}
}

func (c *CaddyRuntime) Kind() Runtime                 { return RuntimeCaddy }
func (c *CaddyRuntime) DisplayName() string          { return DisplayName(RuntimeCaddy) }
func (c *CaddyRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (c *CaddyRuntime) Recommended() []string        { return Versions(RuntimeCaddy) }

// versionDir 便携 Caddy 版本目录：runtime/caddy/<version>
func (c *CaddyRuntime) versionDir(version string) string {
	return filepath.Join(c.baseDir, version)
}

func (c *CaddyRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(c.versionDir(version), "caddy.exe")
	}
	return filepath.Join(c.versionDir(version), "caddy")
}

// InstalledVersions 便携目录（runtime/caddy/<version>/caddy.exe）+ 系统 PATH 上的 caddy。
func (c *CaddyRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(c.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(c.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: c.versionDir(v)})
				dirs.record(filepath.Dir(c.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("caddy"); err == nil {
		if v := parseCaddyVersion(RunVersion(p, "version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Caddy 版本目录。
func (c *CaddyRuntime) DeleteVersion(version string) error {
	dir := c.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

// Install 安装指定版本到 runtime/caddy/<version>：下载 zip 解压，校验 caddy.exe。
func (c *CaddyRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeCaddy)[0]
	}
	dir := c.versionDir(version)
	if _, err := os.Stat(c.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Caddy " + version + " 已安装: " + c.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeCaddy, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Caddy 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-caddy-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Caddy "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Caddy " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Caddy 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Caddy…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Caddy 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Caddy 失败: %w", err)
	}
	if _, err := os.Stat(c.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", c.ExeFor(version))
	}
	// 首次安装即写入默认 Caddyfile，供用户在环境页直接编辑（站点/反向代理等）。
	if err := c.ensureConfig(version); err != nil {
		logger.W("[env][caddy] 生成默认 Caddyfile 失败: %v", err)
	}
	if cb.OnLog != nil {
		cb.OnLog("Caddy " + version + " 解压完成（默认配置 Caddyfile 已生成，可编辑托管站点）")
	}
	return nil
}

// parseCaddyVersion 解析 `caddy version` 输出（如 "v2.8.4 h1:..." 或 "v2.8.4"）。
func parseCaddyVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			return v
		}
	}
	return ""
}

// ---- ServiceController ----

func (c *CaddyRuntime) DefaultPort() int { return caddyAdminPort }

// ConfigPath 返回某版本 Caddyfile 绝对路径。
func (c *CaddyRuntime) ConfigPath(version string) string {
	return filepath.Join(c.versionDir(version), "Caddyfile")
}

// LogPath 返回某版本 caddy.log 绝对路径（运行日志落盘位置）。
func (c *CaddyRuntime) LogPath(version string) string {
	return filepath.Join(c.versionDir(version), "caddy.log")
}

// ensureConfig 默认 Caddyfile 缺失时写入一份（admin 锁 localhost:2019，站点开 :8080 避开 80 冲突）。
func (c *CaddyRuntime) ensureConfig(version string) error {
	p := c.ConfigPath(version)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(defaultCaddyfile), 0644)
}

// Start 以 `caddy run`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志；admin API 恒在 localhost:2019，
// 作为健康探针。Caddy 单例（admin 端口唯一），同一时刻只能有一个实例。
func (c *CaddyRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := c.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 Caddy 版本")
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
			exe, wd = c.ExeFor(version), c.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	// admin 端口 2019 单例：本会话已拉起其它版本则明确提示先停止
	running, _ := svcMgr.info(RuntimeCaddy)
	if running != "" && running != version {
		return fmt.Errorf("Caddy 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	// 端口被非 caddy 的其它程序占用（如旧版残留）时，bind admin 会失败，这里提前给清晰提示
	if running == "" && isPortOpen(caddyAdminPort) {
		if pid := findListenPID(caddyAdminPort); pid != 0 && !processImageMatches(pid, "caddy.exe") {
			return fmt.Errorf("端口 %d 已被占用，无法启动 Caddy admin API", caddyAdminPort)
		}
	}
	if err := c.ensureConfig(version); err != nil {
		return fmt.Errorf("生成 Caddyfile 失败: %w", err)
	}
	if onLog != nil {
		onLog("启动 Caddy " + version + " …")
	}
	return svcMgr.start(RuntimeCaddy, version, exe, wd,
		[]string{"run", "--config", "Caddyfile", "--adapter", "caddyfile"}, c.LogPath(version), onLog)
}

// Stop 先 `caddy stop`（admin API 优雅关闭，覆盖孤儿/外部实例），端口兜底强杀镜像确为 caddy.exe 的进程树，
// 最后清掉会话内句柄。
func (c *CaddyRuntime) Stop(version string) error {
	for _, ins := range c.InstalledVersions() {
		stopCmd := sysutil.Command(c.ExeFor(ins.Version), "stop")
		_ = stopCmd.Run()
	}
	stopByPort(caddyAdminPort, "caddy.exe")
	svcMgr.forget(RuntimeCaddy)
	return nil
}

// Status 优先用本会话句柄（最准确）；否则以 admin API 连通性判定是否真在服务
// （比裸端口更语义化：端口通不代表 Caddy 已就绪，admin API 响应才代表真正在管配置）。
func (c *CaddyRuntime) Status(version string) ServiceStatus {
	port := caddyAdminPort
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeCaddy); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeCaddy)
		return st
	}
	if caddyHealthy() {
		st.Running = true
		st.Version = version
		if pid := findListenPID(port); pid != 0 {
			st.PID = pid
		}
	}
	return st
}

// caddyHealthy 探 admin API（localhost:2019）是否可连通，作为语义化健康检查。
func caddyHealthy() bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", caddyAdminPort))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}
