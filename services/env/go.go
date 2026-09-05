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

const goBaseRel = "runtime/go"

// GoRuntime 管理便携 Go 运行时（官方发行包），支持多版本共存于 runtime/go/<version>。
type GoRuntime struct {
	baseDir string
}

func NewGoRuntime() *GoRuntime {
	return &GoRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), goBaseRel)}
}

func (g *GoRuntime) Kind() Runtime                 { return RuntimeGo }
func (g *GoRuntime) DetectArgs() []string          { return []string{"version"} }
func (g *GoRuntime) ParseVersion(out string) (string, error) {
	const p = "go version go"
	if i := strings.Index(out, p); i >= 0 {
		rest := out[i+len(p):]
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			return rest[:sp], nil
		}
		return strings.TrimSpace(rest), nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeGo))
}
func (g *GoRuntime) DisplayName() string          { return DisplayName(RuntimeGo) }
func (g *GoRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }
func (g *GoRuntime) Recommended() []string        { return Versions(RuntimeGo) }

func (g *GoRuntime) versionDir(version string) string {
	return filepath.Join(g.baseDir, version)
}

func (g *GoRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.versionDir(version), "bin", "go.exe")
	}
	return filepath.Join(g.versionDir(version), "bin", "go")
}

// InstalledVersions 同时探测便携目录（runtime/go/<v>）与系统 PATH 上的 go。
// 这正是修复「本机已装 go 1.25 却提示未安装」的关键——不再只认便携目录。
func (g *GoRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(g.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(g.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: g.versionDir(v)})
				dirs.record(filepath.Dir(g.ExeFor(v)))
			}
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		if v := parseGoVersion(RunVersion(p, "version")); v != "" {
			// LookPath 可能命中本就由 QuickDock 托管并写入 PATH 的便携版（与上面 ReadDir 收录的是同一份），
			// 此时不要重复登记为 system，否则同一版本出现两条“取消环境变量”。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Go 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (g *GoRuntime) DeleteVersion(version string) error {
	dir := g.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (g *GoRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGo)[0]
	}
	dir := g.versionDir(version)
	if _, err := os.Stat(g.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Go " + version + " 已安装: " + g.ExeFor(version))
		}
		return nil
	}
	// 残留的半成品目录先清掉再装
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeGo, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Go 下载源")
	}
	ext := ".zip"
	if runtime.GOOS != "windows" {
		ext = ".tar.gz"
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-go-"+version+ext)
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Go "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Go " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Go 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Go…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Go 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Go 失败: %w", err)
	}
	if _, err := os.Stat(g.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", g.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Go " + version + " 解压完成")
	}
	return nil
}

func parseGoVersion(out string) string {
	// "go version go1.23.4 windows/amd64"
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "go") && strings.Contains(tok, ".") {
			return strings.TrimPrefix(tok, "go")
		}
	}
	return ""
}
