package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const ftpBaseRel = "runtime/ftp"

const ftpDefaultPort = 21
const ftpExeName = "ftpdmin.exe"

// FTPRuntime 管理便携 FTP 运行时（FTPDMIN，Matthias Wandel，public domain）。
// FTPDMIN 是单文件 ftpdmin.exe（~65KB），无安装、匿名登录（设计如此，无账号/密码），
// 适合临时文件传输。它完全靠命令行参数配置（无配置文件），故本运行时把「启动参数」
// 持久化到版本目录下的 ftpdmin.args（每行一个参数），并借此实现通用 ConfigProvider，
// 复用环境管理已有的「编辑配置」弹窗——用户只需在弹窗里写端口/根目录/只读即可。
//
// 命令行语法（来自官方文档）：ftpdmin [-p 端口] [-g 只读] [-tp 传输端口] [-ha 可达地址] [根目录]
// 注意 FTPDMIN 没有 --version 之类的版本查询参数，且直接运行即启动服务（会阻塞），
// 因此 InstalledVersions 只扫描便携目录、不调用 LookPath 探测系统 PATH 版本。
type FTPRuntime struct {
	baseDir string
}

func NewFTPRuntime() *FTPRuntime {
	return &FTPRuntime{baseDir: filepath.Join(platform.DefaultDataDir(), ftpBaseRel)}
}

func (f *FTPRuntime) Kind() Runtime                 { return RuntimeFTP }
// FTPDMIN 没有 --version 之类的版本查询参数，且直接运行即启动服务（会阻塞），
// 因此不能通过版本探测导入外部目录；InstalledVersions 也只扫描便携目录、不调用 LookPath。
func (f *FTPRuntime) DetectArgs() []string { return nil }
func (f *FTPRuntime) ParseVersion(out string) (string, error) {
	return "", fmt.Errorf("FTP 不支持通过目录导入")
}
func (f *FTPRuntime) DisplayName() string          { return DisplayName(RuntimeFTP) }
func (f *FTPRuntime) SupportedPlatforms() []string { return []string{"windows"} }
func (f *FTPRuntime) Recommended() []string        { return Versions(RuntimeFTP) }

func (f *FTPRuntime) versionDir(version string) string {
	return filepath.Join(f.baseDir, version)
}

func (f *FTPRuntime) ExeFor(version string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(f.versionDir(version), ftpExeName)
	}
	return filepath.Join(f.versionDir(version), "ftpdmin")
}

// rootDir 默认 FTP 共享根目录（匿名登录后看到的根），即本版本的 ftp-root 目录。
func (f *FTPRuntime) rootDir(version string) string {
	return filepath.Join(f.versionDir(version), "ftp-root")
}

// argsPath 返回启动参数配置文件路径（每行一个参数，便于包含空格的根目录路径）。
func (f *FTPRuntime) argsPath(version string) string {
	return filepath.Join(f.versionDir(version), "ftpdmin.args")
}

// ftpArgs 解析后的启动参数。
type ftpArgs struct {
	port    int      // 控制端口（默认 21）
	rootDir string   // 共享根目录（位置参数）
	raw     []string // 原始令牌，直接作为 ftpdmin 的启动参数
}

// ensureDefaultArgs 若启动参数文件不存在则写入默认内容（端口 21 + 默认根目录）。
func (f *FTPRuntime) ensureDefaultArgs(version string) {
	p := f.argsPath(version)
	if _, err := os.Stat(p); err == nil {
		return
	}
	content := "-p\n" + strconv.Itoa(ftpDefaultPort) + "\n" + f.rootDir(version) + "\n"
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		logger.W("[env][ftp] 写入默认启动参数失败: %v", err)
	}
}

// loadArgs 读取并解析 ftpdmin.args。文件缺失时退回内存默认值（不写盘）。
// 语法：每行一个令牌；-p/-tp/-ha 后跟一个值；-g 为开关（无值）；
// 其余不以 - 开头的令牌视为根目录（位置参数）。默认端口 21、默认根目录为 ftp-root。
func (f *FTPRuntime) loadArgs(version string) ftpArgs {
	a := ftpArgs{port: ftpDefaultPort}
	data, err := os.ReadFile(f.argsPath(version))
	if err != nil {
		a.rootDir = f.rootDir(version)
		a.raw = []string{"-p", strconv.Itoa(ftpDefaultPort), f.rootDir(version)}
		return a
	}
	// 逐行解析：每行通常是一个「逻辑参数」。ftpdmin 的 -p/-tp/-ha 需要紧跟一个值；
	// 若用户写成 "-p 21"（旗标与值同一行，常见但非每行一参数），这里兼容拆成两个令牌。
	// 不以 - 开头且含空格的行（如带空格的目录路径）保持原样，作为单个位置参数。
	knownVal := map[string]bool{"-p": true, "-tp": true, "-ha": true}
	var tokens []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.IndexByte(line, ' '); i > 0 && knownVal[line[:i]] {
			tokens = append(tokens, line[:i], strings.TrimSpace(line[i+1:]))
			continue
		}
		tokens = append(tokens, line)
	}
	a.raw = tokens
	for i := 0; i < len(tokens); i++ {
		tk := tokens[i]
		switch {
		case knownVal[tk]:
			// -p/-tp/-ha 后跟一个值令牌，消费后跳过，避免被误判为根目录
			if i+1 < len(tokens) {
				if tk == "-p" {
					if p, e := strconv.Atoi(tokens[i+1]); e == nil {
						a.port = p
					}
				}
				i++
			}
		case tk == "-g":
			// 只读开关，无值
		case !strings.HasPrefix(tk, "-"):
			a.rootDir = tk
		}
	}
	if a.rootDir == "" {
		a.rootDir = f.rootDir(version)
	}
	return a
}

func (f *FTPRuntime) InstalledVersions() []Install {
	var out []Install
	// 不调用 exec.LookPath("ftpdmin") 探测系统 PATH 版本——FTPDMIN 无 --version，
	// LookPath 命中后会直接启动一个 FTP 服务进程（阻塞），故仅扫描便携目录。
	if entries, err := os.ReadDir(f.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := e.Name()
			if _, err := os.Stat(f.ExeFor(v)); err == nil {
				out = append(out, Install{Version: v, Scope: "portable", Path: f.versionDir(v)})
			}
		}
	}
	return out
}

func (f *FTPRuntime) DeleteVersion(version string) error {
	dir := f.versionDir(version)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("未找到该版本: %s", version)
	}
	return os.RemoveAll(dir)
}

func (f *FTPRuntime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeFTP)[0]
	}
	dir := f.versionDir(version)
	if _, err := os.Stat(f.ExeFor(version)); err == nil {
		if cb.OnLog != nil {
			cb.OnLog("FTP " + version + " 已安装: " + f.ExeFor(version))
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	urls := CandidateURLs(RuntimeFTP, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 FTP 下载源")
	}
	// FTPDMIN 为单文件 exe，直接下载到目标路径（无需解压）。
	exePath := f.ExeFor(version)
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 FTP "+version+"…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 FTPDMIN " + version + "…")
	}
	if err := Download(ctx, exePath, urls, cb.OnProgress); err != nil {
		os.Remove(exePath)
		return fmt.Errorf("下载 FTP 失败: %w", err)
	}
	// 确保共享根目录与默认启动参数文件存在。
	if err := os.MkdirAll(f.rootDir(version), 0755); err != nil {
		logger.W("[env][ftp] 创建 FTP 根目录失败: %v", err)
	}
	f.ensureDefaultArgs(version)
	if cb.OnLog != nil {
		cb.OnLog("FTP " + version + " 下载完成（单文件 ftpdmin.exe）；启动参数见「编辑配置」")
	}
	return nil
}

// ---- ServiceController ----

func (f *FTPRuntime) DefaultPort() int { return ftpDefaultPort }

func (f *FTPRuntime) Start(ctx context.Context, version string, onLog func(string)) error {
	installs := f.InstalledVersions()
	if version == "" {
		if len(installs) == 0 {
			return fmt.Errorf("请先安装 FTP 版本")
		}
		version = installs[0].Version
	}
	var exe, wd string
	for _, ins := range installs {
		if ins.Version != version {
			continue
		}
		exe, wd = f.ExeFor(version), f.versionDir(version)
		break
	}
	if exe == "" {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("未安装该版本: %s", version)
	}
	// 确保启动参数文件与根目录存在（文件缺失时写默认）。
	f.ensureDefaultArgs(version)
	args := f.loadArgs(version)
	if err := os.MkdirAll(args.rootDir, 0755); err != nil {
		return fmt.Errorf("创建 FTP 根目录失败: %w", err)
	}
	running, _ := svcMgr.info(RuntimeFTP)
	if running != "" && running != version {
		return fmt.Errorf("FTP 已在运行（%s），请先停止当前版本再启动 %s", running, version)
	}
	if running == "" && isPortOpen(args.port) {
		logger.W("[env][ftp] Start 拒绝：端口 %d 已被占用", args.port)
		return fmt.Errorf("端口 %d 已被占用，请先释放该端口再启动 FTP（可在「编辑配置」中改端口）", args.port)
	}
	if onLog != nil {
		onLog(fmt.Sprintf("启动 FTP %s（端口 %d，根目录 %s）…", version, args.port, args.rootDir))
	}
	// 参数来自 ftpdmin.args：[-p 端口] [-g] [-tp ...] [-ha ...] [根目录]，直接透传给 ftpdmin。
	return svcMgr.start(RuntimeFTP, version, exe, wd, args.raw, "", onLog)
}

func (f *FTPRuntime) Stop(version string) error {
	args := f.loadArgs(version)
	stopByPort(args.port, ftpExeName)
	svcMgr.forget(RuntimeFTP)
	return nil
}

func (f *FTPRuntime) Status(version string) ServiceStatus {
	args := f.loadArgs(version)
	port := args.port
	st := ServiceStatus{Running: false, Port: port}
	if v, _ := svcMgr.info(RuntimeFTP); v != "" {
		st.Running = true
		st.Version = v
		st.PID = svcMgr.pid(RuntimeFTP)
		if st.PID == 0 {
			st.PID = findListenPID(port)
		}
		return st
	}
	if isPortOpen(port) {
		if pid := findListenPID(port); pid != 0 {
			exe := processExePath(pid)
			if exe == "" || strings.EqualFold(filepath.Base(exe), ftpExeName) {
				st.Running = true
				st.Version = version
				st.PID = pid
			}
		}
	}
	return st
}

// ---- ConfigProvider ----
// FTPDMIN 没有配置文件，启动参数全部来自命令行；这里把「启动参数」持久化到 ftpdmin.args，
// 复用环境管理统一的「编辑配置」弹窗。后端 ReadConfig/WriteConfig 按文件路径读写即可，
// 无需为 FTP 单独写一套 API 与前端。

// ConfigPath 返回某版本启动参数文件路径，实现通用 ConfigProvider 接口。
func (f *FTPRuntime) ConfigPath(version string) string {
	return f.argsPath(version)
}

var _ ConfigProvider = (*FTPRuntime)(nil)
