package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/platform"
)

const nginxBaseRel = "runtime/nginx"

const nginxDefaultPort = 80

// NginxRuntime 管理便携 Nginx 运行时（官方 Windows 构建），多版本共存于 runtime/nginx/<version>。
// 同时实现 ServiceController，支持以服务方式启动/停止并监听运行状态。
type NginxRuntime struct {
	baseDir string
}

func NewNginxRuntime() *NginxRuntime {
	return &NginxRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), nginxBaseRel)}
}

func (n *NginxRuntime) Kind() Runtime                 { return RuntimeNginx }
func (n *NginxRuntime) DisplayName() string          { return DisplayName(RuntimeNginx) }
func (n *NginxRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (n *NginxRuntime) Recommended() []string        { return Versions(RuntimeNginx) }

func (n *NginxRuntime) versionDir(version string) string {
	return filepath.Join(n.baseDir, version)
}

func (n *NginxRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(n.versionDir(version), "nginx.exe")
	}
	return filepath.Join(n.versionDir(version), "nginx")
}

func (n *NginxRuntime) InstalledVersions() []Install {
	var out []Install
	if entries, err := os.ReadDir(n.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(n.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: n.versionDir(v)})
			}
		}
	}
	if exe, err := exec.LookPath("nginx"); err == nil {
		if v := parseNginxVersion(RunVersion(exe, "-v")); v != "" {
			out = append(out, Install{Version: v, Scope: "system", Path: exe})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Nginx 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (n *NginxRuntime) DeleteVersion(version string) error {
	dir := n.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (n *NginxRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeNginx)[0]
	}
	dir := n.versionDir(version)
	if _, err := os.Stat(n.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Nginx " + version + " 已安装: " + n.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeNginx, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Nginx 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-nginx-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Nginx "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Nginx " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Nginx 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Nginx…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Nginx 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Nginx 失败: %w", err)
	}
	if _, err := os.Stat(n.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", n.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Nginx " + version + " 解压完成")
	}
	return nil
}

// ---- ServiceController ----

func (n *NginxRuntime) DefaultPort() int { return nginxDefaultPort }

func (n *NginxRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	if version == "" {
		if vs := n.InstalledVersions(); len(vs) > 0 {
			version = vs[0].Version
		}
	}
	if version == "" {
		return fmt.Errorf("请先安装 Nginx 版本")
	}
	exe := n.ExeFor(version)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if onLog != nil {
		onLog("启动 Nginx " + version + " …")
	}
	return svcMgr.start(RuntimeNginx, exe, n.versionDir(version), onLog)
}

func (n *NginxRuntime) Stop(version string) error {
	// 1) 对每个已装版本目录尝试原生优雅停止（覆盖孤儿/多版本场景，不依赖 QuickDock 句柄）
	for _, ins := range n.InstalledVersions() {
		runNginxStopSignal(n.ExeFor(ins.Version), n.versionDir(ins.Version))
	}
	// 2) 端口兜底：杀掉占用默认端口且镜像确为 nginx.exe 的进程树（避免误杀其它程序）
	stopByPort(nginxDefaultPort, "nginx.exe")
	// 3) 清掉会话内记录的句柄
	svcMgr.forget(RuntimeNginx)
	return nil
}

func (n *NginxRuntime) Status(version string) ServiceStatus {
	running := isPortOpen(nginxDefaultPort)
	st := ServiceStatus{Running: running, Port: nginxDefaultPort}
	if running {
		st.PID = svcMgr.pid(RuntimeNginx)
		st.Version = version
	} else if svcMgr.running(RuntimeNginx) {
		st.Running = true
		st.PID = svcMgr.pid(RuntimeNginx)
		st.Version = version
	}
	return st
}

func parseNginxVersion(out string) string {
	// "nginx version: nginx/1.27.5"
	if i := strings.Index(out, "nginx/"); i >= 0 {
		return out[i+6:]
	}
	return ""
}
