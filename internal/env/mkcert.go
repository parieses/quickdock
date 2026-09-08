package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

const mkcertBaseRel = "runtime/mkcert"

// MkcertRuntime 管理便携 mkcert 运行时（FiloSottile/mkcert），单文件 exe（非 zip）。
// 无服务、无配置文件、无数据目录，纯版本管理 + PATH 切换。
type MkcertRuntime struct {
	baseDir string
}

func NewMkcertRuntime() *MkcertRuntime {
	return &MkcertRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), mkcertBaseRel)}
}

func (m *MkcertRuntime) Kind() Runtime        { return RuntimeMkcert }
func (m *MkcertRuntime) DetectArgs() []string { return []string{"-version"} }
func (m *MkcertRuntime) ParseVersion(out string) (string, error) {
	// 输出形如 "v1.4.4" 或 "mkcert v1.4.4"
	for _, tok := range strings.Fields(out) {
		if v := strings.TrimPrefix(tok, "v"); v != "" && strings.Contains(v, ".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("无法识别 %s 版本", DisplayName(RuntimeMkcert))
}
func (m *MkcertRuntime) DisplayName() string          { return DisplayName(RuntimeMkcert) }
func (m *MkcertRuntime) SupportedPlatforms() []string { return []string{"windows", "darwin"} }
func (m *MkcertRuntime) Recommended() []string        { return Versions(RuntimeMkcert) }

func (m *MkcertRuntime) versionDir(version string) string { return filepath.Join(m.baseDir, version) }

func (m *MkcertRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(m.versionDir(version), "mkcert.exe")
	}
	return filepath.Join(m.versionDir(version), "mkcert")
}

func (m *MkcertRuntime) InstalledVersions() []Install {
	var out []Install
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !fileExists(m.ExeFor(e.Name())) {
			continue
		}
		out = append(out, Install{Version: e.Name(), Scope: "portable", Path: m.versionDir(e.Name())})
	}
	return out
}

func (m *MkcertRuntime) DeleteVersion(version string) error {
	dir := m.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

// Install 下载单文件 exe 直接落到 ExeFor 路径（mkcert 非 zip，无解压步骤）。
func (m *MkcertRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeMkcert)[0]
	}
	dir := m.versionDir(version)
	exe := m.ExeFor(version)
	if fileExists(exe) {
		if cb.OnLog != nil {
			cb.OnLog("mkcert " + version + " 已安装: " + exe)
		}
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	urls := CandidateURLs(RuntimeMkcert, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 mkcert 下载源")
	}
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 mkcert "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 mkcert " + version + "…")
	}
	if err := Download(ctx, exe, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 mkcert 失败: %w", err)
	}
	if !fileExists(exe) {
		return fmt.Errorf("下载完成但未找到 %s", exe)
	}
	if cb.OnLog != nil {
		cb.OnLog("mkcert " + version + " 安装完成")
	}
	return nil
}

// ==================== 基于 mkcert 的本地可信证书签发 ====================
// 以下为 *Manager 上的证书操作封装（调用已安装的 mkcert 执行），与上文的
// MkcertRuntime 版本管理同属 mkcert 能力，故收敛在本文件。接收者用 *Manager
// 是因为签发需经 manager 定位已装/激活的 mkcert 可执行文件。

// certHostRe 校验待签发域名/主机名：仅允许字母数字、点、中划线、下划线与通配符前缀。
// 用于 mkcert 命令参数白名单校验，杜绝把用户输入当参数拼接导致的注入。
var certHostRe = regexp.MustCompile(`^(?:\*\.)?[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// certNameRe 校验证书文件名（仅字母数字、点、中划线、下划线）。
var certNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// MkcertExe 返回可用于签发证书的 mkcert 可执行文件路径。
// 优先取激活版本；无激活则取任一所装版本；未安装返回 error 提示先安装。
func (m *Manager) MkcertExe() (string, error) {
	a, err := m.adapter(RuntimeMkcert)
	if err != nil {
		return "", err
	}
	mk, ok := a.(*MkcertRuntime)
	if !ok {
		return "", fmt.Errorf("mkcert 运行时类型异常")
	}
	if v := activeVersion(RuntimeMkcert); v != "" && fileExists(mk.ExeFor(v)) {
		return mk.ExeFor(v), nil
	}
	insts, err := m.InstalledVersions(RuntimeMkcert)
	if err == nil && len(insts) > 0 {
		for _, it := range insts {
			if fileExists(mk.ExeFor(it.Version)) {
				return mk.ExeFor(it.Version), nil
			}
		}
	}
	return "", fmt.Errorf("未检测到已安装的 mkcert，请先在「mkcert」运行时安装一个版本")
}

// CertCARootTrusted 探测 mkcert 本地根 CA 是否已安装到系统信任。
// 通过执行 `mkcert -install` 的输出判断：已安装时 mkcert 会提示本地 CA 已存在。
func (m *Manager) CertCARootTrusted() (bool, error) {
	exe, err := m.MkcertExe()
	if err != nil {
		return false, err
	}
	out, err := runCertCmd(exe, "-install")
	if err != nil {
		return false, err
	}
	low := strings.ToLower(out)
	// mkcert 对已安装根会输出类似 "The local CA is already installed"；未安装则执行安装。
	// 统一视为「信任就绪」（install 幂等：未装则装上，已装则提示已存在）。
	return !strings.Contains(low, "error") && !strings.Contains(low, "fail"), nil
}

// CertInstallRoot 信任 mkcert 本地根 CA（等价 `mkcert -install`，幂等）。
// Windows 写入系统/用户根证书存储，可能触发 UAC 或需要管理员权限；失败返回原始输出便于定位。
func (m *Manager) CertInstallRoot() error {
	exe, err := m.MkcertExe()
	if err != nil {
		return err
	}
	out, err := runCertCmd(exe, "-install")
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(out))
	}
	return nil
}

// CertIssue 用 mkcert 为 hosts 签发本地可信证书到 outDir。
// 证书文件名 <name>-cert.pem、私钥 <name>-key.pem（name 默认 "localhost"，可含在主名里）。
// 返回 map{ cert, key } 的绝对路径。hosts 需逐个通过白名单校验。
func (m *Manager) CertIssue(outDir, name string, hosts []string) (map[string]string, error) {
	exe, err := m.MkcertExe()
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "localhost"
	}
	name = filepath.Base(name) // 防路径穿越
	if !certNameRe.MatchString(name) {
		return nil, fmt.Errorf("证书文件名不合法: %s", name)
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	for _, h := range hosts {
		if !certHostRe.MatchString(h) {
			return nil, fmt.Errorf("待签发域名不合法: %s", h)
		}
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("创建证书目录失败: %w", err)
	}
	certPath := filepath.Join(outDir, name+"-cert.pem")
	keyPath := filepath.Join(outDir, name+"-key.pem")
	args := []string{"-cert-file", certPath, "-key-file", keyPath}
	args = append(args, hosts...)
	out, err := runCertCmd(exe, args...)
	if err != nil {
		return nil, fmt.Errorf("签发失败: %s", strings.TrimSpace(out))
	}
	logger.I("[env] mkcert 签发完成 cert=%s key=%s hosts=%v", certPath, keyPath, hosts)
	return map[string]string{"cert": certPath, "key": keyPath}, nil
}

// runCertCmd 以隐藏控制台方式执行一次 mkcert 命令并捕获合并输出，带 60s 超时。
// 全程走 sysutil（项目硬约束：exec.Command 拉起控制台程序必须经 sysutil）。
func runCertCmd(exe string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := sysutil.CommandContext(ctx, exe, args...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), fmt.Errorf("命令执行超时")
	}
	return out.String(), err
}
