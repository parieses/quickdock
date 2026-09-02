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

const gitBaseRel = "runtime/git"

// GitRuntime 管理便携 Git 运行时（git-for-windows 的 MinGit 发行包），支持多版本共存于 runtime/git/<version>。
// 同时探测系统 PATH 上已安装的 git（如 Git for Windows / 系统自带）。
type GitRuntime struct {
	baseDir string
}

func NewGitRuntime() *GitRuntime {
	return &GitRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), gitBaseRel)}
}

func (g *GitRuntime) Kind() Runtime                 { return RuntimeGit }
func (g *GitRuntime) DisplayName() string          { return DisplayName(RuntimeGit) }
func (g *GitRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }
func (g *GitRuntime) Recommended() []string        { return Versions(RuntimeGit) }

func (g *GitRuntime) versionDir(version string) string {
	return filepath.Join(g.baseDir, version)
}

// cmdDir 返回含 git 可执行文件的目录：Windows 用 cmd/，其余用 bin/。该目录即写入系统 PATH 的条目。
func (g *GitRuntime) cmdDir(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.versionDir(version), "cmd")
	}
	return filepath.Join(g.versionDir(version), "bin")
}

func (g *GitRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.cmdDir(version), "git.exe")
	}
	return filepath.Join(g.cmdDir(version), "git")
}

// InstalledVersions 同时探测便携目录（runtime/git/<v>）与系统 PATH 上的 git。
func (g *GitRuntime) InstalledVersions() []Install {
	var out []Install
	if entries, err := os.ReadDir(g.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(g.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: g.cmdDir(v)})
			}
		}
	}
	if p, err := exec.LookPath("git"); err == nil {
		if v := parseGitVersion(RunVersion(p, "--version")); v != "" {
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Git 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (g *GitRuntime) DeleteVersion(version string) error {
	dir := g.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (g *GitRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGit)[0]
	}
	if _, err := os.Stat(g.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Git " + version + " 已安装: " + g.ExeFor(version))
		}
		return nil
	}
	dir := g.versionDir(version)
	// 残留的半成品目录先清掉再装
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeGit, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Git 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-git-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Git "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Git " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Git 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Git…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Git 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Git 失败: %w", err)
	}
	if _, err := os.Stat(g.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", g.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Git " + version + " 解压完成")
	}
	return nil
}

func parseGitVersion(out string) string {
	// "git version 2.45.0.windows.1" 或 "git version 2.45.0"
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "version") {
			continue
		}
		if v := strings.TrimPrefix(tok, "git"); v != "" && strings.Contains(v, ".") {
			return strings.TrimSuffix(v, ".windows.1")
		}
	}
	return ""
}
