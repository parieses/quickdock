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

const frpcBaseRel = "runtime/frpc"

// FrpcRuntime 管理便携 frpc 运行时（fatedier/frp 内网穿透客户端）。
// 单版本语义（singleVersion=true）：始终安装到 runtime/frpc，装新版本即原地覆盖旧版本，
// 不支持多版本并存——frpc 一份客户端足够，多版本并存无收益。用户侧的 frpc.toml 配置在覆盖升级时
// 会被保留（与 Ollama 保留 ollama.env 同思路），避免每次升级丢失服务端地址。
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

// versionDir 单版本语义：忽略 version，目录恒为 runtime/frpc。
func (f *FrpcRuntime) versionDir(_ string) string {
	return f.baseDir
}

// legacyVersionDir 多版本时代的目录形态 runtime/frpc/<version>，仅用于迁移。
func (f *FrpcRuntime) legacyVersionDir(version string) string {
	return filepath.Join(f.baseDir, version)
}

// migrateLegacyLayout 把旧的 runtime/frpc/<version>/ 内容上提到 runtime/frpc/，并清掉空目录。
// 只在目标目录尚无 frpc 可执行文件时执行（幂等）；失败不阻断调用方，交由后续流程按「未安装」处理。
func (f *FrpcRuntime) migrateLegacyLayout() {
	if _, err := os.Stat(f.ExeFor("")); err == nil {
		return // 已是单版本布局
	}
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		return
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() || !reFrpcVersionDir.MatchString(e.Name()) {
			continue
		}
		if best == "" || semverLess(best, e.Name()) {
			best = e.Name()
		}
	}
	if best == "" {
		return
	}
	src := f.legacyVersionDir(best)
	names, err := os.ReadDir(src)
	if err != nil {
		return
	}
	for _, n := range names {
		_ = os.Rename(filepath.Join(src, n.Name()), filepath.Join(f.baseDir, n.Name()))
	}
	if rest, err := os.ReadDir(src); err == nil && len(rest) == 0 {
		_ = os.Remove(src)
	}
}

func (f *FrpcRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(f.versionDir(version), "frpc.exe")
	}
	return filepath.Join(f.versionDir(version), "frpc")
}

// ConfigPath 返回 frpc.toml 路径，实现通用 ConfigProvider 接口（读写由通用层提供）。
// 单版本：忽略 version，固定为 runtime/frpc/frpc.toml。
func (f *FrpcRuntime) ConfigPath(version string) string {
	return filepath.Join(f.versionDir(version), "frpc.toml")
}

func (f *FrpcRuntime) InstalledVersions() []Install {
	f.migrateLegacyLayout()

	var out []Install
	dirs := managedDirs{}
	v := parseFrpcVersion(RunVersion(f.ExeFor(""), "--version"))
	// exe 缺失或 --version 探测失败时仍按「已安装」登记，避免用户看到空列表却删不掉残留目录。
	if _, err := os.Stat(f.ExeFor("")); err != nil {
		if b, e2 := os.ReadFile(filepath.Join(f.baseDir, "VERSION")); e2 == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				v = s
			}
		}
	}
	if v != "" {
		out = append(out, Install{Version: v, Scope: "portable", Path: f.baseDir})
		dirs.record(f.baseDir)
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

// DeleteVersion 单版本语义：忽略 version，删整个 runtime/frpc（含 frpc.toml 配置）。
// ⚠️ 会一并删除 frpc.toml——卸载即视为不再需要该客户端配置；如需保留请先备份。
func (f *FrpcRuntime) DeleteVersion(version string) error {
	if _, err := os.Stat(f.baseDir); err != nil {
		return fmt.Errorf("未安装 frpc")
	}
	if _, err := os.Stat(f.ExeFor("")); err != nil {
		return fmt.Errorf("未找到 frpc 可执行文件: %s", f.ExeFor(""))
	}
	if err := os.RemoveAll(f.baseDir); err != nil {
		return fmt.Errorf("删除失败（可能服务正在运行，请先停止）: %w", err)
	}
	return nil
}

func (f *FrpcRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeFrpc)[0]
	}
	f.migrateLegacyLayout()

	installed := parseFrpcVersion(RunVersion(f.ExeFor(""), "--version"))
	isUpgrade := installed != ""

	// 升级时保留用户的 frpc.toml（服务端地址/代理配置），覆盖完成后再写回。
	cfgBak := ""
	if isUpgrade {
		if b, err := os.ReadFile(f.ConfigPath("")); err == nil {
			cfgBak = string(b)
		}
	}

	urls := CandidateURLs(RuntimeFrpc, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 frpc 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-frpc-"+version+".zip")
	if cb.OnStage != nil {
		stage := "正在下载 frpc " + version
		if isUpgrade {
			stage = "正在下载新版本 " + version + "，完成前保留当前 " + installed
		}
		cb.OnStage("download", stage+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 frpc " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 frpc 失败: %w（当前版本未受影响）", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 frpc…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 frpc 到 " + f.baseDir)
	}
	if err := Extract(zipPath, f.baseDir); err != nil {
		return fmt.Errorf("解压 frpc 失败: %w（当前版本未受影响）", err)
	}
	if _, err := os.Stat(f.ExeFor("")); err != nil {
		return fmt.Errorf("解压完成但未找到 %s（当前版本未受影响）", f.ExeFor(""))
	}
	// 升级：写回被覆盖前的 frpc.toml。
	if cfgBak != "" {
		_ = os.WriteFile(f.ConfigPath(""), []byte(cfgBak), 0o644)
	} else {
		// 全新安装：保证 frpc.toml 存在，使通用配置编辑可用（frp 发布包通常已带示例，缺失则写最小模板）。
		if err := f.ensureConfig(); err != nil {
			logger.W("[env][frpc] 生成默认 frpc.toml 失败: %v", err)
		}
	}
	_ = os.WriteFile(filepath.Join(f.baseDir, "VERSION"), []byte(version), 0o644)
	if cb.OnLog != nil {
		if isUpgrade {
			cb.OnLog("frpc 已从 " + installed + " 更新到 " + version + "，配置已保留")
		} else {
			cb.OnLog("frpc " + version + " 安装完成")
		}
	}
	return nil
}

// ensureConfig 若 frpc.toml 不存在则写入最小可用模板（连接远端 frps 的占位配置）。
func (f *FrpcRuntime) ensureConfig() error {
	p := f.ConfigPath("")
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

// reFrpcVersionDir 匹配旧多版本布局的版本目录名（如 "0.71.0"）。
var reFrpcVersionDir = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// parseFrpcVersion 解析 `frpc --version` 输出（如 "frpc version 0.61.0"）。
func parseFrpcVersion(out string) string {
	return frpcVerRe.FindString(out)
}

// 保证 frpc 满足 ConfigProvider（由通用层提供 ConfigGet/ConfigSet）。
var _ ConfigProvider = (*FrpcRuntime)(nil)

// ---- ServiceController ----

// DefaultPort 返回 0：frpc 是出站客户端、无固定监听端口，状态按子进程存活判定而非端口。
func (f *FrpcRuntime) DefaultPort() int { return 0 }

// LogPath 返回 frpc.log 绝对路径（运行日志落盘位置，含连接服务端的输出）。
func (f *FrpcRuntime) LogPath(version string) string {
	return filepath.Join(f.versionDir(version), "frpc.log")
}

// Start 以 `frpc -c frpc.toml`（前台阻塞）拉起，由 svcMgr 记录 PID 并捕获日志。
// 单版本语义：目录固定，exe 只有一份，忽略 version。运行判定由 svcMgr 子进程存活负责。
func (f *FrpcRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	f.migrateLegacyLayout()

	exe := f.ExeFor("")
	wd := f.baseDir
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("请先安装 frpc")
	}
	running, _ := svcMgr.info(RuntimeFrpc)
	if running != "" {
		return fmt.Errorf("frpc 已在运行（%s），请先停止再启动", running)
	}
	// 保证 frpc.toml 存在，使 frpc -c 能启动（用户可后续在环境页编辑）。
	if err := f.ensureConfig(); err != nil {
		return fmt.Errorf("生成 frpc.toml 失败: %w", err)
	}
	if onLog != nil {
		onLog("启动 frpc …")
	}
	return svcMgr.start(RuntimeFrpc, version, exe, wd,
		[]string{"-c", f.ConfigPath("")}, f.LogPath(""), onLog)
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

// LogGet 读取运行日志尾部（实现 LogProvider），供前端日志弹窗查看连接输出
// （如 frpc 打印的 "success connect to server"）。
func (f *FrpcRuntime) LogGet(version string) (string, error) {
	return readLogTail(f.LogPath(""))
}

// 保证 frpc 满足 ServiceController（启停按钮）与 LogProvider（日志弹窗）。
var _ ServiceController = (*FrpcRuntime)(nil)
var _ LogProvider = (*FrpcRuntime)(nil)
