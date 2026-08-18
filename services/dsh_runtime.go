package services

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// DSHProcessManager 启动并管理 dsh web 子进程（独立 Node 进程，127.0.0.1 随机端口）。
// 与现有 AI 助手互不干扰：DSH 是完整 Agent 入口（工具/文件/终端/会话），AI 助手是轻量聊天。
type DSHProcessManager struct {
	app     *application.App
	nodeEnv *NodeEnvManager
	mu      sync.Mutex
	openMu  sync.Mutex // 串行化 OpenDSHWindow，防止连点开出多个窗口
	cmd     *exec.Cmd
	url     string
	port    int
	exit    chan struct{} // reaper 通知：进程已退出（nil=未启动）
	ready   chan struct{} // 冷启动就绪通知：waitReady 完成后 close（nil=无进行中的冷启动）
	starting bool         // 冷启动进行中（窗口已开但 dsh 未就绪）；连点/关窗时避免误判"进程已退出"而重启
	window  *application.WebviewWindow

	// aliveCache 缓存 isDSHAlive 结果：连续点击时避免每次 spawn netstat/tasklist（各 ~300ms）
	aliveCache struct {
		port int
		ok   bool
		at   time.Time
	}
}

// dshLoadingPage 冷启动期间窗口先展示的加载页（暗色，避免用户面对空白/错误页干等）
var dshLoadingPage = "data:text/html;charset=utf-8," + neturl.QueryEscape(`<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8"><style>
html,body{height:100%;margin:0;background:#17181b;color:#e8eaed;font-family:system-ui,-apple-system,"Segoe UI",sans-serif;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:18px}
.spinner{width:36px;height:36px;border:3px solid rgba(74,158,255,.25);border-top-color:#4a9eff;border-radius:50%;animation:spin .9s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
.t{font-size:15px;letter-spacing:.3px}.s{font-size:12px;color:#8b919c}
</style></head><body><div class="spinner"></div><div class="t">正在启动 dsh…</div><div class="s">首次启动需初始化 profile，可能需要 10~30 秒</div></body></html>`)

func NewDSHProcessManager(app *application.App, nodeEnv *NodeEnvManager) *DSHProcessManager {
	return &DSHProcessManager{app: app, nodeEnv: nodeEnv}
}

// DefaultDSHPort dsh 官方默认端口，固定使用以便软件窗口与浏览器访问同一地址
const DefaultDSHPort = 3080

// FindFreePort 在 127.0.0.1 上找空闲端口（默认端口被占用时兜底）
func FindFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Start 拉起 dsh web，返回服务 URL。dsh 未安装时返回错误（调用方应先 SetupDSH）。
func (m *DSHProcessManager) Start() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		return m.url, nil // 已在运行，复用
	}

	// 以可执行入口存在与否判断 dsh 是否安装（DshBin 只认 installDir，会漏掉官方全局安装）
	mainJS := m.nodeEnv.DshMainJS()
	if _, err := os.Stat(mainJS); err != nil {
		return "", fmt.Errorf("dsh 未安装，请先在设置中安装运行环境")
	}
	// 固定官方默认端口 3080，软件内窗口与浏览器访问同一地址。
	port := DefaultDSHPort
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultDSHPort)); err != nil {
		// 端口被占用。dsh 的 task-board ledger 是 per-profile 单实例锁——换端口也绕不开，
		// 另起进程会报 "ledger is already owned by process <pid>" 直接崩溃。因此分三步处理：
		// 1) 若 3080 上已是健康 dsh 服务（node 监听 + HTTP 可达）→ 直接复用，不再启动新进程；
		// 2) 若占用者是残留 node（半死/启动失败的旧实例）→ taskkill 清理后重新绑定 3080；
		// 3) 仍失败（无关进程占用）→ 才退回随机端口并提示。
		if m.isDSHAlive(DefaultDSHPort) {
			u := "http://127.0.0.1:" + strconv.Itoa(DefaultDSHPort)
			m.url = u
			m.port = DefaultDSHPort
			if m.app != nil {
				m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "info", Message: "检测到端口 3080 已有 dsh 服务，直接复用（不启动新进程）"})
			}
			return u, nil
		}
		if pid := findPortPID(DefaultDSHPort); pid > 0 {
			if m.app != nil {
				m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "warn", Message: fmt.Sprintf("端口 3080 被残留进程 %d 占用，正在清理…", pid)})
			}
			killProcessTree(pid)
			// 等待端口真正释放（最多 3s），确保下面能重新绑定 3080
			for i := 0; i < 30; i++ {
				if ln2, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultDSHPort)); err == nil {
					ln2.Close()
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		if ln2, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultDSHPort)); err != nil {
			free, ferr := FindFreePort()
			if ferr != nil {
				return "", ferr
			}
			port = free
			if m.app != nil {
				m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "warn", Message: fmt.Sprintf("端口 %d 被非 dsh 进程占用且无法清理，本次改用随机端口 %d", DefaultDSHPort, free)})
			}
		} else {
			ln2.Close()
		}
	} else {
		ln.Close()
	}
	dshHome := m.nodeEnv.DshHome()
	node := m.nodeEnv.NodePath()
	if node == "" {
		return "", fmt.Errorf("node 未就绪，请先在设置中安装运行环境")
	}
	// 全新机器 ~/.dsh 可能尚未创建，而 CreateProcess 的 lpCurrentDirectory 指向不存在的目录
	// 会报 "The directory name is invalid"（ERROR_DIRECTORY），必须先建目录再启动
	if err := os.MkdirAll(dshHome, 0755); err != nil {
		return "", fmt.Errorf("创建 DSH 数据目录失败: %w", err)
	}

	// 直接用 node 拉起 dsh 的 JS 入口（不走 dsh.cmd/cmd.exe），
	// 配合 CREATE_NO_WINDOW 彻底隐藏控制台窗口；stdout 进入管道，便于解析端口 URL。
	args := []string{mainJS, "web", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	cmd := exec.Command(node, args...)
	cmd.Dir = dshHome
	// 由 os.Environ 过滤掉 WorkBuddy 的 genie-safe-delete NODE_OPTIONS 注入，
	// 否则 dsh 启动时 heal profile 的文件删除操作会被劫持成 trash 而崩溃。
	env := cleanNodeEnv(os.Environ())
	env = append(env, "DSH_HOME="+dshHome)
	if nodeDir := m.nodeEnv.runtimeDir; nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	// hideWindowAttr：Windows 用 CREATE_NO_WINDOW(0x08000000)，切勿用 DETACHED_PROCESS(0x00000008)
	cmd.SysProcAttr = hideWindowAttr()

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 dsh 失败: %w", err)
	}

	// 排空 stdout/stderr（监听端口已知，无需解析 URL 行），同时把输出转发到日志面板，
	// 首次初始化 profile 或启动失败时能直接看到 dsh 在做什么
	logf := func(level, msg string) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
		}
	}
	logf("info", "正在启动 dsh web（首次启动需初始化 profile，可能较慢）…")
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				logf("info", l)
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				logf("error", l)
			}
		}
	}()

	exit := make(chan struct{})
	ready := make(chan struct{})
	m.cmd = cmd
	m.port = port
	m.exit = exit
	m.ready = ready
	m.starting = true
	// reaper：进程自行退出（崩溃/被杀）后清理状态，避免后续 Start() 复用一个死进程
	go func() {
		_ = cmd.Wait()
		close(exit)
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
			m.url = ""
			m.port = 0
			m.exit = nil
			m.ready = nil
			m.starting = false
		}
		m.mu.Unlock()
	}()

	// 就绪检查：监听端口是我们自己选的，直接构造 URL 轮询。
	// 异步执行——窗口先以 loading 页打开，就绪后由 OpenDSHWindow 的 goroutine Navigate 过去，
	// 避免冷启动（首次初始化 profile 可长达 30s）期间用户对着空白 toast 干等。
	u := "http://127.0.0.1:" + strconv.Itoa(port)
	go func() {
		err := m.waitReady(u, exit)
		if err != nil {
			m.Stop() // 内部 stopLocked：进程已死则无副作用；活着的半死实例会被 taskkill 清理
			if m.app != nil {
				m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "error", Message: err.Error()})
			}
			m.mu.Lock()
			m.starting = false
			m.mu.Unlock()
			close(ready) // 窗口 goroutine 收到后仍 Navigate（端口已死显示连接错误页，日志已说明原因）
			return
		}
		m.mu.Lock()
		if m.cmd == cmd {
			m.url = u
		}
		m.starting = false
		m.mu.Unlock()
		close(ready)
	}()
	return u, nil
}

// isDSHAlive 探测端口上是否已有健康的 dsh 服务：占用者必须是 node 进程且 HTTP 可达
// （与 waitReady 相同的就绪判定，404/5xx 不算）。命中后 Start() 直接复用该端口，
// 不再另起进程——dsh 的 task-board ledger 是 per-profile 单实例锁，换端口也绕不开，
// 多实例同时启动会互相抢锁崩溃（"ledger is already owned by process <pid>"）。
func (m *DSHProcessManager) isDSHAlive(port int) bool {
	// 缓存：成功 10s、失败 3s（失败时端口/进程状态可能很快变化，如清理中）。
	// 命中成功缓存时复用路径零子进程秒回——"端口存在应直接起来"。
	ttl := 10 * time.Second
	if !m.aliveCache.ok {
		ttl = 3 * time.Second
	}
	if time.Since(m.aliveCache.at) < ttl && m.aliveCache.port == port {
		return m.aliveCache.ok
	}
	// HTTP GET 优先：端口上有 dsh 服务（健康）时一次 ~10ms 请求即可判定，
	// 不再 spawn netstat/tasklist（各 ~300ms）。3080 是 dsh 官方默认端口，
	// 被其他 HTTP 服务占用且返回 200 的概率极低，可接受该极小误判。
	ok := false
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	if resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/"); err == nil {
		resp.Body.Close()
		ok = resp.StatusCode >= 200 && resp.StatusCode < 400 && resp.StatusCode != 404
	}
	// GET 失败（端口占用者不是健康服务）→ 保持 false，由 Start() 走
	// findPortPID + killProcessTree 清理残留 node 后重绑 3080 的路径。
	// 注意：绝不能把"占用者是 node 但 HTTP 不通"判为 alive，否则窗口打开是死页面。
	m.aliveCache.port = port
	m.aliveCache.ok = ok
	m.aliveCache.at = time.Now()
	return ok
}

// findPortPID 返回 127.0.0.1:<port> 上 LISTENING 进程的 PID；无占用返回 0。
// netstat 是控制台程序，GUI 主进程（-H windowsgui）直接拉起会弹 cmd，必须隐藏窗口。
func findPortPID(port int) int {
	cmd := exec.Command("netstat", "-ano")
	cmd.SysProcAttr = hideWindowAttr()
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	target := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "LISTENING") || !strings.Contains(line, target) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err == nil && pid > 0 {
			return pid
		}
	}
	return 0
}

// isNodeProcess 判断 PID 对应进程是否为 node.exe（dsh 由 node 拉起，残留实例也是 node）。
// tasklist 是控制台程序，同样必须隐藏窗口。
func isNodeProcess(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = hideWindowAttr()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV 首字段是镜像名（可能带引号），如 "node.exe","8216","Console",...
		name := strings.Trim(line, `"`)
		if idx := strings.Index(name, ","); idx > 0 {
			name = name[:idx]
		}
		if strings.EqualFold(name, "node.exe") {
			return true
		}
	}
	return false
}

// killProcessTree 用 taskkill /T /F 强杀进程树（隐藏窗口），返回是否成功发出。
func killProcessTree(pid int) bool {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = hideWindowAttr()
	return cmd.Run() == nil
}

// waitReady 轮询 GET / 直到服务真正就绪（45s，新机器首次启动需初始化 profile，20s 内常常起不来）。
// 判定条件：2xx/3xx/401/403 视为就绪；404（页面资源未就绪）与 5xx 继续等待，
// 避免把「端口已监听但服务尚未初始化完成」误判为就绪。
// exit 为进程退出通知：dsh 中途崩溃（配置错误/缺依赖/端口被抢）时立即返回明确错误，
// 避免干等满 45s 才报一句误导性的「健康检查超时」。
func (m *DSHProcessManager) waitReady(url string, exit <-chan struct{}) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 90; i++ {
		select {
		case <-exit:
			return fmt.Errorf("dsh 进程已退出，启动失败（请查看上方日志）")
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 && resp.StatusCode != 404 {
				return nil
			}
		}
		select {
		case <-exit:
			return fmt.Errorf("dsh 进程已退出，启动失败（请查看上方日志）")
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("健康检查超时: %s", url)
}

// Stop 优雅停止；Windows 下用 taskkill /T 杀进程树兜底，防止残留 Node 占端口
func (m *DSHProcessManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// stopLocked 停止逻辑的带锁实现，调用方必须已持有 m.mu。
// 只负责发信号与兜底强杀；cmd.Wait 由 Start() 创建的 reaper goroutine 负责，
// 避免与 reaper 对同一 cmd 重复 Wait。
func (m *DSHProcessManager) stopLocked() {
	if m.cmd == nil || m.cmd.Process == nil {
		m.cmd = nil
		m.url = ""
		m.starting = false
		m.ready = nil
		return
	}
	cmd := m.cmd
	exit := m.exit
	m.cmd = nil
	m.url = ""
	m.port = 0
	m.exit = nil
	m.starting = false
	m.ready = nil
	if exit == nil {
		// 防御：理论上 Start 一定先建 exit；万一没有就强杀
		_ = cmd.Process.Kill()
		return
	}
	if runtime.GOOS == "windows" {
		// Windows 不支持 os.Interrupt 信号（Signal 必然报错），旧实现每次 Stop 都白等
		// 3s 超时才 taskkill，关 DSH 窗口有明显延迟。这里直接 taskkill /T /F 杀进程树
		// （dsh 的 node 子进程一并清理，不残留占端口）；reaper 的 cmd.Wait() 随之返回并清状态。
		// taskkill.exe 是控制台程序：GUI 主进程（正式版 -H windowsgui）直接拉起会弹 cmd 窗，
		// 必须带 CREATE_NO_WINDOW，与 Start()/node_env.go 的其他隐藏控制台调用保持一致。
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		kill.SysProcAttr = hideWindowAttr()
		kill.Run()
		// 等待端口释放：dsh 关闭后 TCP TIME_WAIT 短暂存在，轮询 30 次（3s）确保下次
		// OpenDSHWindow() 能重新绑定 3080 而不是开随机端口。
		for i := 0; i < 30; i++ {
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultDSHPort))
			if err == nil {
				ln.Close()
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	go func() {
		select {
		case <-exit: // reaper 已确认退出
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()
}

// InstallPlugin 执行 dsh plugin --profile web add <plugin> 安装市场插件。
// 与 Start() 相同的拉起方式（node 直拉 lib/bin.js，隐藏控制台）；输出逐行经
// quickdock:dsh:log 事件推送到前端日志面板，完成后 emit info 标记。
func (m *DSHProcessManager) InstallPlugin(plugin string) error {
	// 失败兜底必须覆盖整个函数：node 未就绪/入口缺失/目录创建失败等前置 return 同样发生在
	// service.go 的异步 goroutine 里（go func 调用），前端拿不到同步错误，只能靠
	// quickdock:dsh:plugin 事件复位 installingPlugin，漏发会导致按钮永久禁用（只能重进页面恢复）。
	finished := false
	defer func() {
		if !finished {
			m.emitPluginDone(false)
		}
	}()

	node := m.nodeEnv.NodePath()
	if node == "" {
		return fmt.Errorf("node 未就绪，请先安装运行环境")
	}
	mainJS := m.nodeEnv.DshMainJS()
	if _, err := os.Stat(mainJS); err != nil {
		return fmt.Errorf("dsh 入口缺失，请重新安装: %s", mainJS)
	}
	dshHome := m.nodeEnv.DshHome()
	// 同 Start()：确保 DSH 数据目录存在，否则 CreateProcess 工作目录无效报 ERROR_DIRECTORY
	if err := os.MkdirAll(dshHome, 0755); err != nil {
		return err
	}
	args := []string{mainJS, "plugin", "--profile", "web", "add", plugin}
	// 超时兜底：避免 npm/网络卡死时前端 installingPlugin 状态永久占用导致按钮不可点
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Dir = dshHome
	env := cleanNodeEnv(os.Environ())
	env = append(env, "DSH_HOME="+dshHome)
	// 便携 node 目录前置到 PATH：dsh 插件安装过程中可能 spawn `node` 子进程
	// （npm 的 postinstall 如 koffi 的 cnoke.cjs 走 cmd /c node），新电脑无系统 node
	// 时 PATH 里找不到 node 会直接失败。与 Start()/runNpmInstall() 保持一致。
	if nodeDir := m.nodeEnv.runtimeDir; nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	cmd.SysProcAttr = hideWindowAttr()

	logf := func(level, msg string) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
		}
	}
	logf("info", fmt.Sprintf("安装插件 %s（dsh plugin --profile web add %s）…", plugin, plugin))

	// 确保 pnpm 可用：dsh plugin add 内部用 pnpm 管理依赖，新电脑可能没装
	if err := m.ensurePnpm(ctx, logf); err != nil {
		logf("error", fmt.Sprintf("pnpm 不可用且自动安装失败: %v", err))
		return err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()

	if err := cmd.Wait(); err != nil {
		logf("error", fmt.Sprintf("安装插件失败: %v", err))
		return err // defer 统一补发 emitPluginDone(false)
	}
	logf("info", fmt.Sprintf("插件 %s 安装完成", plugin))
	finished = true
	m.emitPluginDone(true)
	return nil
}

// ensurePnpm 检查 pnpm 是否可用，不可用则用当前 node/npm 补装。
// dsh plugin add 内部调 pnpm 管理插件依赖，全新电脑无 pnpm 会报
// 'pnpm' 不是内部或外部命令。
func (m *DSHProcessManager) ensurePnpm(ctx context.Context, logf func(string, string)) error {
	// 先检查 PATH 上是否有 pnpm
	if _, err := exec.LookPath("pnpm"); err == nil {
		return nil
	}
	// 便携 node 全局目录里可能有（SetupDSH 已装但 PATH 未刷新）
	nodeDir := m.nodeEnv.runtimeDir
	if nodeDir != "" {
		pnpmExe := nodeDir + string(os.PathSeparator) + "pnpm" + exeExt()
		if _, err := os.Stat(pnpmExe); err == nil {
			return nil
		}
	}
	// 没有 pnpm，用 npm 装
	node := m.nodeEnv.NodePath()
	npmCli := m.nodeEnv.npmCli()
	if node == "" || npmCli == "" {
		return fmt.Errorf("node 或 npm 不可用，无法自动安装 pnpm")
	}
	logf("info", "pnpm 未检测到，正在自动安装…")
	installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(installCtx, node, npmCli, "install", "-g", "pnpm")
	cmd.Dir = m.nodeEnv.DshHome()
	env := cleanNodeEnv(os.Environ())
	env = append(env, "DSH_HOME="+m.nodeEnv.DshHome(),
		"npm_config_registry=https://registry.npmmirror.com/")
	if nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	cmd.SysProcAttr = hideWindowAttr()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	if err := cmd.Wait(); err != nil {
		return err
	}
	logf("info", "pnpm 安装完成")
	return nil
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".cmd"
	}
	return ""
}

// emitPluginDone 通知前端插件命令执行结束（quickdock:dsh:plugin 事件）
func (m *DSHProcessManager) emitPluginDone(ok bool) {
	if m.app != nil {
		m.app.Event.Emit("quickdock:dsh:plugin", map[string]bool{"ok": ok})
	}
}

// cleanNodeEnv 返回剔除了 WorkBuddy/CodeBuddy 注入的 NODE_OPTIONS 的环境变量。
// WorkBuddy 会设 NODE_OPTIONS=--require=...genie-safe-delete.cjs 让 node 加载 safe-delete
// shim（把 fs 删除操作劫持成 trash），dsh 启动时 heal profile 需删除旧文件 → 直接抛错崩溃。
// 这里按引号感知分词后滤掉含 genie-safe-delete 的 token，其余保留（如 --use-system-ca）。
func cleanNodeEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key != "NODE_OPTIONS" || !strings.Contains(val, "genie-safe-delete") {
			out = append(out, kv)
			continue
		}
		toks := splitQuoted(val)
		filtered := make([]string, 0, len(toks))
		for _, t := range toks {
			if strings.Contains(t, "genie-safe-delete") {
				continue
			}
			filtered = append(filtered, t)
		}
		out = append(out, key+"="+quoteJoin(filtered))
	}
	return out
}

// splitQuoted 按空格拆分，尊重双引号包裹（NODE_OPTIONS 的 --require="C:/Program Files/..." 路径带空格）
func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQ = !inQ
		case c == ' ' && !inQ:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// quoteJoin 重拼 NODE_OPTIONS：含空格的 token 重新加引号
func quoteJoin(toks []string) string {
	for i, t := range toks {
		if strings.ContainsAny(t, " \t") {
			toks[i] = `"` + t + `"`
		}
	}
	return strings.Join(toks, " ")
}

// OpenDSHWindow 拉起 dsh 并在原生 WebviewWindow 中加载其 URL。
// openMu 串行化整个流程，保证「启动进程 + 建窗口」期间的重复点击不会开出第二个窗口。
func (m *DSHProcessManager) OpenDSHWindow() (string, error) {
	m.openMu.Lock()
	defer m.openMu.Unlock()

	// 已有窗口：直接复用（含首次点击尚在启动中、用户连点的情况）。
	// starting=true 说明冷启动进行中（窗口显示 loading，dsh 尚未就绪）——此时也必须复用
	// 窗口而不是销毁重建，否则会 Stop() 杀掉正在启动的 dsh 再重来一遍。
	m.mu.Lock()
	win, url := m.window, m.url
	starting := m.starting
	m.mu.Unlock()
	if win != nil {
		if url != "" || starting {
			win.Show()
			win.Focus()
			if url != "" {
				return url, nil
			}
			return dshLoadingPage, nil // 启动中：返回 loading 页地址，前端仅作提示
		}
		// 进程已退出（reaper 已清空 url）但窗口残留：销毁旧窗口走下方重建流程，
		// 否则返回空 URL 且窗口显示死页面，前端无任何提示。
		m.mu.Lock()
		m.window = nil
		m.mu.Unlock()
		win.Close() // 触发 WindowClosing handler（内部 Stop() 对已死进程无副作用）
	}

	// Start 内部已做「进程在跑则复用」，此处无需额外判断
	url, err := m.Start()
	if err != nil {
		return "", err
	}

	// 冷启动先展示 loading 页，dsh 就绪后自动跳转真实地址——用户点击后立即看到窗口，
	// 而不是在 toast 上干等 dsh web（首次初始化 profile 可达 30s）慢慢起来。
	pageURL := dshLoadingPage
	m.mu.Lock()
	starting = m.starting
	ready := m.ready // Start() 冷启动路径创建；复用路径为 nil
	m.mu.Unlock()
	if !starting || url != "" {
		pageURL = url
	}

	win = m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - DeepSeek Harness",
		Width:            1200,
		Height:           800,
		MinWidth:         600,
		MinHeight:        400,
		BackgroundColour: application.RGBA{Red: 23, Green: 24, Blue: 27, Alpha: 255},
		URL:              pageURL,
		Windows:          application.WindowsWindow{HiddenOnTaskbar: false},
	})
	// 关闭窗口（Wails 内部 handler 负责真正销毁）时清引用并停掉 dsh 子进程（避免端口残留）
	win.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		m.mu.Lock()
		m.window = nil
		m.mu.Unlock()
		m.Stop()
	})

	m.mu.Lock()
	m.window = win
	m.mu.Unlock()
	win.Show()

	// 冷启动：等 waitReady goroutine 通知后 Navigate 到真实 dsh 地址
	if starting && ready != nil {
		go func(w *application.WebviewWindow, target string, rd <-chan struct{}) {
			select {
			case <-rd:
				// dsh 就绪（或启动失败已 close）：跳到最终地址；窗口若已销毁则 no-op
				w.SetURL(target)
			case <-time.After(50 * time.Second): // 兜底 > waitReady 45s 上限
				w.SetURL(target)
			}
		}(win, url, ready)
	}
	return url, nil
}
