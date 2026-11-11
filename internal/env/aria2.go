package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quickdock/internal/aria2"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

const aria2BaseRel = "runtime/aria2"

// reAria2Version `aria2c --version` 输出形如 "aria2 version 1.37.0"。
var reAria2Version = regexp.MustCompile(`(\d+\.\d+\.\d+)`)

// Aria2Runtime 托管 aria2c 可执行文件：单版本语义（解压到 runtime/aria2），
// 以 daemon 形态常驻，QuickDock 通过 JSON-RPC 发指令。文件字节由 aria2c 自己写盘。
type Aria2Runtime struct {
	baseDir string
}

func NewAria2Runtime() *Aria2Runtime {
	return &Aria2Runtime{baseDir: filepath.Join(platform.DefaultDataDir(), aria2BaseRel)}
}

func (a *Aria2Runtime) Kind() Runtime       { return RuntimeAria2 }
func (a *Aria2Runtime) DisplayName() string { return DisplayName(RuntimeAria2) }
func (a *Aria2Runtime) DetectArgs() []string {
	return []string{"--version"}
}

func (a *Aria2Runtime) ParseVersion(out string) (string, error) {
	if v := reAria2Version.FindString(out); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("无法识别 aria2 版本")
}

// 官方 Windows 便携 zip 无需额外 DLL。macOS/Linux 走系统包管理器（brew/apt）自行安装，
// 本适配器只做探测与接管，不托管下载，故 SupportedPlatforms 只声明 windows。
func (a *Aria2Runtime) SupportedPlatforms() []string { return []string{"windows"} }
func (a *Aria2Runtime) Recommended() []string        { return Versions(RuntimeAria2) }

func (a *Aria2Runtime) exeName() string {
	if runtime.GOOS == "windows" {
		return "aria2c.exe"
	}
	return "aria2c"
}

// ExeFor 单版本语义：忽略 version，目录恒为 runtime/aria2。
func (a *Aria2Runtime) ExeFor(version string) string {
	return filepath.Join(a.baseDir, a.exeName())
}

func (a *Aria2Runtime) LogPath(version string) string {
	return filepath.Join(a.baseDir, "aria2.log")
}

func (a *Aria2Runtime) InstalledVersions() []Install {
	var out []Install
	dirs := managedDirs{}
	if _, err := os.Stat(a.ExeFor("")); err == nil {
		v := reAria2Version.FindString(RunVersion(a.ExeFor(""), "--version"))
		if v == "" {
			v = "unknown"
			if b, e := os.ReadFile(filepath.Join(a.baseDir, "VERSION")); e == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					v = s
				}
			}
		}
		out = append(out, Install{Version: v, Scope: "portable", Path: a.baseDir})
		// 登记的是 exe 所在目录，不是 Install.Path：portable 的 Path 本身是目录，
		// system 的 Path 是 exe 全路径，早期拿两边各套一次 filepath.Dir 会错一层
		// （目录的 Dir 是父目录），去重恒不命中，同一份 1.37.0 会显示「便携 + 系统」两行。
		dirs.record(filepath.Dir(a.ExeFor("")))
	}
	if p, err := exec.LookPath("aria2c"); err == nil {
		v := reAria2Version.FindString(RunVersion(p, "--version"))
		if v == "" {
			return out
		}
		// 便携版被写进 PATH 时 LookPath 会命中自己，别重复登记成 system。
		// 与 nginx/ffmpeg/go 等适配器统一走 managedDirs 范式。
		if dirs.dedupeByDir(p) {
			return out
		}
		out = append(out, Install{Version: v, Scope: "system", Path: p})
	}
	return out
}

func (a *Aria2Runtime) DeleteVersion(version string) error {
	if _, err := os.Stat(a.baseDir); err != nil {
		return fmt.Errorf("未安装 aria2")
	}
	return os.RemoveAll(a.baseDir)
}

// Install 从官方 Release 下载 Windows 便携 zip（约 2.4 MB）并解压。
// 归档顶层是单一目录 aria2-<version>-win-64bit-build1/，Extract 会自动剥离，
// 解压后 aria2c.exe 直接落在 runtime/aria2 下。
func (a *Aria2Runtime) Install(ctx context.Context, version string, cb InstallCallback) error {
	if version == "" {
		version = Versions(RuntimeAria2)[0]
	}
	urls := CandidateURLs(RuntimeAria2, version)
	if len(urls) == 0 {
		return fmt.Errorf("无可用 aria2 下载源（当前平台暂未支持）")
	}
	// 下载包放进数据目录而不是 os.TempDir()：系统盘写满时连 2.4MB 临时文件都建不出来，
	// 安装会毫无提示地失败。baseDir 与解压暂存区同盘，moveDirContents 不需要跨盘搬运。
	if err := os.MkdirAll(a.baseDir, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(a.baseDir, ".aria2-"+version+".zip")
	if cb.OnStage != nil {
		cb.OnStage("download", "正在下载 aria2 "+version+"（约 2.4 MB）…")
	}
	if cb.OnLog != nil {
		cb.OnLog("正在下载 aria2 " + version + "…")
	}
	if err := Download(ctx, zipPath, urls, cb.OnProgress); err != nil {
		return fmt.Errorf("下载 aria2 失败: %w", err)
	}
	defer os.Remove(zipPath)

	staging := filepath.Join(filepath.Dir(a.baseDir), ".aria2-staging")
	_ = os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if cb.OnStage != nil {
		cb.OnStage("extract", "正在解压 aria2…")
	}
	if err := Extract(zipPath, staging); err != nil {
		return fmt.Errorf("解压 aria2 失败: %w", err)
	}
	if _, err := os.Stat(filepath.Join(staging, a.exeName())); err != nil {
		return fmt.Errorf("解压完成但未找到 %s", a.exeName())
	}
	if err := os.MkdirAll(a.baseDir, 0o755); err != nil {
		return err
	}
	if err := moveDirContents(staging, a.baseDir); err != nil {
		return fmt.Errorf("安装 aria2 失败: %w", err)
	}
	_ = os.WriteFile(filepath.Join(a.baseDir, "VERSION"), []byte(version), 0o644)
	if cfg, err := aria2.LoadConfig(); err == nil {
		cfg.Version = version
		_ = aria2.SaveConfig(cfg)
	}
	if cb.OnLog != nil {
		cb.OnLog("aria2 " + version + " 安装完成")
	}
	return nil
}

// ---- ServiceController ----

func (a *Aria2Runtime) DefaultPort() int {
	if c, err := aria2.LoadConfig(); err == nil {
		return c.Port
	}
	return aria2.PortScanStart
}

func (a *Aria2Runtime) Start(ctx context.Context, version string, onLog func(string)) error {
	exe := a.ExeFor("")
	wd := a.baseDir
	if _, err := os.Stat(exe); err != nil {
		// 系统里已装 aria2（brew/apt/winget）时也能直接接管，不强求便携版。
		if p, e := exec.LookPath("aria2c"); e == nil {
			exe, wd = p, filepath.Dir(p)
		} else {
			return fmt.Errorf("请先安装 aria2")
		}
	}
	cfg, err := aria2.LoadConfig()
	if err != nil {
		return err
	}
	if err := aria2.EnsureSessionFile(); err != nil {
		return err
	}
	// 端口上是否已有 daemon、以及它是否真的能用，统一由 probeAria2Daemon 判定。
	// 旧实现只看 TCP 端口 + 一次 GetVersion：卡死但仍 Listen 的实例被当成健康复用，
	// 或反过来因一次偶发超时就把正忙着保存会话的健康 daemon 强杀掉。
	switch probeAria2Daemon(cfg, true) {
	case aria2Healthy:
		logger.I("[env][aria2] 端口 %d 上已有健康 daemon，直接复用", cfg.Port)
		return nil
	case aria2Foreign:
		return fmt.Errorf("端口 %d 上有服务响应，但不是当前配置的 aria2 daemon，已保留不清理", cfg.Port)
	case aria2Stuck:
		logger.W("[env][aria2] 端口 %d 上的 daemon 连续 %d 次 RPC 无响应，判定假活，先清理再重启",
			cfg.Port, aria2ProbeAttempts)
		// 句柄优先 + 端口兜底，两者都做：卡死实例可能不再 Listen（stopByPort 找不到），
		// 而 svcMgr 里那条旧记录不先清掉，下面的 start 会被「服务已在运行」直接拒绝，
		// 于是「清理了但没重启」。
		svcMgr.killTracked(RuntimeAria2)
		svcMgr.forget(RuntimeAria2)
		stopByPort(cfg.Port, "aria2c.exe")
		for i := 0; i < 10 && isPortOpen(cfg.Port); i++ {
			time.Sleep(200 * time.Millisecond)
		}
		if isPortOpen(cfg.Port) {
			return fmt.Errorf("端口 %d 上的无响应 aria2 进程未能清理，请手动结束 aria2c.exe", cfg.Port)
		}
	case aria2Offline:
		// 端口空闲，走正常启动。
	}
	// 只有真的要拉起新进程时才校验保存目录：复用健康 daemon 时别去改用户配置。
	// 移动硬盘拔掉后旧配置里的 E:\Downloads 会让 MkdirAll 直接失败，旧行为是
	// 整个 Start 报错 → 下载页每轮轮询自愈、日志无限刷、引擎永远起不来。
	if err := ensureDownloadDir(cfg); err != nil {
		return err
	}
	args := aria2.DaemonArgs(cfg)
	logger.I("[env][aria2] Start exe=%s port=%d dir=%s", exe, cfg.Port, cfg.DownloadDir)
	if onLog != nil {
		onLog(fmt.Sprintf("启动 aria2 daemon（127.0.0.1:%d）…", cfg.Port))
	}
	err = svcMgr.start(RuntimeAria2, version, exe, wd, args, a.LogPath(version), onLog)
	// 旧的健康结论（尤其是刚清掉的 stuck）对「刚拉起的新实例」已经失效，
	// 不清掉会让 TTL 内的状态面又把新实例判成假活，触发一次多余重启。
	invalidateAria2Health()
	return err
}

// ensureDownloadDir 保证 --dir 指向一个真能写的目录。
// 旧配置里可能留着已拔掉的移动硬盘盘符（E:\Downloads），MkdirAll 会直接报
// "The system cannot find the path specified"，旧行为是让整个 Start 失败，
// 下载页于是每轮轮询自愈、日志无限刷、引擎永远起不来、设置页也跟着被当成异常。
// 这里只在「创建或写入失败」时把这一项切回用户默认下载目录并持久化，
// 端口 / secret / 限速等其余配置一律不动；写盘前会再确认默认目录可用。
func ensureDownloadDir(cfg *aria2.Config) error {
	dir := strings.TrimSpace(cfg.DownloadDir)
	if dir == "" {
		dir = aria2.DefaultDownloadDir()
	}
	firstErr := os.MkdirAll(dir, 0o755)
	if firstErr == nil {
		firstErr = dirWritable(dir) // 目录在但只读，aria2 一样写不进去
	}
	if firstErr == nil {
		cfg.DownloadDir = dir
		return nil
	}

	fallback := aria2.DefaultDownloadDir()
	if strings.EqualFold(filepath.Clean(fallback), filepath.Clean(dir)) {
		return fmt.Errorf("下载目录 %s 不可用: %w", dir, firstErr)
	}
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return fmt.Errorf("下载目录 %s 不可用（%v），默认目录 %s 也无法创建: %w", dir, firstErr, fallback, err)
	}
	if err := dirWritable(fallback); err != nil {
		return fmt.Errorf("下载目录 %s 不可用（%v），默认目录 %s 不可写: %w", dir, firstErr, fallback, err)
	}
	old := dir
	cfg.DownloadDir = fallback
	if err := aria2.SaveConfig(cfg); err != nil {
		return fmt.Errorf("下载目录 %s 不可用，切回 %s 后保存配置失败: %w", old, fallback, err)
	}
	logger.W("[env][aria2] 下载目录 %s 不可用（%v），已自动切回 %s 并保存；可在下载设置里重新指定",
		old, firstErr, fallback)
	return nil
}

// dirWritable 用一个立刻删掉的临时文件确认目录可写。MkdirAll 对「存在但只读」
// 的目录也返回 nil，而 aria2 之后会在写盘时卡死，光看创建结果不够。
func dirWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".quickdock-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// invalidateAria2Health 丢弃健康探测缓存，下一轮查询强制重新探测。
func invalidateAria2Health() {
	aria2HealthMu.Lock()
	aria2HealthCache = aria2HealthSnapshot{}
	aria2HealthMu.Unlock()
}

// ===== aria2 假活（端口在 Listen、RPC 无响应）统一判定 =====
//
// 大文件写盘 / 刷 write cache 阻塞时，aria2 的单线程事件循环会卡在磁盘 I/O：
// 端口仍在 Listen，但所有 RPC 都超时。这种「假活」实例只看 TCP 端口就会被当成
// 运行中，看门狗跳过、Start 又因端口占用被挡回来，用户看到的是下载彻底不动。
//
// 判定必须收敛到一处：Start（能不能复用/要不要清）、Status（看门狗与对账的
// 输入）、ReclaimOrphans（启动期孤儿回收）此前各用各的判据，正是「状态面显示
// 运行中、下载页却一直转圈」的来源。
type aria2DaemonState uint8

const (
	aria2Offline aria2DaemonState = iota // 端口没监听（含进程已退出、尚未启动完成）
	aria2Healthy                         // RPC 往返正常，可直接复用
	aria2Stuck                           // 端口在、监听者确实是 aria2c，但 RPC 连续无响应
	aria2Foreign                         // 端口上有服务应答，但不是配置里的 daemon；绝不能清理
)

const (
	// 连续多次短探测都失败才判卡死：aria2 保存会话 / 合并大分片时会暂缓 RPC，
	// 单次超时就强杀会丢掉正在下载的任务（旧实现正是单次 GetVersion 判 stuck）。
	aria2ProbeAttempts = 3
	aria2ProbeTimeout  = 1200 * time.Millisecond
	aria2ProbeGap      = 200 * time.Millisecond
	// 状态面 3s 一刷、看门狗 15s 一巡；健康结果缓存一小段，避免同一秒内
	// 几个调用点各发一轮 RPC。异常路径上探测本身要花约 4s，缓存同样防重入。
	aria2HealthTTL = 2 * time.Second
)

type aria2HealthSnapshot struct {
	port  int
	at    time.Time
	state aria2DaemonState
}

var (
	aria2HealthMu    sync.Mutex // 保护下面的缓存
	aria2HealthCache aria2HealthSnapshot
	// 同一时刻只允许一次真实探测。状态面走 TryLock（拿不到就用旧结论，不阻塞 UI），
	// Start / 回收走 Lock（必须拿到属于本次决策的新鲜结论）。
	aria2ProbeMu sync.Mutex

	aria2StuckLogAt atomic.Int64
)

// logAria2Stuck 状态面对假活的告警做节流：状态面每 3s 查一次，不节流会刷日志。
func logAria2Stuck(port int) {
	now := time.Now().UnixMilli()
	last := aria2StuckLogAt.Load()
	if last > 0 && now-last < 60_000 {
		return
	}
	if !aria2StuckLogAt.CompareAndSwap(last, now) {
		return
	}
	logger.W("[env][aria2] 端口 %d 上的 daemon 仍在监听但 RPC 连续无响应（假活），状态按不可用处理，等待看门狗清理重启", port)
}

// probeAria2Daemon 返回 aria2 实例的真实可用性。force=true 时强制重新探测，
// 用于「据此决定是否杀进程」的路径；false 允许读短 TTL 缓存（状态面/看门狗）。
func probeAria2Daemon(cfg *aria2.Config, force bool) aria2DaemonState {
	if cfg == nil || cfg.IsRemote() {
		// 远程实例不归本机管，也不该被本机端口上的任何东西影响。
		return aria2Foreign
	}
	aria2HealthMu.Lock()
	cached, hasCache, fresh := aria2HealthCache.state, !aria2HealthCache.at.IsZero() && aria2HealthCache.port == cfg.Port,
		!force && aria2HealthCache.port == cfg.Port && time.Since(aria2HealthCache.at) < aria2HealthTTL
	aria2HealthMu.Unlock()
	if fresh {
		return cached
	}

	if force {
		aria2ProbeMu.Lock()
		defer aria2ProbeMu.Unlock()
	} else if !aria2ProbeMu.TryLock() {
		// 已有探测在跑：别把状态面卡在这里。没有旧结论时保守当健康——
		// 下一轮（3s 后）自然会拿到真实结果，误判一次的代价远小于阻塞。
		if hasCache {
			return cached
		}
		return aria2Healthy
	} else {
		defer aria2ProbeMu.Unlock()
	}

	state := aria2DaemonStateOf(cfg)
	aria2HealthMu.Lock()
	aria2HealthCache = aria2HealthSnapshot{port: cfg.Port, at: time.Now(), state: state}
	aria2HealthMu.Unlock()
	return state
}

// aria2DaemonStateOf 执行一次完整判定。只在 probeAria2Daemon 持有探测锁时调用。
func aria2DaemonStateOf(cfg *aria2.Config) aria2DaemonState {
	if !isPortOpen(cfg.Port) {
		return aria2Offline
	}
	c := aria2.NewClient(cfg)
	for i := 0; i < aria2ProbeAttempts; i++ {
		if i > 0 {
			time.Sleep(aria2ProbeGap)
		}
		ctx, cancel := context.WithTimeout(context.Background(), aria2ProbeTimeout)
		err := c.Ping(ctx)
		cancel()
		if err == nil {
			return aria2Healthy
		}
		if !aria2.IsTransportError(err) {
			// 有应答但不是我们的 daemon（secret 不符 / HTTP 非 200 / 非 JSON-RPC）。
			// 这类进程一律保留，杀掉就是误伤用户自己的服务。
			return aria2Foreign
		}
	}
	// 端口在、连续多次都是网络层失败。判「卡死」之前还要确认监听者确实是 aria2c：
	// 也可能是别的程序占着端口且恰好不响应，误杀代价远高于多等一轮。
	pid := findListenPID(cfg.Port)
	if pid == 0 || !processIsExeAny(pid, "aria2c.exe", "aria2c") {
		logger.W("[env][aria2] 端口 %d 连续无响应，但监听进程 pid=%d 不是 aria2c，按外部服务保留", cfg.Port, pid)
		return aria2Foreign
	}
	return aria2Stuck
}

func (a *Aria2Runtime) Stop(version string) error {
	// 先按句柄精确终止：aria2 卡死时不写日志、也可能已经不再 Listen，
	// 只靠 stopByPort（netstat 反查监听 PID）会找不到目标，进程变孤儿继续占着
	// 会话文件与内存；下一轮 Start 起来后两个实例抢同一个 RPC 端口，后者即以
	// code=1 退出。killTracked 不依赖端口，覆盖这条路径。
	svcMgr.killTracked(RuntimeAria2)
	if cfg, err := aria2.LoadConfig(); err == nil {
		stopByPort(cfg.Port, "aria2c.exe")
	}
	svcMgr.forget(RuntimeAria2)
	return nil
}

func (a *Aria2Runtime) Status(version string) ServiceStatus {
	port := aria2.PortScanStart
	if cfg, err := aria2.LoadConfig(); err == nil {
		port = cfg.Port
	}
	st := ServiceStatus{Running: false, Port: port}
	r := Probe(RuntimeRunningProbe{
		Kind:     RuntimeAria2,
		Port:     port,
		ExeNames: []string{"aria2c.exe", "aria2c"},
		Installs: a.InstalledVersions(),
	})
	if !r.Running {
		return st
	}
	st.Running = true
	st.PID = r.PID
	st.Version = r.Version
	// 端口在 Listen 但 RPC 连续无响应 = 假活。按「未运行」返回，看门狗 / 启动对账 /
	// 「设为常驻」才会走 Start 的清理重启闭环。旧实现只看 TCP 端口，假活实例会被
	// 当成健康永久跳过自愈，只能靠下载页轮询链路兜底，两条链路还会抢着重启。
	// aria2Offline（端口还没 Listen = 刚拉起或刚退出）不介入，避免把启动中的实例
	// 判成掉线、触发重复启动。
	if cfg, e := aria2.LoadConfig(); e == nil && !cfg.IsRemote() &&
		probeAria2Daemon(cfg, false) == aria2Stuck {
		st.Running = false
		logAria2Stuck(cfg.Port)
	}
	return st
}

// 编译期断言：环境管理的服务契约必须完整实现，漏一个方法会在编译期暴露。
var _ RuntimeAdapter = (*Aria2Runtime)(nil)
var _ ServiceController = (*Aria2Runtime)(nil)
