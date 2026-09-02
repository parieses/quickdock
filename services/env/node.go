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
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/platform"
)

const (
	nodeDefaultVersion = "v22.22.2" // LTS，匹配本机托管运行时；要求 ^22.19.0 || >=24.0.0
	nodeRuntimeRel     = "runtime/node"
)

// InstallCallback Node 安装过程中的进度/日志回调
type InstallCallback struct {
	OnProgress func(written, total int64) // 字节进度（total 可能为 -1 表示未知）
	OnLog      func(msg string)           // 可读日志行
	OnStage    func(stage, msg string)   // 阶段：download | extract
}

// NodeRuntime 管理便携 Node 运行时：检测系统/便携 Node、按需从可切换下载源拉取并解压。
// 不依赖系统 PATH、不写注册表、不申请管理员权限——纯用户态、随 QuickDock 清理。
type NodeRuntime struct {
	dataDir    string
	runtimeDir string
	installing atomic.Bool
	mu         sync.Mutex
}

func NewNodeRuntime() *NodeRuntime {
	dataDir := platform.DefaultDataDir()
	return &NodeRuntime{
		dataDir:    dataDir,
		runtimeDir: filepath.Join(dataDir, nodeRuntimeRel),
	}
}

// RuntimeDir 便携 Node 目录（DSH 等需引用以拼 PATH）
func (n *NodeRuntime) RuntimeDir() string { return n.runtimeDir }

// Exe 返回 node 可执行文件绝对路径
func (n *NodeRuntime) Exe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(n.runtimeDir, "node.exe")
	}
	return filepath.Join(n.runtimeDir, "node")
}

// NpxExe 返回 npx 可执行文件绝对路径
func (n *NodeRuntime) NpxExe() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(n.runtimeDir, "npx.cmd")
	}
	return filepath.Join(n.runtimeDir, "npx")
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
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = hideWindowAttr()
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
	if _, err := os.Stat(n.Exe()); err == nil {
		if v := RunVersion(n.Exe(), "--version"); v != "" && VersionOK(v) {
			return n.Exe(), v, true
		}
	}
	return "", "", false
}

// Install 一键安装便携 Node：从可切换下载源拉取并解压到 runtimeDir。已就绪则跳过。
func (n *NodeRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = nodeDefaultVersion
	}
	if n.installing.Swap(true) {
		return fmt.Errorf("Node 安装正在进行中，请稍候")
	}
	defer n.installing.Store(false)

	if _, _, ok := n.Detect(); ok {
		if cb.OnLog != nil {
			cb.OnLog("Node 已就绪: " + n.Exe())
		}
		return nil
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
	zipPath := filepath.Join(os.TempDir(), "quickdock-node-"+version+".zip")
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Node 失败: %w", err)
	}
	defer os.Remove(zipPath)

	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Node…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Node 到 " + n.runtimeDir)
	}
	if err := Extract(zipPath, n.runtimeDir); err != nil {
		return fmt.Errorf("解压 Node 失败: %w", err)
	}
	if _, err := os.Stat(n.Exe()); err != nil {
		return fmt.Errorf("解压完成但未找到 %s，请删除 %s 后重试", n.Exe(), n.runtimeDir)
	}
	if cb.OnLog != nil {
		cb.OnLog("Node 解压完成")
	}
	return nil
}

// ---- RuntimeAdapter 接口实现 ----

func (n *NodeRuntime) Kind() Runtime { return RuntimeNode }

func (n *NodeRuntime) DisplayName() string { return DisplayName(RuntimeNode) }

func (n *NodeRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }

func (n *NodeRuntime) Recommended() []string { return Versions(RuntimeNode) }

// ExeFor 兼容 RuntimeAdapter 接口：node 为单版本目录（DSH 依赖 runtime/node 固定路径），忽略 version。
func (n *NodeRuntime) ExeFor(version string) string { return n.Exe() }

// InstalledVersions 返回已装的 Node（优先系统 PATH，其次便携目录；不满足 dsh 版本要求的系统 node 也列出）。
func (n *NodeRuntime) InstalledVersions() []Install {
	var out []Install
	if p, v, ok := n.Detect(); ok {
		scope := "system"
		if p == n.Exe() {
			scope = "portable"
		}
		out = append(out, Install{Version: strings.TrimPrefix(v, "v"), Scope: scope, Path: p})
	}
	// 也识别版本不满足 dsh 要求的系统 node（仅用于展示，避免误报「未安装」）
	if p, err := exec.LookPath("node"); err == nil {
		if v := RunVersion(p, "--version"); v != "" {
			dup := false
			for _, it := range out {
				if it.Path == p {
					dup = true
					break
				}
			}
			if !dup {
				out = append(out, Install{Version: strings.TrimPrefix(v, "v"), Scope: "system", Path: p})
			}
		}
	}
	return out
}

// DeleteVersion Node 为单版本目录且被 DSH 依赖，不允许在此删除，交由系统/DSH 管理。
func (n *NodeRuntime) DeleteVersion(version string) error {
	return fmt.Errorf("Node 由系统/DSH 管理，不支持在此删除")
}
