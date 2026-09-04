package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const composerBaseRel = "runtime/composer"

// ComposerRuntime 管理便携 Composer 运行时（单文件 composer.phar，依赖 PHP 运行）。
// 为纯工具型运行时：下载 composer.phar 到 runtime/composer/<version>/ 并支持多版本切换；
// 运行需本机或 QuickDock 托管的 PHP（php composer.phar）。不实现 ServiceController。
type ComposerRuntime struct {
	baseDir string
}

func NewComposerRuntime() *ComposerRuntime {
	return &ComposerRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), composerBaseRel)}
}

func (c *ComposerRuntime) Kind() Runtime                 { return RuntimeComposer }
func (c *ComposerRuntime) DisplayName() string          { return DisplayName(RuntimeComposer) }
func (c *ComposerRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }
func (c *ComposerRuntime) Recommended() []string        { return Versions(RuntimeComposer) }

// versionDir 便携 Composer 版本目录：runtime/composer/<version>
func (c *ComposerRuntime) versionDir(version string) string {
	return filepath.Join(c.baseDir, version)
}

func (c *ComposerRuntime) pharFor(version string) string {
	return filepath.Join(c.versionDir(version), "composer.phar")
}

func (c *ComposerRuntime) ExeFor(version string) string {
	return c.pharFor(version)
}

// InstalledVersions 仅列出便携目录下的 composer.phar（版本即目录名）。
// 系统 PATH 上的 composer 依赖 PHP 且难以可靠取版本号，不在此登记，避免污染版本列表。
func (c *ComposerRuntime) InstalledVersions() []Install {
	var out []Install
	if entries, err := os.ReadDir(c.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(c.pharFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: c.versionDir(v)})
			}
		}
	}
	return out
}

// DeleteVersion 删除某便携 Composer 版本目录。
func (c *ComposerRuntime) DeleteVersion(version string) error {
	dir := c.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

// Install 安装指定版本：直接下载 composer.phar 到 runtime/composer/<version>/（非 zip，无需解压）。
func (c *ComposerRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeComposer)[0]
	}
	dir := c.versionDir(version)
	phar := c.pharFor(version)
	if _, err := os.Stat(phar); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("Composer " + version + " 已安装: " + phar)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	urls := CandidateURLs(RuntimeComposer, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Composer 下载源")
	}
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Composer "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Composer " + version + "…")
	}
	if err := Download(ctx, phar, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Composer 失败: %w", err)
	}
	if fi, err := os.Stat(phar); err != nil || fi.Size() == 0 {
		return fmt.Errorf("下载完成但 composer.phar 无效（可能需 PHP 运行环境）")
	}
	// 生成 composer.bat（cmd/powershell）与 composer（git-bash/msys）启动器，
	// 把版本目录加进 PATH 后 `composer` 即可运行（前提是 php 也在 PATH）。
	if err := c.writeShims(version); err != nil {
		logger.W("[env][composer] 生成启动器失败: %v", err)
	} else if cb.OnLog != nil {
		cb.OnLog("Composer " + version + " 下载完成，已生成 composer.bat 启动器（需 php 在 PATH，运行：composer）")
	}
	return nil
}

// writeShims 在版本目录下生成 Windows 启动器 composer.bat 与类 Unix 启动器 composer，
// 两者均调用同目录的 composer.phar（需 php 在 PATH）。目录加进 PATH 后 `composer` 即可直跑。
func (c *ComposerRuntime) writeShims(version string) error {
	dir := c.versionDir(version)
	bat := filepath.Join(dir, "composer.bat")
	batContent := "@echo off\r\nphp \"%~dp0composer.phar\" %*\r\n"
	if err := os.WriteFile(bat, []byte(batContent), 0755); err != nil {
		return err
	}
	sh := filepath.Join(dir, "composer")
	shContent := "#!/bin/sh\nphp \"$(dirname \"$0\")/composer.phar\" \"$@\"\n"
	return os.WriteFile(sh, []byte(shContent), 0755)
}
