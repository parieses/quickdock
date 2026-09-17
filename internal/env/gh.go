package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const ghBaseRel = "runtime/gh"

// GhRuntime 管理便携 GitHub CLI（gh）运行时。
// 与 Git 同属「单版本」工具链：始终安装到 runtime/gh，装新版本即覆盖旧版本
// （gh 主版本间兼容性好，日常开发只需一份；多版本并存只会让 PATH 切换变成负担）。
// 同时探测系统 PATH 上已安装的 gh（如官方安装器装的）。无服务、无配置文件。
type GhRuntime struct {
	dir string // runtime/gh
}

func NewGhRuntime() *GhRuntime {
	return &GhRuntime{dir: filepath.Join(platform.DefaultDataDir(), ghBaseRel)}
}

func (g *GhRuntime) Kind() Runtime        { return RuntimeGh }
func (g *GhRuntime) DetectArgs() []string { return []string{"--version"} }
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
func (g *GhRuntime) SupportedPlatforms() []string { return []string{"windows", "darwin"} }
func (g *GhRuntime) Recommended() []string        { return Versions(RuntimeGh) }

// ExeFor 单版本：忽略 version，始终返回固定目录下的 gh。
func (g *GhRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.dir, "gh.exe")
	}
	return filepath.Join(g.dir, "gh")
}

// DetectInstalledVersion 返回当前实际安装的版本号（探测 gh --version）。
// 单版本语义下它就是「已安装的那个版本」，供前端判断是否需要更新。
func (g *GhRuntime) DetectInstalledVersion() string {
	exe := g.ExeFor("")
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	if v, err := g.ParseVersion(RunVersion(exe, "--version")); err == nil {
		return v
	}
	return ""
}

// legacyExeFor 旧版多版本布局 runtime/gh/<version>/gh.exe（升级前安装的）。
// 仍要能被识别，否则用户升级后旧目录变成既看不见也删不掉的垃圾。
func (g *GhRuntime) legacyExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.dir, version, "gh.exe")
	}
	return filepath.Join(g.dir, version, "gh")
}

// InstalledVersions 便携目录（runtime/gh）+ 历史多版本子目录 + 系统 PATH。
func (g *GhRuntime) InstalledVersions() []Install {
	var out []Install
	seen := map[string]bool{}
	dirs := managedDirs{}
	// 单版本目录 runtime/gh/gh.exe
	if p := g.ExeFor(""); fileExists(p) {
		if v, err := g.ParseVersion(RunVersion(p, "--version")); err == nil && v != "" && !seen[v] {
			seen[v] = true
			out = append(out, Install{Version: v, Scope: "portable", Path: g.dir})
			dirs.record(filepath.Dir(p))
		}
	}
	// 历史多版本子目录 runtime/gh/<version>/gh.exe（升级前安装的）
	if entries, err := os.ReadDir(g.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			exe := g.legacyExeFor(e.Name())
			if !fileExists(exe) {
				continue
			}
			if v, err := g.ParseVersion(RunVersion(exe, "--version")); err == nil && v != "" {
				seen[v] = true
				out = append(out, Install{Version: v, Scope: "portable", Path: filepath.Dir(exe)})
				dirs.record(filepath.Dir(exe))
			}
		}
	}
	// 系统 PATH 上的 gh
	if p, err := exec.LookPath("gh"); err == nil {
		if v, err := g.ParseVersion(RunVersion(p, "--version")); err == nil && v != "" && !seen[v] {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 单版本语义：删除整个 runtime/gh（含 gh.exe 与配套文件），彻底卸载。
// 历史多版本子目录（升级前安装的遗留）单独清理。系统 PATH 上的 gh 由前端禁用删除（scope=system）。
func (g *GhRuntime) DeleteVersion(version string) error {
	// 历史多版本子目录 runtime/gh/<version>/（升级前安装的遗留）
	if _, err := os.Stat(g.legacyExeFor(version)); err == nil {
		return os.RemoveAll(filepath.Join(g.dir, version))
	}
	// 单版本主目录 runtime/gh/（始终删除整个托管目录，以便彻底卸载）
	if fileExists(g.ExeFor("")) {
		if err := os.RemoveAll(g.dir); err != nil {
			return fmt.Errorf("删除 GitHub CLI 失败: %w", err)
		}
		return nil
	}
	return fmt.Errorf("未找到该版本: %s", version)
}

func (g *GhRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGh)[0]
	}
	exe := g.ExeFor("")
	if fileExists(exe) {
		if cur, err := g.ParseVersion(RunVersion(exe, "--version")); err == nil && cur == version {
			if cb.OnLog != nil {
				cb.OnLog("GitHub CLI " + version + " 已安装: " + exe)
			}
			return nil
		}
		if cb.OnLog != nil {
			cb.OnLog("GitHub CLI 为单版本安装，覆盖更新为 " + version)
		}
	}
	// 单版本：整目录重装，避免新旧文件混用
	if err := os.RemoveAll(g.dir); err != nil {
		return fmt.Errorf("清理旧版本失败: %w", err)
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
		cb.OnLog("解压 GitHub CLI 到 " + g.dir)
	}
	if err := Extract(zipPath, g.dir); err != nil {
		return fmt.Errorf("解压 GitHub CLI 失败: %w", err)
	}
	// cli/cli 官方 Windows zip 顶层同时含 bin/、share/ 及 LICENSE 等散条目，不满足「单一顶层
	// 目录」条件，Extract 不会自动剥离，gh.exe 落在 runtime/gh/bin/ 下。将 bin/ 内容提升到
	// 版本目录根，使 ExeFor 约定的 runtime/gh/gh.exe 成立。
	if err := g.liftBin(g.dir); err != nil {
		return fmt.Errorf("整理 GitHub CLI 目录失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("GitHub CLI " + version + " 解压完成")
	}
	return nil
}

// liftBin 将解压产物内 bin/ 子目录的内容提升到版本目录根，使 gh.exe 直接位于版本目录。
// 已扁平（无 bin/ 子目录或 bin/ 已空）时直接返回，幂等可重复调用。
func (g *GhRuntime) liftBin(dir string) error {
	src := filepath.Join(dir, "bin")
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return nil // 已扁平，无需处理
	}
	if err := mergeInto(src, dir); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		logger.W("[env][gh] 清理 bin 空目录失败: %v", err)
	}
	return nil
}

// 保证实现对基础运行时接口的承诺（接口未实现时编译期即报错）。
var _ RuntimeAdapter = (*GhRuntime)(nil)
