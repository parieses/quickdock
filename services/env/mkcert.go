package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/platform"
)

const mkcertBaseRel = "runtime/mkcert"

// MkcertRuntime 管理便携 mkcert 运行时（FiloSottile/mkcert），单文件 exe（非 zip）。
// 无服务、无配置文件、无数据目录，纯版本管理 + PATH 切换。
type MkcertRuntime struct {
	baseDir string
}

func NewMkcertRuntime() *MkcertRuntime {
	return &MkcertRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), mkcertBaseRel)}
}

func (m *MkcertRuntime) Kind() Runtime                 { return RuntimeMkcert }
func (m *MkcertRuntime) DetectArgs() []string          { return []string{"-version"} }
func (m *MkcertRuntime) ParseVersion(out string) (string, error) {
	// 输出形如 "v1.4.4" 或 "mkcert v1.4.4"
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMkcert))
}
func (m *MkcertRuntime) DisplayName() string          { return DisplayName(RuntimeMkcert) }
func (m *MkcertRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (m *MkcertRuntime) Recommended() []string        { return Versions(RuntimeMkcert) }

func (m *MkcertRuntime) versionDir(version string) string { return filepath.Join(m.baseDir, version) }

func (m *MkcertRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "mkcert.exe")
	}
	return filepath.Join(m.versionDir(version), "mkcert")
}

func (m *MkcertRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !fileExists(m.ExeFor(e.Name())) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: m.versionDir(e.Name())})
	}
	return out
}

func (m *MkcertRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

// Install 下载单文件 exe 直接落到 ExeFor 路径（mkcert 非 zip，无解压步骤）。
func (m *MkcertRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMkcert)[0]
	}
	dir := m.versionDir(version)
	exe := m.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("mkcert " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	urls := CandidateURLs(RuntimeMkcert, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 mkcert 下载源")
	}
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 mkcert "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 mkcert " + version + "…")
	}
	if err := Download(ctx, exe, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 mkcert 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("下载完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("mkcert " + version + " 安装完成")
	}
	return nil
}
