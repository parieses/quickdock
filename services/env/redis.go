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

const redisBaseRel = "runtime/redis"

const redisDefaultPort = 6379

// RedisRuntime 管理便携 Redis 运行时（tporadowski/redis Windows 构建），多版本共存于 runtime/redis/<version>。
// 同时实现 ServiceController，支持以服务方式启动/停止并监听运行状态。
type RedisRuntime struct {
	baseDir string
}

func NewRedisRuntime() *RedisRuntime {
	return &RedisRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), redisBaseRel)}
}

func (r *RedisRuntime) Kind() Runtime                 { return RuntimeRedis }
func (r *RedisRuntime) DisplayName() string          { return DisplayName(RuntimeRedis) }
func (r *RedisRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (r *RedisRuntime) Recommended() []string        { return Versions(RuntimeRedis) }

func (r *RedisRuntime) versionDir(version string) string {
	return filepath.Join(r.baseDir, version)
}

func (r *RedisRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(r.versionDir(version), "redis-server.exe")
	}
	return filepath.Join(r.versionDir(version), "redis-server")
}

func (r *RedisRuntime) InstalledVersions() []Install {
	var out []Install
	if entries, err := os.ReadDir(r.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(r.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: r.versionDir(v)})
			}
		}
	}
	if exe, err := exec.LookPath("redis-server"); err == nil {
		if v := parseRedisVersion(RunVersion(exe, "--version")); v != "" {
			out = append(out, Install{Version: v, Scope: "system", Path: exe})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Redis 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (r *RedisRuntime) DeleteVersion(version string) error {
	dir := r.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (r *RedisRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeRedis)[0]
	}
	dir := r.versionDir(version)
	if _, err := os.Stat(r.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Redis " + version + " 已安装: " + r.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeRedis, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Redis 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-redis-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Redis "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Redis " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Redis 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Redis…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Redis 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Redis 失败: %w", err)
	}
	if _, err := os.Stat(r.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", r.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Redis " + version + " 解压完成")
	}
	return nil
}

// ---- ServiceController ----

func (r *RedisRuntime) DefaultPort() int { return redisDefaultPort }

func (r *RedisRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	if version == "" {
		if vs := r.InstalledVersions(); len(vs) > 0 {
			version = vs[0].Version
		}
	}
	if version == "" {
		return fmt.Errorf("请先安装 Redis 版本")
	}
	exe := r.ExeFor(version)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if onLog != nil {
		onLog("启动 Redis " + version + " …")
	}
	return svcMgr.start(RuntimeRedis, exe, r.versionDir(version), onLog)
}

func (r *RedisRuntime) Stop(version string) error {
	// 1) 对每个已装版本目录尝试原生优雅关闭（redis-cli SHUTDOWN NOSAVE，覆盖孤儿/多版本）
	for _, ins := range r.InstalledVersions() {
		runRedisStopSignal(filepath.Join(r.versionDir(ins.Version), "redis-cli.exe"), redisDefaultPort)
	}
	// 2) 端口兜底：杀掉占用默认端口且镜像确为 redis-server.exe 的进程树
	stopByPort(redisDefaultPort, "redis-server.exe")
	// 3) 清掉会话内记录的句柄
	svcMgr.forget(RuntimeRedis)
	return nil
}

func (r *RedisRuntime) Status(version string) ServiceStatus {
	running := isPortOpen(redisDefaultPort)
	st := ServiceStatus{Running: running, Port: redisDefaultPort}
	if running {
		st.PID = svcMgr.pid(RuntimeRedis)
		st.Version = version
	} else if svcMgr.running(RuntimeRedis) {
		// 端口未通但进程在（如启动中）：也视为运行中以便前端展示
		st.Running = true
		st.PID = svcMgr.pid(RuntimeRedis)
		st.Version = version
	}
	return st
}

func parseRedisVersion(out string) string {
	// "Redis server v=5.0.14.1 sha=... malloc=jemalloc ..."
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "v=") {
			return strings.TrimPrefix(tok, "v=")
		}
	}
	return ""
}
