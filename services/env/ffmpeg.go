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

const ffmpegBaseRel = "runtime/ffmpeg"

// FFmpegRuntime 管理便携 FFmpeg 运行时（gyan.dev Windows 构建，含 bin/ffmpeg.exe）。
// 纯工具型：仅下载/解压/版本切换，不支持服务（ffmpeg 是一次性命令行工具，无常驻进程）。
type FFmpegRuntime struct {
	baseDir string
}

func NewFFmpegRuntime() *FFmpegRuntime {
	return &FFmpegRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), ffmpegBaseRel)}
}

func (f *FFmpegRuntime) Kind() Runtime                 { return RuntimeFFmpeg }
func (f *FFmpegRuntime) DetectArgs() []string          { return []string{"version"} }
func (f *FFmpegRuntime) ParseVersion(out string) (string, error) {
	if v := parseFFmpegVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeFFmpeg))
}
func (f *FFmpegRuntime) DisplayName() string          { return DisplayName(RuntimeFFmpeg) }
func (f *FFmpegRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (f *FFmpegRuntime) Recommended() []string        { return Versions(RuntimeFFmpeg) }

func (f *FFmpegRuntime) versionDir(version string) string {
	return filepath.Join(f.baseDir, version)
}

// ExeFor gyan.dev 构建解压后结构为 <dir>/bin/ffmpeg.exe（顶级目录被剥离）。
func (f *FFmpegRuntime) ExeFor(version string) string {
	return filepath.Join(f.versionDir(version), "bin", "ffmpeg.exe")
}

func (f *FFmpegRuntime) InstalledVersions() []Install {
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
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		if v := parseFFmpegVersion(RunVersion(p, "-version")); v != "" {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return out
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

func (f *FFmpegRuntime) DeleteVersion(version string) error {
	dir := f.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (f *FFmpegRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeFFmpeg)[0]
	}
	dir := f.versionDir(version)
	if _, err := os.Stat(f.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("FFmpeg " + version + " 已安装: " + f.ExeFor(version))
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeFFmpeg, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 FFmpeg 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-ffmpeg-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 FFmpeg "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 FFmpeg " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 FFmpeg 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 FFmpeg…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 FFmpeg 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 FFmpeg 失败: %w", err)
	}
	if _, err := os.Stat(f.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", f.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("FFmpeg " + version + " 解压完成（bin/ffmpeg.exe 可用）")
	}
	return nil
}

// parseFFmpegVersion 解析 `ffmpeg -version` 输出首行（如 "ffmpeg version 7.1-essentials_build-..."）。
func parseFFmpegVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ffmpeg version") {
			fields := strings.Fields(line)
			for _, tok := range fields {
				if tok == "version" {
					continue
				}
				// 去掉构建后缀（如 7.1-essentials_build-...）
				if i := strings.IndexByte(tok, '-'); i > 0 {
					return tok[:i]
				}
				return tok
			}
		}
	}
	return ""
}
