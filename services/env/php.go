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

	"quickdock/internal/platform"
)

const phpBaseRel = "runtime/php"

// phpFpmPort 是 PHP-FPM（Windows 下以 php-cgi 的 FastCGI 模式替代）监听的默认端口。
const phpFpmPort = 9000

// PHPRuntime 管理便携 PHP 运行时（Windows 官方二进制包），多版本共存于 runtime/php/<version>。
type PHPRuntime struct {
	baseDir    string
	linkedDirs map[string]string // version→导入目录，由 Manager 从 links.json 注入，使导入版 PHP 也能做 php-fpm 启停
	mu         sync.RWMutex
}

// SetLinkedDirs 注入导入版（linked）PHP 的版本→目录映射，使其在版本列表中可被 php-fpm 启停。
func (p *PHPRuntime) SetLinkedDirs(dirs map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.linkedDirs = dirs
}

// linkedInstalls 返回导入版 PHP 安装项（与 InstalledVersions 同构，仅 scope=linked）。
func (p *PHPRuntime) linkedInstalls() []Install {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Install, 0, len(p.linkedDirs))
	for v, dir := range p.linkedDirs {
		out = append(out, Install{Version: v, Scope: "linked", Path: dir})
	}
	return out
}

// allInstalls 合并便携/系统/导入版，供 php-fpm 启停与状态反查统一解析。
func (p *PHPRuntime) allInstalls() []Install {
	out := make([]Install, 0, len(p.InstalledVersions())+len(p.linkedDirs))
	out = append(out, p.InstalledVersions()...)
	out = append(out, p.linkedInstalls()...)
	return out
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

// ---- ServiceController：PHP-FPM（FastCGI）启停 / 状态 ----
// 说明：官方 windows.php.net 发布的 PHP 构建**不含** unix 版 php-fpm，
// 其 Windows 等价物是 php-cgi.exe 的 FastCGI 模式（php-cgi.exe -b 127.0.0.1:9000）。
// 因此 QuickDock 的「PHP-FPM」以该方式拉起，端口 9000；停止按端口 + 镜像名兜底。

func (p *PHPRuntime) DefaultPort() int { return phpFpmPort }

// fpmExePath 返回某安装项对应的 php-cgi.exe 绝对路径（便携=版本目录，系统=php.exe 同级目录）。
func fpmExePath(ins Install) string {
	if ins.Scope == "system" {
		return filepath.Join(filepath.Dir(ins.Path), "php-cgi.exe")
	}
	return filepath.Join(ins.Path, "php-cgi.exe")
}

func (p *PHPRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := p.allInstalls()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 PHP 版本")
		}
		version = installs[0].Version
	}
	var exe, wd string
	for _, ins := range installs {
		if ins.Version != version {
			continue
		}
		exe, wd = fpmExePath(ins), ins.Path
		if ins.Scope == "system" {
			wd = filepath.Dir(ins.Path)
		}
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("该 PHP 构建不含 php-cgi.exe，无法启动 PHP-FPM（FastCGI）")
	}
	// 端口固定 9000：已在运行其它版本时先停止，避免端口冲突导致两个实例都起不来
	if running, _ := svcMgr.info(RuntimePHP); running != "" && running != version {
		return fmt.Errorf("PHP-FPM 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if onLog != nil {
		onLog("启动 PHP-FPM " + version + " …")
	}
	return svcMgr.start(RuntimePHP, version, exe, wd, []string{"-b", "127.0.0.1:" + strconv.Itoa(phpFpmPort)}, "", onLog)
}

func (p *PHPRuntime) Stop(version string) error {
	// 端口兜底：杀掉占用 9000 且镜像确为 php-cgi.exe 的进程树；再清掉会话句柄
	stopByPort(phpFpmPort, "php-cgi.exe")
	svcMgr.forget(RuntimePHP)
	return nil
}

// runningVersion 返回当前 9000 端口实际在跑的 PHP-FPM 版本；无运行返回 ""。
func (p *PHPRuntime) runningVersion() string {
	if v, _ := svcMgr.info(RuntimePHP); v != "" {
		return v
	}
	if !isPortOpen(phpFpmPort) {
		return ""
	}
	pid := findListenPID(phpFpmPort)
	if pid == 0 {
		return ""
	}
	exe := processExePath(pid)
	if exe == "" {
		return ""
	}
	for _, ins := range p.allInstalls() {
		if strings.EqualFold(exe, fpmExePath(ins)) {
			return ins.Version
		}
	}
	return ""
}

func (p *PHPRuntime) Status(version string) ServiceStatus {
	st := ServiceStatus{Running: false, Port: phpFpmPort}
	if p.runningVersion() == version {
		st.Running = true
		st.Version = version
		st.PID = svcMgr.pid(RuntimePHP)
		if st.PID == 0 {
			st.PID = findListenPID(phpFpmPort)
		}
	}
	return st
}
