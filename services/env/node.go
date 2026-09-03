package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const (
	nodeDefaultVersion = "v22.22.2" // LTS，DSH 要求 ^22.19.0 || >=24.0.0
	nodeBaseRel        = "runtime/node"
)

// InstallCallback Node 安装过程中的进度/日志回调
type InstallCallback struct {
	OnProgress func(written, total int64) // 字节进度（total 可能为 -1 表示未知）
	OnLog      func(msg string)           // 可读日志行
	OnStage    func(stage, msg string)    // 阶段：download | extract
}

// NodeRuntime 管理便携 Node 运行时：多版本共存于 runtime/node/<version>，
// 同时探测系统 PATH 上已安装的 node。按需从可切换下载源拉取并解压。
// 不依赖系统 PATH、不写注册表、不申请管理员权限——纯用户态、随 QuickDock 清理。
type NodeRuntime struct {
	baseDir    string
	installing atomic.Bool
}

func NewNodeRuntime() *NodeRuntime {
	return &NodeRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), nodeBaseRel)}
}

// versionDir 便携 Node 的版本目录：runtime/node/<version>
func (n *NodeRuntime) versionDir(version string) string {
	return filepath.Join(n.baseDir, version)
}

// nodeExeIn 返回某目录下的 node 可执行文件（Windows 在根目录，其余在 bin/）。
func nodeExeIn(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "node.exe")
	}
	return filepath.Join(dir, "bin", "node")
}

// legacyExe 旧版单版本布局的可执行文件：runtime/node/node.exe。
// 升级前安装的便携 node 落在这里，仍要能被 DSH 找到，否则升级后 DSH 直接失效。
func (n *NodeRuntime) legacyExe() string { return nodeExeIn(n.baseDir) }

// RuntimeDir 返回 DSH 应使用的 node 所在目录（npm 全局 prefix 与 PATH 拼装基准）。
// 多版本下随 Exe() 一起解析，而非写死 baseDir。
func (n *NodeRuntime) RuntimeDir() string { return filepath.Dir(n.Exe()) }

// Exe 返回 DSH 应使用的 node 可执行文件，按优先级选择：
//  1. 旧版单版本布局 runtime/node/node.exe（升级前的安装，保持 DSH 可用）
//  2. 已装便携版本中满足 VersionOK 且版本最高的
//  3. 默认版本目录（尚未安装时，Install 会写入这里）
func (n *NodeRuntime) Exe() string {
	if p := n.legacyExe(); fileExists(p) {
		return p
	}
	best, bestVer := "", ""
	for _, it := range n.portableInstalls() {
		if !VersionOK(it.version) {
			continue
		}
		if bestVer == "" || semverLess(bestVer, it.version) {
			bestVer, best = it.version, it.exe
		}
	}
	if best != "" {
		return best
	}
	return nodeExeIn(n.versionDir(nodeDefaultVersion))
}

// NpxExe 返回与 Exe() 同目录下的 npx 可执行文件
func (n *NodeRuntime) NpxExe() string {
	dir := filepath.Dir(n.Exe())
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "npx.cmd")
	}
	return filepath.Join(dir, "npx")
}

// nodeInstall 一个已解压的便携版本
type nodeInstall struct {
	version string // 版本目录名（含 v 前缀，与下载/安装时传入的 version 一致）
	dir     string // 版本目录（Windows 上 node.exe 直接在其中，即 bin 目录）
	exe     string // node 可执行文件绝对路径
}

// portableInstalls 扫描 runtime/node/<version> 下的便携版本
func (n *NodeRuntime) portableInstalls() []nodeInstall {
	entries, err := os.ReadDir(n.baseDir)
	if err != nil {
		return nil
	}
	var out []nodeInstall
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := n.versionDir(e.Name())
		exe := nodeExeIn(dir)
		if !fileExists(exe) {
			continue
		}
		out = append(out, nodeInstall{version: e.Name(), dir: dir, exe: exe})
	}
	return out
}

// VersionOK 判断 node -v 输出是否满足要求：^22.19.0 || >=24.0.0
func VersionOK(version string) bool {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major == 22 {
		return minor >= 19
	}
	return major >= 24
}

// RunVersion 运行 exe --version 并返回去空白的输出；超时/失败返回空串。
// 使用 CombinedOutput 以兼容将版本信息输出到 stderr 的程序（如 nginx -v）。
func RunVersion(exe string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := sysutil.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Detect 返回 (path, version, ok)：优先系统 PATH（版本满足要求），其次便携目录。
func (n *NodeRuntime) Detect() (string, string, bool) {
	if p, err := exec.LookPath("node"); err == nil {
		if v := RunVersion(p, "--version"); v != "" && VersionOK(v) {
			return p, v, true
		}
	}
	if exe := n.Exe(); fileExists(exe) {
		if v := RunVersion(exe, "--version"); v != "" && VersionOK(v) {
			return exe, v, true
		}
	}
	return "", "", false
}

// Install 安装指定版本到 runtime/node/<version>。该版本已存在则跳过（换版本请指定 version）。
func (n *NodeRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeNode)[0]
	}
	if n.installing.Swap(true) {
		return fmt.Errorf("Node 安装正在进行中，请稍候")
	}
	defer n.installing.Store(false)

	dir := n.versionDir(version)
	exe := nodeExeIn(dir)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("Node " + version + " 已安装: " + exe)
		}
		return nil
	}
	// 残留的半成品目录先清掉再装
	if _, err := os.Stat(dir); err == nil {
		_ = os.RemoveAll(dir)
	}

	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Node "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Node " + version + "…")
	}
	urls := CandidateURLs(RuntimeNode, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Node 下载源")
	}
	ext := ".zip"
	if runtime.GOOS != "windows" {
		ext = ".tar.gz"
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-node-"+version+ext)
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Node 失败: %w", err)
	}
	defer os.Remove(zipPath)

	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Node…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Node 到 " + dir)
	}
	if err := Extract(zipPath, dir); err != nil {
		return fmt.Errorf("解压 Node 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s，请删除 %s 后重试", exe, dir)
	}
	if cb.OnLog != nil {
		cb.OnLog("Node " + version + " 解压完成")
	}
	return nil
}

// ---- RuntimeAdapter 接口实现 ----

func (n *NodeRuntime) Kind() Runtime { return RuntimeNode }

func (n *NodeRuntime) DisplayName() string { return DisplayName(RuntimeNode) }

func (n *NodeRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }

func (n *NodeRuntime) Recommended() []string { return Versions(RuntimeNode) }

// ExeFor 返回指定版本的 node 可执行文件（未安装时也返回预期路径，供 PATH 计算使用）。
func (n *NodeRuntime) ExeFor(version string) string {
	if version == "" {
		return n.Exe()
	}
	return nodeExeIn(n.versionDir(version))
}

// InstalledVersions 返回已装的 Node：便携多版本目录 + 旧版单版本目录 + 系统 PATH。
// 版本号保留 v 前缀（与下载源/安装时传入的格式一致），同版本只列一次。
func (n *NodeRuntime) InstalledVersions() []Install {
	var out []Install
	seen := map[string]bool{}
	for _, it := range n.portableInstalls() {
		seen[it.version] = true
		out = append(out, Install{Version: it.version, Scope: "portable", Path: it.dir})
	}
	// 旧版单版本布局（runtime/node/node.exe）：读出真实版本号列示，避免与新布局重复
	if p := n.legacyExe(); fileExists(p) {
		if v := RunVersion(p, "--version"); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, Install{Version: v, Scope: "portable", Path: n.baseDir})
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		if v := RunVersion(p, "--version"); v != "" && !seen[v] {
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return out
}

// DeleteVersion 删除某便携 Node 版本目录（runtime/node/<version>）。
func (n *NodeRuntime) DeleteVersion(version string) error {
	dir := n.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
