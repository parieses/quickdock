package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"quickdock/internal/platform"
)

const erlangBaseRel = "runtime/erlang"

// ErlangRuntime 管理便携 Erlang/OTP 运行时（erlang/otp 官方 GitHub 发布的 Windows 便携 zip，
// otp_win64_X.Y.Z.zip，解压即用、不写注册表、无需管理员权限）。多版本并存于 runtime/erlang/<version>。
// 它被 RabbitMQ 等依赖 Erlang 的运行时作为底层运行时复用：RabbitMQ 安装 / 启动前会通过
// findErlangHome() 优先复用本运行时已安装的 Erlang（或系统 ERLANG_HOME / PATH 上的 erl.exe）。
type ErlangRuntime struct {
	baseDir string
}

func NewErlangRuntime() *ErlangRuntime {
	return &ErlangRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), erlangBaseRel)}
}

func (e *ErlangRuntime) Kind() Runtime                 { return RuntimeErlang }
func (e *ErlangRuntime) DisplayName() string           { return DisplayName(RuntimeErlang) }
func (e *ErlangRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (e *ErlangRuntime) Recommended() []string        { return Versions(RuntimeErlang) }

func (e *ErlangRuntime) versionDir(version string) string {
	return filepath.Join(e.baseDir, version)
}

// ExeFor 指向 erl.exe（位于 OTP 根的 bin 目录）。Extract 会剥离 zip 单层顶层目录，
// 故 erl.exe 通常落在 <version>/bin/erl.exe；若 zip 直接铺开（无顶层目录）同样命中。
func (e *ErlangRuntime) ExeFor(version string) string {
	return filepath.Join(e.versionDir(version), "bin", "erl.exe")
}

// rootOf 返回某已安装版本的 OTP 根目录（含 bin/erl.exe）；找不到返回空。
func (e *ErlangRuntime) rootOf(version string) string {
	d := e.versionDir(version)
	if fileExists(filepath.Join(d, "bin", "erl.exe")) {
		return d
	}
	return findErlangRoot(d)
}

// FindRoot 返回已安装 Erlang 的 OTP 根目录，优先匹配 preferred 版本；无安装返回空。
// RabbitMQ 用它定位可复用的 Erlang（指令：先 QuickDock 管理的，再系统环境变量 / PATH）。
func (e *ErlangRuntime) FindRoot(preferred string) string {
	for _, ins := range e.InstalledVersions() {
		if ins.Version == preferred {
			if r := e.rootOf(ins.Version); r != "" {
				return r
			}
		}
	}
	for _, ins := range e.InstalledVersions() {
		if r := e.rootOf(ins.Version); r != "" {
			return r
		}
	}
	return ""
}

// FindRootByMajor 返回某主版本号（如 27）下任意已安装 Erlang 的 OTP 根目录；无则空。
// 用于 RabbitMQ 等依赖项：只要主版本落在官方支持区间即视为兼容（例如 RabbitMQ 4.3.5 需要 27.x）。
func (e *ErlangRuntime) FindRootByMajor(major int) string {
	if major == 0 {
		return ""
	}
	for _, ins := range e.InstalledVersions() {
		if parseMajor(ins.Version) == major {
			if r := e.rootOf(ins.Version); r != "" {
				return r
			}
		}
	}
	return ""
}

// parseMajor 返回版本号字符串的主版本号（如 "27.3.4" -> 27，"26.2.5" -> 26）。解析失败返回 0。
func parseMajor(v string) int {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	return n
}

// DetectArgs 返回空切片：Erlang 主要由 QuickDock 管理，版本即目录名，无需经二进制探测导入。
func (e *ErlangRuntime) DetectArgs() []string { return nil }

// ParseVersion 兜底解析（实际不会被导入路径调用）：兼容 "Erlang/OTP 27.3.4" 或 "version 27.3.4"。
func (e *ErlangRuntime) ParseVersion(out string) (string, error) {
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Count(v, ".") >= 1 {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeErlang))
}

func (e *ErlangRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(e.baseDir)
	if err != nil {
		return out
	}
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		if !fileExists(e.ExeFor(en.Name())) && findErlangRoot(e.versionDir(en.Name())) == "" {
			continue
		}
		// Path 必须是 bin 目录（erl.exe 所在目录），与 SetActive 注册的 exeDirFor(=Dir(ExeFor)) 保持一致：
		// 否则后端 binInSystemPath 拿 OTP 根目录去比对 PATH，而实际写进 PATH 的是 <version>/bin，
		// 永远对不上 → 列表/面板恒定显示“未写入 PATH”，表现为“设置不了环境变量”。
		out = append(out, Install{Version: en.Name(), Scope: "portable", Path: filepath.Dir(e.ExeFor(en.Name()))})
	}
	return out
}

func (e *ErlangRuntime) DeleteVersion(version string) error {
	dir := e.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	// Erlang 常驻守护进程 epmd.exe 会锁住自身所在目录，导致 os.RemoveAll 报
	// "Access is denied"。删除前先停掉属于该版本的 epmd（按镜像路径精确匹配，
	// 不会误杀服务其他版本 / RabbitMQ 的 epmd）。
	killEpmdBeforeDelete(dir)
	return os.RemoveAll(dir)
}

func (e *ErlangRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeErlang)[0]
	}
	dir := e.versionDir(version)
	exe := e.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("Erlang " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	urls := CandidateURLs(RuntimeErlang, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Erlang 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-erlang-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Erlang "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Erlang " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Erlang 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Erlang…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Erlang 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Erlang 失败: %w", err)
	}
	if e.rootOf(version) == "" {
		return fmt.Errorf("解压 Erlang 后未找到 bin/erl.exe（目录结构异常）")
	}
	if cb.OnLog != nil {
		cb.OnLog("Erlang " + version + " 解压完成")
	}
	return nil
}

// findErlangHome 定位任意可用的 Erlang OTP 根目录，按优先级：
//  1. QuickDock 管理的 Erlang 运行时（runtime/erlang/<ver>，含 bin/erl.exe）
//  2. 系统环境变量 ERLANG_HOME（需存在 bin/erl.exe）
//  3. PATH 上的 erl.exe（取其上一级 bin 的父目录）
//
// 返回空表示系统中不存在任何可用 Erlang——RabbitMQ 等依赖项必须先安装 Erlang。
func findErlangHome() string {
	if root := NewErlangRuntime().FindRoot(""); root != "" {
		return root
	}
	if home := os.Getenv("ERLANG_HOME"); home != "" {
		if fileExists(filepath.Join(home, "bin", "erl.exe")) {
			return home
		}
	}
	if p, err := exec.LookPath("erl.exe"); err == nil && p != "" {
		root := filepath.Dir(filepath.Dir(p))
		if fileExists(filepath.Join(root, "bin", "erl.exe")) {
			return root
		}
	}
	return ""
}

// findErlangRoot 在 base 目录下（含嵌套）查找包含 bin/erl.exe 的 OTP 根目录。
// Erlang 便携 zip 可能带单层顶层目录（otp_win64_X.Y.Z/）或直接铺开，均能正确定位。
func findErlangRoot(base string) string {
	if _, err := os.Stat(base); err != nil {
		return ""
	}
	stack := []string{base}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(cur)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(cur, e.Name())
			if e.IsDir() {
				if rel, err := filepath.Rel(base, p); err == nil {
					if strings.Count(rel, string(os.PathSeparator)) < 4 {
						stack = append(stack, p)
					}
				}
				continue
			}
			if e.Name() == "erl.exe" {
				return filepath.Dir(filepath.Dir(p)) // .../bin/erl.exe -> .../
			}
		}
	}
	return ""
}
