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

const phpBaseRel = "runtime/php"

// PHPRuntime 管理便携 PHP 运行时（Windows 官方二进制包），多版本共存于 runtime/php/<version>。
type PHPRuntime struct {
	baseDir string
}

func NewPHPRuntime() *PHPRuntime {
	return &PHPRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), phpBaseRel)}
}

func (p *PHPRuntime) Kind() Runtime                 { return RuntimePHP }
func (p *PHPRuntime) DisplayName() string          { return DisplayName(RuntimePHP) }
func (p *PHPRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (p *PHPRuntime) Recommended() []string        { return Versions(RuntimePHP) }

func (p *PHPRuntime) versionDir(version string) string {
	return filepath.Join(p.baseDir, version)
}

func (p *PHPRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(p.versionDir(version), "php.exe")
	}
	return filepath.Join(p.versionDir(version), "php")
}

func (p *PHPRuntime) InstalledVersions() []Install {
	var out []Install
	if entries, err := os.ReadDir(p.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(p.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: p.versionDir(v)})
			}
		}
	}
	if exe, err := exec.LookPath("php"); err == nil {
		if v := parsePHPVersion(RunVersion(exe, "-v")); v != "" {
			out = append(out, Install{Version: v, Scope: "system", Path: exe})
		}
	}
	return out
}

// DeleteVersion 删除某便携 PHP 版本目录（系统 PATH 上的版本无目录可删，返回错误）。
func (p *PHPRuntime) DeleteVersion(version string) error {
	dir := p.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (p *PHPRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimePHP)[0]
	}
	dir := p.versionDir(version)
	if _, err := os.Stat(p.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("PHP " + version + " 已安装: " + p.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimePHP, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 PHP 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-php-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 PHP "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 PHP " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 PHP 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 PHP…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 PHP 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 PHP 失败: %w", err)
	}
	if err := p.ensurePHPIni(dir); err != nil {
		return err
	}
	if _, err := os.Stat(p.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", p.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("PHP " + version + " 解压完成")
	}
	return nil
}

// ensurePHPIni 若 php.ini 缺失则从 development/production 模板复制一份，避免 PHP 启动缺少基础配置。
func (p *PHPRuntime) ensurePHPIni(dir string) error {
	iniPath := filepath.Join(dir, "php.ini")
	if _, err := os.Stat(iniPath); err == nil {
		return nil
	}
	for _, tpl := range []string{"php.ini-development", "php.ini-production"} {
		src := filepath.Join(dir, tpl)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		return os.WriteFile(iniPath, data, 0644)
	}
	return nil
}

func parsePHPVersion(out string) string {
	// "PHP 8.3.20 (cli) (built: ...)"
	if i := strings.Index(out, "PHP "); i >= 0 {
		rest := out[i+4:]
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			return rest[:sp]
		}
		return strings.TrimSpace(rest)
	}
	return ""
}
