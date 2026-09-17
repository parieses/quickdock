package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"quickdock/internal/platform"
)

const jdkBaseRel = "runtime/jdk"

// JDKRuntime 管理便携 JDK（Adoptium Temurin 官方构建，单目录含 bin/java.exe）。
// 纯工具链型：仅下载/解压/版本切换，不支持服务（无端口、无 Web 后台）。
// 多版本并存于 runtime/jdk/<feature>（feature 即 8/11/17/21 主版本号）。
type JDKRuntime struct {
	baseDir string
}

func NewJDKRuntime() *JDKRuntime {
	return &JDKRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), jdkBaseRel)}
}

func (j *JDKRuntime) Kind() Runtime                { return RuntimeJDK }
func (j *JDKRuntime) DisplayName() string          { return DisplayName(RuntimeJDK) }
func (j *JDKRuntime) SupportedPlatforms() []string { return []string{"windows", "darwin"} }
func (j *JDKRuntime) Recommended() []string        { return Versions(RuntimeJDK) }

func (j *JDKRuntime) versionDir(version string) string {
	return filepath.Join(j.baseDir, version)
}

// ExeFor Extract 会剥离 zip 单层顶层目录（如 jdk-21.0.5+11/），故 java.exe 落在 <feature>/bin/java.exe。
func (j *JDKRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(j.versionDir(version), "bin", "java.exe")
	}
	return filepath.Join(j.versionDir(version), "bin", "java")
}

// DetectArgs 返回空切片：JDK 版本以 feature 号（8/11/17/21）作为目录名，不探测导入系统安装，
// 避免与系统「21.0.5」之类完整版本号在已装列表里出现重复条目（与 Erlang 同思路）。
func (j *JDKRuntime) DetectArgs() []string { return nil }

// ParseVersion 兜底解析（实际不会被导入路径调用，DetectArgs 为空）。
func (j *JDKRuntime) ParseVersion(out string) (string, error) {
	return "", fmt.Errorf("JDK 不支持版本探测导入")
}

func (j *JDKRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(j.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := e.Name()
		if _, err := os.Stat(j.ExeFor(v)); err != nil {
			continue
		}
		// Path 必须是 bin 目录（java.exe 所在目录），与 SetActive 注册的 exeDirFor(=Dir(ExeFor)) 保持一致：
		// 否则后端 binInSystemPath 拿版本根目录去比对 PATH，而实际写进 PATH 的是 <version>/bin，
		// 永远对不上 →「环境变量」列恒定显示未设置，表现为「设置环境变量没反应」（同 erlang.go 的处理）。
		out = append(out, Install{Version: v, Scope: "portable", Path: filepath.Dir(j.ExeFor(v))})
	}
	return out
}

func (j *JDKRuntime) DeleteVersion(version string) error {
	dir := j.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (j *JDKRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeJDK)[0]
	}
	dir := j.versionDir(version)
	if _, err := os.Stat(j.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("JDK " + version + " 已安装: " + j.ExeFor(version))
		}
		return nil
	}
	// 残留的半成品目录先清掉再装
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeJDK, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 JDK 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-jdk-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 JDK "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 JDK " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 JDK 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 JDK…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 JDK 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 JDK 失败: %w", err)
	}
	if _, err := os.Stat(j.ExeFor(version)); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", j.ExeFor(version))
	}
	if cb.OnLog != nil {
		cb.OnLog("JDK " + version + " 解压完成（feature 系列最新 GA，实际完整版本以 java -version 为准）")
	}
	return nil
}

// 编译期断言：确保 JDKRuntime 实现了 RuntimeAdapter 接口。
var _ RuntimeAdapter = (*JDKRuntime)(nil)
