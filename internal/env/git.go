package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const gitBaseRel = "runtime/git"

// GitRuntime 管理便携 Git 运行时（git-for-windows 的 MinGit 发行包）。
// 与其他运行时不同，Git 为**单版本**：始终安装到 runtime/git，装新版本即覆盖旧版本
// （Git 版本间兼容性好，日常开发只需一份；多版本并存会让 PATH 切换变成负担）。
// 同时探测系统 PATH 上已安装的 git（如 Git for Windows）。
type GitRuntime struct {
	dir string // runtime/git
}

func NewGitRuntime() *GitRuntime {
	return &GitRuntime{dir: filepath.Join(platform.DefaultDataDir(), gitBaseRel)}
}

func (g *GitRuntime) Kind() Runtime        { return RuntimeGit }
func (g *GitRuntime) DetectArgs() []string { return []string{"--version"} }
func (g *GitRuntime) ParseVersion(out string) (string, error) {
	// 走与 InstalledVersions 相同的归一化逻辑，保证「导入的系统 git」与
	// 「可下载版本列表」里的版本号格式一致（否则已安装判定会因 .windows.N 后缀失配）。
	if v := parseGitVersion(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeGit))
}
func (g *GitRuntime) DisplayName() string          { return DisplayName(RuntimeGit) }
func (g *GitRuntime) SupportedPlatforms() []string { return []string{"windows", "linux", "darwin"} }
func (g *GitRuntime) Recommended() []string        { return Versions(RuntimeGit) }

// ExtraPathDirs 除 cmd/（git.exe 所在）外，还需注册进 PATH 的目录。
//
// Git for Windows 自带 usr/bin，里面有 uname/tail/sort 等 Unix 工具，构建脚本常直接调用；
// 只注册 cmd/ 会让这类脚本报 "uname: executable file not found"。
// 目录不存在（如精简版 MinGit 没有 usr/）时返回 nil，不做无效注册。
func (g *GitRuntime) ExtraPathDirs() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	dir := filepath.Join(g.dir, "usr", "bin")
	if !fileExists(filepath.Join(dir, "uname.exe")) {
		return nil
	}
	return []string{dir}
}

// cmdDir 返回含 git 可执行文件的目录：Windows 用 cmd/，其余用 bin/。该目录即写入系统 PATH 的条目。
func (g *GitRuntime) cmdDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.dir, "cmd")
	}
	return filepath.Join(g.dir, "bin")
}

// ExeFor 单版本：忽略 version，始终返回固定目录下的 git。
func (g *GitRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(g.cmdDir(), "git.exe")
	}
	return filepath.Join(g.cmdDir(), "git")
}

// legacyExeFor 旧版多版本布局 runtime/git/<version>/cmd/git.exe（升级前安装的）。
// 仍要能被识别，否则用户升级后旧目录变成既看不见也删不掉的垃圾。
func (g *GitRuntime) legacyExeFor(version string) string {
	base := filepath.Join(g.dir, version)
	if runtime.GOOS == "windows" {
		return filepath.Join(base, "cmd", "git.exe")
	}
	return filepath.Join(base, "bin", "git")
}

// InstalledVersions 便携目录（runtime/git）+ 历史多版本子目录 + 系统 PATH。
func (g *GitRuntime) InstalledVersions() []Install {
	var out []Install
	seen := map[string]bool{}
	dirs := managedDirs{}
	if p := g.ExeFor(""); fileExists(p) {
		if v := parseGitVersion(RunVersion(p, "--version")); v != "" {
			seen[v] = true
			out = append(out, Install{Version: v, Scope: "portable", Path: g.cmdDir()})
			dirs.record(filepath.Dir(p))
		}
	}
	if entries, err := os.ReadDir(g.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			exe := g.legacyExeFor(e.Name())
			if !fileExists(exe) {
				continue
			}
			seen[e.Name()] = true
			out = append(out, Install{Version: e.Name(), Scope: "portable", Path: filepath.Dir(exe)})
			dirs.record(filepath.Dir(exe))
		}
	}
	if p, err := exec.LookPath("git"); err == nil {
		if v := parseGitVersion(RunVersion(p, "--version")); v != "" && !seen[v] {
			// LookPath 命中本就由 QuickDock 托管并写入 PATH 的便携版时，不再重复登记为 system。
			if dirs.dedupeByDir(p) {
				return promoteEffectiveGit(out)
			}
			out = append(out, Install{Version: v, Scope: "system", Path: p})
		}
	}
	return promoteEffectiveGit(out)
}

// promoteEffectiveGit 把 PATH 中真正生效的 git 排到列表首位。
// 前端「当前版本」取 installed[0]，而 InstalledVersions 天然是「便携版在前、系统版在后」；
// 当系统 PATH 上另有一份更新的 git（Git for Windows、或别的软件自带的 PortableGit）时，
// 不排序就会一直展示便携版旧版本号，和终端里 `git --version` 对不上，
// 看起来就像"我的 git 已经很新了，软件还在提示我安装新版本"。
func promoteEffectiveGit(in []Install) []Install {
	if len(in) < 2 {
		return in
	}
	p, err := exec.LookPath("git")
	if err != nil {
		return in
	}
	exe := strings.ToLower(filepath.Clean(p))
	dir := strings.ToLower(filepath.Clean(filepath.Dir(p)))
	for i := range in {
		c := strings.ToLower(filepath.Clean(in[i].Path))
		if c == exe || c == dir { // system 存 exe 全路径；portable 存 bin 目录
			if i > 0 {
				in[0], in[i] = in[i], in[0]
			}
			break
		}
	}
	return in
}

// DeleteVersion 单版本语义：删除整个 runtime/git（含所有配套文件），彻底卸载。
// 历史多版本子目录（升级前安装的遗留）单独清理。系统 PATH 上的 git 由前端禁用删除（scope=system）。
func (g *GitRuntime) DeleteVersion(version string) error {
	// 历史多版本子目录 runtime/git/<version>/（升级前安装的遗留）
	if _, err := os.Stat(g.legacyExeFor(version)); err == nil {
		return os.RemoveAll(filepath.Join(g.dir, version))
	}
	// 单版本主目录 runtime/git/（始终删除整个托管目录，以便彻底卸载）
	if fileExists(g.ExeFor("")) {
		if err := os.RemoveAll(g.dir); err != nil {
			return fmt.Errorf("删除 Git 失败: %w", err)
		}
		return nil
	}
	return fmt.Errorf("未找到该版本: %s", version)
}

func (g *GitRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeGit)[0]
	}
	exe := g.ExeFor("")
	if fileExists(exe) {
		if cur := parseGitVersion(RunVersion(exe, "--version")); cur == version {
			if cb.OnLog != nil {
				cb.OnLog("Git " + version + " 已安装: " + exe)
			}
			return nil
		}
		if cb.OnLog != nil {
			cb.OnLog("Git 为单版本安装，覆盖更新为 " + version)
		}
	}
	// 单版本：整目录重装，避免新旧文件混用
	if err := os.RemoveAll(g.dir); err != nil {
		return fmt.Errorf("清理旧版本失败: %w", err)
	}
	urls := CandidateURLs(RuntimeGit, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 Git 下载源")
	}
	zipPath := filepath.Join(os.TempDir(), "quickdock-git-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 Git "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 Git " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 Git 失败: %w", err)
	}
	defer os.Remove(zipPath)
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 Git…")
	}
	if cb.OnLog != nil {
		cb.OnLog("解压 Git 到 " + g.dir)
	}
	if err := Extract(zipPath, g.dir); err != nil {
		return fmt.Errorf("解压 Git 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("解压完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("Git " + version + " 解压完成")
	}
	return nil
}

// parseGitVersion 从 `git --version` 输出里取纯版本号。
// 输入可能是 "git version 2.45.0"、"git version 2.45.0.windows.1" 乃至
// "git version 2.55.0.windows.3"（Git for Windows 的大版本补丁号）。
// 必须剥掉任意 .windows.N 后缀：下载列表里登记的是纯版本号（"2.55.0"），
// 若这里留下后缀，字符串比较就会判定"该版本未安装"，进而永远提示安装新版本。
func parseGitVersion(out string) string {
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "version") {
			continue
		}
		v := strings.TrimPrefix(tok, "git")
		if v == "" || !strings.Contains(v, ".") {
			continue
		}
		if i := strings.LastIndex(v, ".windows."); i >= 0 {
			v = v[:i]
		}
		return v
	}
	return ""
}

// GitStatusInfo 描述当前 Git 环境的综合状态（版本/路径/SSH/Git LFS），供前端状态表展示。
type GitStatusInfo struct {
	Installed bool       `json:"installed"`
	Version   string     `json:"version"`
	Path      string     `json:"path"`
	SSH       GitSSHInfo `json:"ssh"`
	LFS       GitLFSInfo `json:"lfs"`
	UpdatedAt int64      `json:"updatedAt"`
}

// GitSSHInfo SSH 连通性探测结果。
type GitSSHInfo struct {
	Available bool   `json:"available"` // ssh 命令是否可用
	KeyExists bool   `json:"keyExists"` // ~/.ssh 下是否存在私钥
	KeyPath   string `json:"keyPath"`
	Result    string `json:"result"` // "ok" | "no-key" | "n/a" | "fail"
	Command   string `json:"command"`
	Output    string `json:"output"`
}

// GitLFSInfo Git LFS 安装探测结果。
type GitLFSInfo struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Command   string `json:"command"`
	Output    string `json:"output"`
}

// Status 探测当前 Git 环境的版本、路径、SSH 与 Git LFS 状态。
// 与多版本运行时不同，Git 为单版本，因此这份状态表即代表“当前 Git”。
func (g *GitRuntime) Status() GitStatusInfo {
	info := GitStatusInfo{UpdatedAt: time.Now().Unix()}
	if installs := g.InstalledVersions(); len(installs) > 0 {
		info.Installed = true
		info.Version = installs[0].Version
		info.Path = installs[0].Path
	} else if p, err := exec.LookPath("git"); err == nil {
		info.Installed = true
		info.Version = parseGitVersion(RunVersion(p, "--version"))
		info.Path = p
	}
	info.SSH = g.detectSSH()
	info.LFS = g.detectLFS()
	return info
}

// detectSSH 探测 SSH 私钥与到 GitHub 的连通性（短超时，避免阻塞状态查询）。
func (g *GitRuntime) detectSSH() GitSSHInfo {
	ssh := GitSSHInfo{Command: "ssh -T git@github.com"}
	if _, err := exec.LookPath("ssh"); err != nil {
		ssh.Result = "n/a"
		ssh.Output = "ssh 命令不可用"
		return ssh
	}
	ssh.Available = true

	home, _ := os.UserHomeDir()
	keyDir := filepath.Join(home, ".ssh")
	for _, name := range []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "id_ed25519_sk", "id_ecdsa_sk"} {
		p := filepath.Join(keyDir, name)
		if _, err := os.Stat(p); err == nil {
			ssh.KeyExists = true
			ssh.KeyPath = p
			break
		}
	}
	if !ssh.KeyExists {
		ssh.Result = "no-key"
		ssh.Output = "未找到 SSH 私钥（~/.ssh 下无 id_*）"
		return ssh
	}
	// 仅做本地能力判定（不联网探测 GitHub），避免打开 Git 页时网络阻塞；
	// 手动连通性验证命令见 Command 列（ssh -T git@github.com）。
	ssh.Result = "ok"
	ssh.Output = "已配置 SSH 私钥：" + ssh.KeyPath
	return ssh
}

// detectLFS 探测 Git LFS 是否可用（git lfs version）。
func (g *GitRuntime) detectLFS() GitLFSInfo {
	lfs := GitLFSInfo{Command: "git lfs version"}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := sysutil.CommandContext(ctx, "git", "lfs", "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		lfs.Output = strings.TrimSpace(string(out))
		return lfs
	}
	text := strings.TrimSpace(string(out))
	lfs.Installed = true
	if i := strings.Index(text, "git-lfs/"); i >= 0 {
		v := text[i+8:]
		if sp := strings.IndexAny(v, " ("); sp >= 0 {
			v = v[:sp]
		}
		lfs.Version = v
	} else {
		lfs.Version = text
	}
	lfs.Output = text
	return lfs
}
