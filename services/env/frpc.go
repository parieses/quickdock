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
// 工具型（无 ServiceController）：frpc 是出站客户端、无固定监听端口，且需先配好远端 frps 才能跑，
// 故只提供安装与 frpc.toml 配置编辑（通用 ConfigProvider），启停由用户在终端按需执行。
type FrpcRuntime struct {
	baseDir string
}

func NewFrpcRuntime() *FrpcRuntime {
	return &FrpcRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), frpcBaseRel)}
}

func (f *FrpcRuntime) Kind() Runtime                 { return RuntimeFrpc }
func (f *FrpcRuntime) DetectArgs() []string          { return []string{"--version"} }
func (f *FrpcRuntime) ParseVersion(out string) (string, error) {
	if v := parseFrpcVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeFrpc))
}
func (f *FrpcRuntime) DisplayName() string          { return DisplayName(RuntimeFrpc) }
func (f *FrpcRuntime) SupportedPlatforms() []string { return []string{"windows"} }
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
	tmpl := `# frpc 配置文件（由 QuickDock 生成的最小模板）
# 把 server_addr / server_port 改成你的 frps 服务器地址与端口，并按需配置 proxy。
[common]
server_addr = "127.0.0.1"
server_port = 7000

[ssh]
type = "tcp"
local_ip = "127.0.0.1"
local_port = 22
remote_port = 6000
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
