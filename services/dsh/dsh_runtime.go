package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"quickdock/internal/sysutil"
)

// DSHProcessManager 启动并管理 dsh web 子进程（独立 Node 进程，127.0.0.1 随机端口）。
// 与现有 AI 助手互不干扰：DSH 是完整 Agent 入口（工具/文件/终端/会话），AI 助手是轻量聊天。
type DSHProcessManager struct {
	app      *application.App
	nodeEnv  *NodeEnvManager
	mu       sync.Mutex
	openMu   sync.Mutex // 串行化 OpenDSHWindow，防止连点开出多个窗口
	cmd      *exec.Cmd
	url      string
	port     int
	exit     chan struct{} // reaper 通知：进程已退出（nil=未启动）
	ready    chan struct{} // 冷启动就绪通知：waitReady 完成后 close（nil=无进行中的冷启动）
	readyErr error         // 配合 ready：就绪结果；nil=成功，非 nil=失败原因（窗口 goroutine 据此显示友好错误页而非死地址）
	starting bool          // 冷启动进行中（窗口已开但 dsh 未就绪）；连点/关窗时避免误判"进程已退出"而重启
	window   *application.WebviewWindow

	// lastPluginBackup 最近一次「更新全部插件」前的备份目录（DSH_HOME/backups/web-<ts>）。
	// 用于一键回滚；内存指针为空时回退到 backups/ 下最新 web-* 目录（跨重启仍可用）。
	lastPluginBackup string

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
</style></head><body><div class="spinner"></div><div class="t">正在启动 dsh…</div><div class="s">首次启动需初始化 profile，可能需要 10~30 秒（已等待 <span id="w">0</span> 秒）</div><script>var s=0;setInterval(function(){s++;document.getElementById("w").textContent=s;},1000)</script></body></html>`)

// dshErrorPage 冷启动失败时的内置错误页（暗色，与 loading 页同风格）。
// 避免把窗口 SetURL 到一个没在监听的死端口，让用户直面浏览器"无法访问此网站"。
// 完整日志在主界面「环境管理 → DeepSeek Harness」面板，关闭窗口后重新点击导航可重试。

func NewDSHProcessManager(app *application.App, nodeEnv *NodeEnvManager) *DSHProcessManager {
	return &DSHProcessManager{app: app, nodeEnv: nodeEnv}
}

// SetApp 延迟注入应用实例：AppService 构造早于 application.New，
// 拿到 app 后由 service.go 补挂（与旧同包直赋字段等价）。
func (m *DSHProcessManager) SetApp(app *application.App) { m.app = app }

// DefaultDSHPort dsh 官方默认端口，固定使用以便软件窗口与浏览器访问同一地址
const DefaultDSHPort = 3080

// FindFreePort 在 127.0.0.1 上找空闲端口（默认端口被占用时兜底）

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
	// 必须带 --no-open：dsh web 默认会在启动后自行打开系统默认浏览器访问 127.0.0.1:3080
	// （"opening the default browser"），而 QuickDock 已有原生 WebviewWindow 承载 dsh 界面，
	// 不让它额外弹浏览器。
	args := []string{mainJS, "web", "--no-open", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	cmd := exec.Command(node, args...)
	cmd.Dir = dshHome
	// 由 os.Environ 过滤掉 WorkBuddy 的 genie-safe-delete NODE_OPTIONS 注入，
	// 否则 dsh 启动时 heal profile 的文件删除操作会被劫持成 trash 而崩溃。
	env := cleanNodeEnv(os.Environ())
	env = append(env, "DSH_HOME="+dshHome)
	if nodeDir := m.nodeEnv.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	// sysutil.Hide：Windows 用 CREATE_NO_WINDOW(0x08000000)，切勿用 DETACHED_PROCESS(0x00000008)
	sysutil.Hide(cmd)

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
	// 就绪判定用 HTTP 壳 200 即可（简单可靠）；dsh 官方前端未就绪时自带「定时常量刷新」，
	// 不会出现「无法打开」。这里只负责把 stdout/stderr 转发到日志面板。
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stdout log goroutine panic: %v\n", r)
			}
		}()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if l := sc.Text(); l != "" {
				logf("info", l)
			}
		}
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stderr log goroutine panic: %v\n", r)
			}
		}()
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
	m.readyErr = nil
	m.starting = true
	// reaper：进程自行退出（崩溃/被杀）后清理状态，避免后续 Start() 复用一个死进程
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH reaper goroutine panic: %v\n", r)
			}
		}()
		_ = cmd.Wait()
		close(exit)
		m.mu.Lock()
		if m.cmd == cmd {
			m.cmd = nil
			m.url = ""
			m.port = 0
			m.exit = nil
			m.ready = nil
			m.readyErr = nil
			m.starting = false
		}
		m.mu.Unlock()
	}()

	// 就绪检查：监听端口是我们自己选的，直接构造 URL 轮询。
	// 异步执行——窗口先以 loading 页打开，就绪后由 OpenDSHWindow 的 goroutine Navigate 过去，
	// 避免冷启动（首次初始化 profile 可长达 30s）期间用户对着空白 toast 干等。
	u := "http://127.0.0.1:" + strconv.Itoa(port)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH waitReady goroutine panic: %v\n", r)
			}
		}()
		err := m.waitReady(u, exit)
		if err != nil {
			m.Stop() // 内部 stopLocked：进程已死则无副作用；活着的半死实例会被 taskkill 清理
			if m.app != nil {
				m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "error", Message: err.Error()})
			}
		}
		m.mu.Lock()
		m.starting = false
		m.readyErr = err // nil=就绪；非 nil=失败原因（窗口 goroutine 据此显示友好错误页，而非死地址）
		if err == nil && m.cmd == cmd {
			m.url = u
		}
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

// Running 返回 dsh web 服务当前是否在运行。优先看本管理器拉起的进程；进程不在时
// 探测端口上是否有健康 dsh 服务（覆盖"复用外部已启动的 dsh"场景，Start() 复用路径
// m.cmd 为 nil 但服务实际在跑）。供设置页「dsh web 运行状态」与自动启动判断使用。
func (m *DSHProcessManager) Running() bool {
	m.mu.Lock()
	if m.cmd != nil && m.cmd.Process != nil {
		m.mu.Unlock()
		return true
	}
	port := m.port
	m.mu.Unlock()
	if port == 0 {
		port = DefaultDSHPort
	}
	return m.isDSHAlive(port)
}

// Port 返回当前实际绑定的端口（带锁读取；未启动或默认端口场景返回 3080）。
func (m *DSHProcessManager) Port() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port != 0 {
		return m.port
	}
	return DefaultDSHPort
}

// NotifyStopped 服务被手动停止时调用：若 dsh 窗口还开着，把窗口导航到"服务已停止"
// 提示页，避免用户面对浏览器"无法访问此网站"式的死地址。窗口引用保留，下次点击侧边栏
// dsh 时会因探测到服务不可达而自动销毁重建。
func (m *DSHProcessManager) NotifyStopped() {
	m.mu.Lock()
	w := m.window
	m.mu.Unlock()
	if w == nil {
		return
	}
	w.SetURL(dshErrorPage("dsh web 服务已停止。可在「环境管理 → DeepSeek Harness」重新启动，或直接点击侧边栏 dsh 自动拉起。"))
}

// dshReachable 实时探测 dsh 是否真正可达（与 waitReady 相同的就绪语义：2xx/3xx/401/403，
// 404 与 5xx 视为未就绪）。与 isDSHAlive 不同，这里不依赖 aliveCache，用于两次判断之间的
// 竞态窗口（就绪判定后、WebView 实际导航前 dsh 可能抖动/崩溃），避免把窗口导航到死地址
// 而让用户直面浏览器"无法访问此网站"。探测很快（~ms 级），命中即返回。
func (m *DSHProcessManager) dshReachable(url string) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400 && resp.StatusCode != 404
}

// waitReachableThenNavigate 在给定时限内轮询 dsh 可达性，一达成就把 WebView 导航到 target。
// 若始终不可达返回 false（调用方据此显示友好错误页而不是把窗口甩到死地址）。
// 为冷启动"就绪→导航"的竞态提供兜底：waitReady 判定就绪后到 SetURL 之间 dsh 若抖动/崩溃，
// 这里会重试而不是立刻触发浏览器的"无法访问此网站"页。
func (m *DSHProcessManager) waitReachableThenNavigate(w *application.WebviewWindow, target string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if m.dshReachable(target) {
			w.SetURL(target)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// findPortPID 返回 127.0.0.1:<port> 上 LISTENING 进程的 PID；无占用返回 0。
// netstat 是控制台程序，GUI 主进程（-H windowsgui）直接拉起会弹 cmd，必须隐藏窗口。

// isNodeProcess 判断 PID 对应进程是否为 node.exe（dsh 由 node 拉起，残留实例也是 node）。
// tasklist 是控制台程序，同样必须隐藏窗口。

// killProcessTree 用 taskkill /T /F 强杀进程树（隐藏窗口），返回是否成功发出。

// ensurePortReleased 轮询直到端口可重新绑定（最多 3s）；dsh 关闭后 TCP TIME_WAIT
// 短暂存在，Start()/stopLocked() 都靠它确保下次能重新绑定默认端口 3080。
func (m *DSHProcessManager) ensurePortReleased(port int) {
	for i := 0; i < 30; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			ln.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// Stop 优雅停止；Windows 下用 taskkill /T 杀进程树兜底，防止残留 Node 占端口。
// stopLocked 之外统一失效 aliveCache：刚停止时绝不能再报告"正在运行"
// （旧成功缓存会让 Stop 后 10s 内 Running()/状态页误判，看起来像"没停掉/又启动"）。
func (m *DSHProcessManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	m.aliveCache.port = 0
	m.aliveCache.ok = false
	m.aliveCache.at = time.Time{}
}

// logf 转发诊断日志到「环境管理 → DeepSeek Harness」日志面板（quickdock:dsh:log 事件）
func (m *DSHProcessManager) logf(level, msg string) {
	if m.app != nil {
		m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
	}
}

// stopLocked 停止逻辑的带锁实现，调用方必须已持有 m.mu。
// 只负责发信号与兜底强杀；cmd.Wait 由 Start() 创建的 reaper goroutine 负责，
// 避免与 reaper 对同一 cmd 重复 Wait。
func (m *DSHProcessManager) stopLocked() {
	if m.cmd == nil || m.cmd.Process == nil {
		// 无进程句柄 ≠ 服务不在跑：Start() 的"复用路径"（检测到 3080 已有健康 dsh
		// 直接复用，m.cmd 保持 nil）下服务由外部/残留 node 提供。此时必须按端口兜底
		// 清理，否则设置里点"停止"什么都不做，服务继续跑、状态过一会又显示运行。
		if runtime.GOOS == "windows" {
			m.logf("info", "DSHStop: 无托管进程句柄（复用/外部服务），按端口兜底清理 127.0.0.1:3080")
			if pid := findPortPID(DefaultDSHPort); pid > 0 {
				if isNodeProcess(pid) {
					m.logf("info", fmt.Sprintf("DSHStop: 端口 3080 由 node PID %d 监听，taskkill /T /F 清理", pid))
				} else {
					// 复用路径的成立前提（Start 时 isDSHAlive 判定"3080 是健康 dsh 服务"）
					// 已把该进程当作 dsh——用户点"停止"意图就是停掉它，非 node 也强制清理，
					// 否则停止永远失效（曾遇 3080 由非 node 名进程监听的场景）。记录进程名供追溯。
					m.logf("warn", fmt.Sprintf("DSHStop: 端口 3080 由非 node 进程 PID %d 监听，按停止意图强制清理", pid))
				}
				killProcessTree(pid)
				m.ensurePortReleased(DefaultDSHPort)
			} else {
				m.logf("info", "DSHStop: 端口 3080 无监听，无需清理")
			}
		}
		m.cmd = nil
		m.url = ""
		m.starting = false
		m.ready = nil
		m.readyErr = nil
		return
	}
	cmd := m.cmd
	exit := m.exit
	m.cmd = nil
	m.url = ""
	// 保留 m.port：Running() 停止后按"最后实际使用的端口"探测。若清零回退回默认端口 3080，
	// 随机端口场景（3080 被无关进程占用时 Start 才退避）会把 3080 上的无关服务误判为 dsh。
	m.exit = nil
	m.starting = false
	m.ready = nil
	m.readyErr = nil
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
		sysutil.Hide(kill)
		if err := kill.Run(); err != nil {
			m.logf("warn", fmt.Sprintf("DSHStop: taskkill 进程树 PID %d 返回错误: %v（继续按端口验证）", cmd.Process.Pid, err))
		}
		// 验证端口是否真正释放。仍被占用（taskkill 未生效 / 锁定的 cmd 进程已退、
		// 3080 实际由树外残留进程提供）→ 按端口兜底再清一次，确保"停止"一定生效。
		if pid := findPortPID(DefaultDSHPort); pid > 0 {
			if isNodeProcess(pid) {
				m.logf("warn", fmt.Sprintf("DSHStop: taskkill 后 3080 仍由 node PID %d 监听，再次清理", pid))
			} else {
				m.logf("warn", fmt.Sprintf("DSHStop: taskkill 后 3080 仍由非 node 进程 PID %d 监听，按停止意图强制清理", pid))
			}
			killProcessTree(pid)
		}
		// 等待端口释放：dsh 关闭后 TCP TIME_WAIT 短暂存在，轮询 30 次（3s）确保下次
		// OpenDSHWindow() 能重新绑定 3080 而不是开随机端口。
		m.ensurePortReleased(DefaultDSHPort)
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH signal-fallback goroutine panic: %v\n", r)
			}
		}()
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
	if nodeDir := m.nodeEnv.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	sysutil.Hide(cmd)

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
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stdout log goroutine panic: %v\n", r)
			}
		}()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stderr log goroutine panic: %v\n", r)
			}
		}()
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

// PluginUpdateInfo 单个插件的「有可用更新」信息（供 CheckPluginUpdates 预检返回）。
type PluginUpdateInfo struct {
	Name    string `json:"name"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
}

// UpdateAllPlugins 执行 dsh plugin --profile web update，将 profile 内所有插件升级到各自
// semver 范围内的最新版（git 依赖拉默认分支最新提交；精确固定版本如 0.3.6 不动）。
// 升级前自动备份 package.json + pnpm-lock.yaml 到 DSH_HOME/backups/web-<ts>/，便于失败时回滚。
// 日志复用 quickdock:dsh:log；完成时 emit quickdock:dsh:plugin{ok,backup,kind:"update"}。
func (m *DSHProcessManager) UpdateAllPlugins() error {
	finished := false
	defer func() {
		if !finished {
			m.emitPluginUpdateDone(false, "")
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
	profileDir := filepath.Join(dshHome, "profiles", "web")
	if err := os.MkdirAll(dshHome, 0755); err != nil {
		return err
	}
	logf := func(level, msg string) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
		}
	}

	// 1) 升级前备份（固定版/range 版都会改 package.json，备份保证可回滚）
	backupDir, err := m.backupProfileDeps(profileDir, logf)
	if err != nil {
		logf("error", fmt.Sprintf("备份失败，已取消更新: %v", err))
		return err
	}
	m.lastPluginBackup = backupDir

	// 2) 确保 pnpm 可用（复用 InstallPlugin 的兜底逻辑）
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := m.ensurePnpm(ctx, logf); err != nil {
		logf("error", fmt.Sprintf("pnpm 不可用且自动安装失败: %v", err))
		return err
	}

	// 3) dsh plugin 子命令会把剩余参数转发给 profile 目录里的 pnpm；无包名即更新全部
	args := []string{mainJS, "plugin", "--profile", "web", "update"}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Dir = dshHome
	env := cleanNodeEnv(os.Environ())
	env = append(env, "DSH_HOME="+dshHome)
	if nodeDir := m.nodeEnv.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	sysutil.Hide(cmd)

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
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH update stdout log goroutine panic: %v\n", r)
			}
		}()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH update stderr log goroutine panic: %v\n", r)
			}
		}()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()

	if err := cmd.Wait(); err != nil {
		logf("error", fmt.Sprintf("更新插件失败: %v（可点回滚恢复备份）", err))
		return err // defer 统一补发 emitPluginUpdateDone(false)
	}
	logf("info", fmt.Sprintf("全部插件更新完成（旧配置已备份到 %s，异常可回滚）", backupDir))
	finished = true
	m.emitPluginUpdateDone(true, backupDir)
	return nil
}

// RollbackPlugins 将 profile 插件回滚到最近一次「更新全部插件」之前的备份状态：
// 删当前 node_modules 与 package.json/lock，从备份恢复 package.json+lock 后 pnpm install 重建。
func (m *DSHProcessManager) RollbackPlugins() (err error) {
	backupDir := m.latestBackupDir()
	if backupDir == "" {
		return fmt.Errorf("没有可用的插件备份")
	}
	profileDir := filepath.Join(m.nodeEnv.DshHome(), "profiles", "web")
	logf := func(level, msg string) {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: level, Message: msg})
		}
	}
	// 统一在返回前 emit 完成事件（service.go 在另一包，无法直接调 unexported emit），
	// 成功 err==nil → ok:true，任意失败路径 err!=nil → ok:false，前端据此复位回滚按钮。
	defer func() {
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:plugin-rollback", map[string]bool{"ok": err == nil})
		}
	}()
	logf("info", fmt.Sprintf("开始回滚插件（备份: %s）…", backupDir))

	// 重建前清场：删 node_modules（大目录）+ package.json/lock，避免旧依赖残留
	if err := os.RemoveAll(filepath.Join(profileDir, "node_modules")); err != nil {
		logf("error", fmt.Sprintf("清理 node_modules 失败: %v", err))
	}
	_ = os.Remove(filepath.Join(profileDir, "package.json"))
	_ = os.Remove(filepath.Join(profileDir, "pnpm-lock.yaml"))
	if err := copyFile(filepath.Join(backupDir, "package.json"), filepath.Join(profileDir, "package.json")); err != nil {
		logf("error", fmt.Sprintf("恢复 package.json 失败: %v", err))
		return err
	}
	if _, err := os.Stat(filepath.Join(backupDir, "pnpm-lock.yaml")); err == nil {
		_ = copyFile(filepath.Join(backupDir, "pnpm-lock.yaml"), filepath.Join(profileDir, "pnpm-lock.yaml"))
	}

	node := m.nodeEnv.NodePath()
	if node == "" {
		return fmt.Errorf("node 未就绪，请先安装运行环境")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	pnpm, err := m.pnpmBin()
	if err != nil {
		if err2 := m.ensurePnpm(ctx, logf); err2 != nil {
			logf("error", fmt.Sprintf("pnpm 不可用且自动安装失败: %v", err2))
			return err2
		}
		if pnpm, err = m.pnpmBin(); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, pnpm, "install")
	cmd.Dir = profileDir
	env := cleanNodeEnv(os.Environ())
	if nodeDir := m.nodeEnv.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	sysutil.Hide(cmd)
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
		logf("error", fmt.Sprintf("回滚安装依赖失败: %v", err))
		return err
	}
	m.lastPluginBackup = ""
	logf("info", "插件已回滚到更新前状态")
	return nil
}

// CheckPluginUpdates 预检 profile 内 registry 插件（git 依赖不纳入）是否有可用更新，
// 返回 {name,current,latest} 列表。联网查 npm registry，可能耗时数秒。
func (m *DSHProcessManager) CheckPluginUpdates() ([]PluginUpdateInfo, error) {
	profileDir := filepath.Join(m.nodeEnv.DshHome(), "profiles", "web")
	if _, err := os.Stat(filepath.Join(profileDir, "package.json")); err != nil {
		return nil, fmt.Errorf("profile 未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pnpm, err := m.pnpmBin()
	if err != nil {
		if err2 := m.ensurePnpm(ctx, func(string, string) {}); err2 != nil {
			return nil, err2
		}
		if pnpm, err = m.pnpmBin(); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, pnpm, "outdated", "--json")
	cmd.Dir = profileDir
	env := cleanNodeEnv(os.Environ())
	if nodeDir := m.nodeEnv.runtimeDir(); nodeDir != "" {
		if _, err := os.Stat(nodeDir); err == nil {
			env = append(env, "PATH="+nodeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}
	cmd.Env = env
	sysutil.Hide(cmd)
	// pnpm outdated 有更新时 exit code=1（非错误），故用 Output 捕获 stdout 后尽力解析
	out, _ := cmd.Output()
	var raw map[string]map[string]string
	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return []PluginUpdateInfo{}, nil
	}
	res := make([]PluginUpdateInfo, 0, len(raw))
	for name, info := range raw {
		res = append(res, PluginUpdateInfo{Name: name, Current: info["current"], Latest: info["latest"]})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

// emitPluginUpdateDone 更新全部插件完成事件（带 backup 路径供前端启用回滚）。
func (m *DSHProcessManager) emitPluginUpdateDone(ok bool, backup string) {
	if m.app != nil {
		m.app.Event.Emit("quickdock:dsh:plugin", map[string]any{"ok": ok, "backup": backup, "kind": "update"})
	}
}

// pnpmBin 返回可用 pnpm 可执行文件路径（PATH 优先，其次便携 node 目录），无则空串。
func (m *DSHProcessManager) pnpmBin() (string, error) {
	if p, err := exec.LookPath("pnpm"); err == nil {
		return p, nil
	}
	nodeDir := m.nodeEnv.runtimeDir()
	if nodeDir != "" {
		cand := filepath.Join(nodeDir, "pnpm"+exeExt())
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("pnpm 不可用")
}

// backupProfileDeps 将 profile 的 package.json(+pnpm-lock.yaml) 备份到
// DSH_HOME/backups/web-<时间戳>/，返回备份目录。
func (m *DSHProcessManager) backupProfileDeps(profileDir string, logf func(string, string)) (string, error) {
	pkg := filepath.Join(profileDir, "package.json")
	if _, err := os.Stat(pkg); err != nil {
		return "", fmt.Errorf("找不到 profile package.json: %s", pkg)
	}
	backupDir := filepath.Join(m.nodeEnv.DshHome(), "backups", "web-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}
	if err := copyFile(pkg, filepath.Join(backupDir, "package.json")); err != nil {
		return "", err
	}
	if lock := filepath.Join(profileDir, "pnpm-lock.yaml"); statOk(lock) {
		if err := copyFile(lock, filepath.Join(backupDir, "pnpm-lock.yaml")); err != nil {
			return "", err
		}
	}
	logf("info", fmt.Sprintf("已备份插件配置到 %s", backupDir))
	return backupDir, nil
}

// latestBackupDir 返回最近一次备份目录：内存指针优先，否则 backups/ 下按名称（含时间戳）排序取最新。
func (m *DSHProcessManager) latestBackupDir() string {
	if m.lastPluginBackup != "" {
		if statOk(m.lastPluginBackup) {
			return m.lastPluginBackup
		}
	}
	backupRoot := filepath.Join(m.nodeEnv.DshHome(), "backups")
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "web-") {
			dirs = append(dirs, filepath.Join(backupRoot, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs) // 名称含时间戳，字典序即时间序
	return dirs[len(dirs)-1]
}

// copyFile 文件逐字节复制（备份/回滚用，简单可靠）。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// statOk 文件/目录存在返回 true（封装 os.Stat 的 err 判断，避免重复啰嗦）。
func statOk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
	nodeDir := m.nodeEnv.runtimeDir()
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
	sysutil.Hide(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stdout log goroutine panic: %v\n", r)
			}
		}()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			logf("info", sc.Text())
		}
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("QuickDock: DSH stderr log goroutine panic: %v\n", r)
			}
		}()
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

// splitQuoted 按空格拆分，尊重双引号包裹（NODE_OPTIONS 的 --require="C:/Program Files/..." 路径带空格）

// quoteJoin 重拼 NODE_OPTIONS：含空格的 token 重新加引号

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
		if starting {
			// 冷启动进行中（窗口已显示 loading）：直接复用，绝不能销毁重建——否则 Stop()
			// 会杀掉正在启动的 dsh 再重来一遍。
			win.Show()
			win.Focus()
			return dshLoadingPage, nil // 启动中：返回 loading 页地址，前端仅作提示
		}
		if url != "" {
			// 有可用地址：先实时确认 dsh 仍真正可达（m.url 可能因进程崩溃/外部 dsh 退出而
			// 过期，reaper 与窗口关闭未必及时清空）。若已死还直接 Show()，窗口会卡在浏览器
			// "无法访问此网站"；因此先探测，不可达则销毁旧窗口并同步清空过期 url 走重建流程。
			if m.dshReachable(url) {
				win.Show()
				win.Focus()
				return url, nil
			}
			m.mu.Lock()
			m.window = nil
			m.url = "" // 同步清空过期地址，避免后续又把死地址误判为"已有服务"
			m.mu.Unlock()
			win.Close() // 销毁旧窗口（WindowClosing handler 仅清引用，服务停止由上层负责）
		} else {
			// 进程已退出（reaper 已清空 url）但窗口残留：销毁旧窗口走下方重建流程，
			// 否则返回空 URL 且窗口显示死页面，前端无任何提示。
			m.mu.Lock()
			m.window = nil
			m.mu.Unlock()
			win.Close()
		}
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
	// 关闭窗口（Wails 内部 handler 负责真正销毁）时只清窗口引用，不停 dsh 服务：
	// dsh web 进程独立于窗口运行（自动启动/设置里手动启停负责生命周期），关窗口后
	// 服务继续在后台跑，下次点击侧边栏 dsh 秒开。真正的停止只发生在设置了手动停止
	// 或 QuickDock 退出（ServiceShutdown）。
	win.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		m.mu.Lock()
		m.window = nil
		m.mu.Unlock()
	})

	m.mu.Lock()
	m.window = win
	m.mu.Unlock()
	win.Show()

	// 冷启动：等 waitReady goroutine 通知后 Navigate 到真实 dsh 地址
	if starting && ready != nil {
		go func(w *application.WebviewWindow, target string, rd <-chan struct{}) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("QuickDock: DSH navigate goroutine panic: %v\n", r)
				}
			}()
			select {
			case <-rd:
				m.mu.Lock()
				err := m.readyErr
				m.mu.Unlock()
				if err != nil {
					// 启动失败：不把窗口甩到死地址（浏览器"无法访问此网站"），显示内置友好错误页
					w.SetURL(dshErrorPage(err.Error()))
					return
				}
				// 就绪后到实际导航之间存在竞态窗口：dsh 可能刚响应完就抖动/崩溃。先做一次
				// 可达性重试（≤10s），确认真正可达才导航；否则显示友好错误页，绝不把窗口
				// 甩到死地址而让用户直面浏览器"无法访问此网站"。
				if !m.waitReachableThenNavigate(w, target, 10*time.Second) {
					w.SetURL(dshErrorPage("dsh 启动后无法访问（服务不可达），请关闭本窗口后重新点击导航重试，或在「环境管理 → DeepSeek Harness」查看日志"))
				}
			case <-time.After(50 * time.Second): // 兜底 > waitReady 45s 上限
				w.SetURL(dshErrorPage("dsh 启动超时（超过 50 秒），请关闭窗口后重新点击导航重试，或在「环境管理 → DeepSeek Harness」查看日志"))
			}
		}(win, url, ready)
	}
	return url, nil
}
