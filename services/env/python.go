package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"quickdock/internal/platform"
)

const pythonBaseRel = "runtime/python"

// PythonRuntime 管理便携 Python 运行时（python.org embeddable 构建，单目录含 python.exe）。
// 纯工具型：仅下载/解压/版本切换，不支持服务。embeddable 默认不含 pip，
// 需要时在版本目录内 `python -m ensurepip` 引导即可（不自动执行以免安装失败）。
type PythonRuntime struct {
	baseDir string
}

func NewPythonRuntime() *PythonRuntime {
	return &PythonRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), pythonBaseRel)}
}

func (p *PythonRuntime) Kind() Runtime                 { return RuntimePython }
func (p *PythonRuntime) DetectArgs() []string          { return []string{"--version"} }
func (p *PythonRuntime) ParseVersion(out string) (string, error) {
	if v := parsePythonVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimePython))
}
func (p *PythonRuntime) DisplayName() string          { return DisplayName(RuntimePython) }
func (p *PythonRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (p *PythonRuntime) Recommended() []string        { return Versions(RuntimePython) }

func (p *PythonRuntime) versionDir(version string) string {
	return filepath.Join(p.baseDir, version)
}

// ExeFor embeddable 包解压后 python.exe 直接在版本目录根（扁平结构，无顶级目录）。
func (p *PythonRuntime) ExeFor(version string) string {
	return filepath.Join(p.versionDir(version), "python.exe")
}

func (p *PythonRuntime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if entries, err := os.ReadDir(p.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(p.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: p.versionDir(v)})
				dirs.record(filepath.Dir(p.ExeFor(v)))
			}
		}
	}
	if exe, err := exec.LookPath("python"); err == nil {
		if v := parsePythonVersion(RunVersion(exe, "--version")); v != "" {
			// LookPath 可能命中本就由 QuickDock 托管并写入 PATH 的便携版（与上面 ReadDir 收录的是同一份），
			// 此时不要重复登记为 system，否则同一版本出现两条“取消环境变量”。
			if dirs.dedupeByDir(exe) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: exe})
		}
	}
	return out
}

func (p *PythonRuntime) DeleteVersion(version string) error {
	dir := p.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (p *PythonRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimePython)[0]
	}
	dir := p.versionDir(version)
	if _, err := os.Stat(p.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Python " + version + " 已安装: " + p.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimePython, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Python 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-python-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Python "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Python " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Python 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Python…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Python 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Python 失败: %w", err)
	}
	if _, err := os.Stat(p.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", p.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("Python " + version + " 解压完成（embeddable 默认不含 pip，需时在目录内执行 python -m ensurepip）")
	}
	return nil
}

// parsePythonVersion 解析 `python --version` 输出（如 "Python 3.12.7"，版本信息输出到 stderr）。
func parsePythonVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "Python") {
			continue
		}
		if strings.Contains(tok, ".") {
			return strings.TrimSuffix(tok, "\r")
		}
	}
	return ""
}
