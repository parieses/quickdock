package services

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
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
	window  *application.WebviewWindow
}

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
	// 固定官方默认端口 3080，软件内窗口与浏览器访问同一地址；
	// 仅当 3080 被占用（如用户已手动起了 dsh）才退回随机端口并提示。
	port := DefaultDSHPort
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(DefaultDSHPort)); err != nil {
		free, ferr := FindFreePort()
		if ferr != nil {
			return "", ferr
		}
		port = free
		if m.app != nil {
			m.app.Event.Emit("quickdock:dsh:log", setupLog{Level: "warn", Message: fmt.Sprintf("默认端口 %d 已被占用，本次改用随机端口 %d", DefaultDSHPort, free)})
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
	m.cmd = cmd
	m.port = port
	m.exit = exit
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
		}
		m.mu.Unlock()
	}()

	// 就绪检查：监听端口是我们自己选的，直接构造 URL 轮询
	u := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := m.waitReady(u, exit); err != nil {
		// 注意：此处已持有 m.mu，绝不能调 m.Stop()（非重入锁二次加锁），走内部 stopLocked()
		m.stopLocked()
		return "", err
	}
	m.url = u
	return u, nil
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
		return
	}
	cmd := m.cmd
	exit := m.exit
	m.cmd = nil
	m.url = ""
	m.port = 0
	m.exit = nil
	if exit == nil {
		// 防御：理论上 Start 一定先建 exit；万一没有就强杀
		_ = cmd.Process.Kill()
		return
	}
	if runtime.GOOS == "windows" {
		// Windows 不支持 os.Interrupt 信号（Signal 必然报错），旧实现每次 Stop 都白等
		// 3s 超时才 taskkill，关 DSH 窗口有明显延迟。这里直接 taskkill /T /F 杀进程树
		// （dsh 的 node 子进程一并清理，不残留占端口）；reaper 的 cmd.Wait() 随之返回并清状态。
		exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
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

	// 已有窗口：直接复用（含首次点击尚在启动中、用户连点的情况）
	m.mu.Lock()
	win, url := m.window, m.url
	m.mu.Unlock()
	if win != nil {
		if url != "" {
			win.Show()
			win.Focus()
			return url, nil
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

	win = m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "快启坞 - DeepSeek Harness",
		Width:            1200,
		Height:           800,
		MinWidth:         600,
		MinHeight:        400,
		BackgroundColour: application.RGBA{Red: 23, Green: 24, Blue: 27, Alpha: 255},
		URL:              url,
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
	return url, nil
}
