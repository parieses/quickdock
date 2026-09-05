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

const ghBaseRel = "runtime/gh"

// GhRuntime 管理便携 GitHub CLI（gh）运行时。多版本并存于 runtime/gh/<version>，
// 下载的 gh_X.Y.Z_windows_amd64.zip 内为 gh.exe。无服务、无配置文件。
type GhRuntime struct {
	baseDir string
}

func NewGhRuntime() *GhRuntime {
	return &GhRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), ghBaseRel)}
}

func (g *GhRuntime) Kind() Runtime                 { return RuntimeGh }
func (g *GhRuntime) DetectArgs() []string          { return []string{"--version"} }
func (g *GhRuntime) ParseVersion(out string) (string, error) {
	// "gh version 2.100.0 (2025-..." 或 "gh version 2.100.0"
	for _, tok := range strings.Fields(out) {
		if strings.EqualFold(tok, "version") {
			continue
		}
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeGh))
}
func (g *GhRuntime) DisplayName() string          { return DisplayName(RuntimeGh) }
func (g *GhRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (g *GhRuntime) Recommended() []string        { return Versions(RuntimeGh) }

func (g *GhRuntime) versionDir(version string) string { return filepath.Join(g.baseDir, version) }

func (g *GhRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.versionDir(version), "gh.exe")
	}
	return filepath.Join(g.versionDir(version), "gh")
}

func (g *GhRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(g.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		exe := g.ExeFor(e.Name())
		if !fileExists(exe) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: g.versionDir(e.Name())})
	}
	return out
}

func (g *GhRuntime) DeleteVersion(version string) error {
	dir := g.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (g *GhRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGh)[0]
	}
	dir := g.versionDir(version)
	exe := g.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("GitHub CLI " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeGh, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 GitHub CLI 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-gh-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 GitHub CLI "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 GitHub CLI " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 GitHub CLI 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 GitHub CLI…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 GitHub CLI 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 GitHub CLI 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("GitHub CLI " + version + " 解压完成")
	}
	return nil
}
