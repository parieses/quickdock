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

const bunBaseRel = "runtime/bun"

// BunRuntime 管理便携 Bun 运行时（oven-sh/bun）。多版本并存于 runtime/bun/<version>，
// 下载的 bun-windows-x64.zip 内为 bun.exe。无服务、无配置文件。
type BunRuntime struct {
	baseDir string
}

func NewBunRuntime() *BunRuntime {
	return &BunRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), bunBaseRel)}
}

func (b *BunRuntime) Kind() Runtime                 { return RuntimeBun }
func (b *BunRuntime) DetectArgs() []string          { return []string{"--version"} }
func (b *BunRuntime) ParseVersion(out string) (string, error) {
	// "bun 1.4.1" 或 "bun 1.4.1+sha1abc"
	for _, tok := range strings.Fields(out) {
		if strings.EqualFold(tok, "bun") {
			continue
		}
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			if i := strings.IndexByte(v, '+'); i >= 0 {
				v = v[:i]
			}
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeBun))
}
func (b *BunRuntime) DisplayName() string          { return DisplayName(RuntimeBun) }
func (b *BunRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (b *BunRuntime) Recommended() []string        { return Versions(RuntimeBun) }

func (b *BunRuntime) versionDir(version string) string { return filepath.Join(b.baseDir, version) }

func (b *BunRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(b.versionDir(version), "bun.exe")
	}
	return filepath.Join(b.versionDir(version), "bun")
}

func (b *BunRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(b.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		exe := b.ExeFor(e.Name())
		if !fileExists(exe) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: b.versionDir(e.Name())})
	}
	return out
}

func (b *BunRuntime) DeleteVersion(version string) error {
	dir := b.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (b *BunRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeBun)[0]
	}
	dir := b.versionDir(version)
	exe := b.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("Bun " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeBun, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Bun 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-bun-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Bun "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Bun " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Bun 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Bun…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Bun 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Bun 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("Bun " + version + " 解压完成")
	}
	return nil
}
