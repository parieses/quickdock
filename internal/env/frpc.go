package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const frpcBaseRel = "runtime/frpc"

// FrpcRuntime 管理便携 frpc 运行时（fatedier/frp 内网穿透客户端）。
// 实现 ServiceController：以 `frpc -c frpc.toml`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
// frpc 是出站客户端、无固定监听端口（无法像 nginx/redis 那样按端口探活），故运行状态一律
// 「按本会话拉起的子进程存活」判定；控制台是否真正连上远端 frps 以日志里 frpc 打印的
// "success connect to server" 为准（实现 LogProvider 供前端日志弹窗查看）。
type FrpcRuntime struct {
	baseDir string
}

func NewFrpcRuntime() *FrpcRuntime {
	return &FrpcRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), frpcBaseRel)}
}

func (f *FrpcRuntime) Kind() Runtime        { return RuntimeFrpc }
func (f *FrpcRuntime) DetectArgs() []string { return []string{"--version"} }
func (f *FrpcRuntime) ParseVersion(out string) (string, error) {
	if v := parseFrpcVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeFrpc))
}
func (f *FrpcRuntime) DisplayName() string          { return DisplayName(RuntimeFrpc) }
func (f *FrpcRuntime) SupportedPlatforms() []string { return []string{"windows", "darwin"} }
func (f *FrpcRuntime) Recommended() []string        { return Versions(RuntimeFrpc) }

func (f *FrpcRuntime) versionDir(version string) string {
	return filepath.Join(f.baseDir, version)
}

func (f *FrpcRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(f.versionDir(version), "frpc.exe")
	}
	return filepath.Join(f.versionDir(version), "frpc")
}

// ConfigPath 返回某版本 frpc.toml 路径，实现通用 ConfigProvider 接口（读写由通用层提供）。
func (f *FrpcRuntime) ConfigPath(version string) string {
	return filepath.Join(f.versionDir(version), "frpc.toml")
}

func (f *FrpcRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(f.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(f.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: f.versionDir(v)})
				dirs.record(filepath.Dir(f.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("frpc"); err == nil {
		if v := parseFrpcVersion(RunVersion(p, "--version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (f *FrpcRuntime) DeleteVersion(version string) error {
	dir := f.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (f *FrpcRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeFrpc)[0]
	}
	dir := f.versionDir(version)
	if _, err := os.Stat(f.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("frpc " + version + " 已安装: " + f.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeFrpc, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 frpc 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-frpc-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 frpc "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 frpc " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 frpc 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 frpc…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 frpc 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 frpc 失败: %w", err)
	}
	if _, err := os.Stat(f.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", f.ExeFor(version))
	}
	// 保证 frpc.toml 存在，使通用配置编辑可用（frp 发布包通常已带示例，缺失则写最小模板）
	if err := f.ensureConfig(version); err != nil {
		logger.W("[env][frpc] 生成默认 frpc.toml 失败: %v", err)
	}
	if cb.OnLog != nil {
		cb.OnLog("frpc " + version + " 解压完成")
	}
	return nil
}

// ensureConfig 若 frpc.toml 不存在则写入最小可用模板（连接远端 frps 的占位配置）。
func (f *FrpcRuntime) ensureConfig(version string) error {
	p := f.ConfigPath(version)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	tmpl := `# frpc 配置文件（由 QuickDock 生成的 TOML 最小模板）
# 把 serverAddr / serverPort 改成你的 frps 服务器地址与端口，并按需配置 proxies。
serverAddr = "127.0.0.1"
serverPort = 7000

[[proxies]]
name = "test-tcp"
type = "tcp"
localIP = "127.0.0.1"
localPort = 22
remotePort = 6000
`
	return os.WriteFile(p, []byte(tmpl), 0644)
}

var frpcVerRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// parseFrpcVersion 解析 `frpc --version` 输出（如 "frpc version 0.61.0"）。
func parseFrpcVersion(out string) string {
	return frpcVerRe.FindString(out)
}

// 保证 frpc 满足 ConfigProvider（由通用层提供 ConfigGet/ConfigSet）。
var _ ConfigProvider = (*FrpcRuntime)(nil)

// ---- ServiceController ----

// DefaultPort 返回 0：frpc 是出站客户端、无固定监听端口，状态按子进程存活判定而非端口。
func (f *FrpcRuntime) DefaultPort() int { return 0 }

// LogPath 返回某版本 frpc.log 绝对路径（运行日志落盘位置，含连接服务端的输出）。
func (f *FrpcRuntime) LogPath(version string) string {
	return filepath.Join(f.versionDir(version), "frpc.log")
}

// Start 以 `frpc -c frpc.toml`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
// frpc 单例：本会话已拉起其它版本则明确提示先停止。运行判定由 svcMgr 子进程存活负责。
func (f *FrpcRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := f.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 frpc 版本")
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
			exe, wd = f.ExeFor(version), f.versionDir(version)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	running, _ := svcMgr.info(RuntimeFrpc)
	if running != "" && running != version {
		return fmt.Errorf("frpc 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	// 保证 frpc.toml 存在，使 frpc -c 能启动（用户可后续在环境页编辑）
	if err := f.ensureConfig(version); err != nil {
		return fmt.Errorf("生成 frpc.toml 失败: %w", err)
	}
	if onLog != nil {
		onLog("启动 frpc " + version + " …")
	}
	return svcMgr.start(RuntimeFrpc, version, exe, wd,
		[]string{"-c", f.ConfigPath(version)}, f.LogPath(version), onLog)
}

// Stop 结束本会话拉起的 frpc 进程（无端口可兜底，只按句柄停本会话托管的实例），随后清句柄。
func (f *FrpcRuntime) Stop(version string) error {
	svcMgr.killTracked(RuntimeFrpc)
	svcMgr.forget(RuntimeFrpc)
	return nil
}

// Status 按子进程存活判定：frpc 无固定监听端口，只要本会话拉起的 frpc 进程仍在即视为运行中。
func (f *FrpcRuntime) Status(version string) ServiceStatus {
	st := ServiceStatus{Running: false, Port: 0}
	if v, _ := svcMgr.info(RuntimeFrpc); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeFrpc)
	}
	return st
}

// LogGet 读取某版本运行日志尾部（实现 LogProvider），供前端日志弹窗查看连接输出
// （如 frpc 打印的 "success connect to server"）。
func (f *FrpcRuntime) LogGet(version string) (string, error) {
	return readLogTail(f.LogPath(version))
}

// 保证 frpc 满足 ServiceController（启停按钮）与 LogProvider（日志弹窗）。
var _ ServiceController = (*FrpcRuntime)(nil)
var _ LogProvider = (*FrpcRuntime)(nil)
