package plugin

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	_ "modernc.org/sqlite"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/internal/sysutil"
)

// pidFileVersion 用于兼容未来格式变更
const pidFileVersion = 1

// statusRegistered 表示插件「仅登记、未启动」的懒加载占位状态：创建实例并写入 m.plugins，
// 但不起进程 / 不起 goja VM / 不执行 initialize 握手。首次使用时经 EnsureLoaded 惰性拉起。
const statusRegistered = "registered"

// statusStarting 表示插件正在启动、等待 initialize 握手完成（瞬时状态）。
const statusStarting = "starting"

// pluginIdleDegradeAfter 后端进程闲置（窗口不可见且无命令交互）超过该时长后自动降级为
// registered（就绪）：杀进程释放 CPU/内存，下次打开窗口/执行命令经 EnsureLoaded 复活。
// 与 window_manager 的 pluginWindowRecycleAfter（回收 WebView2 渲染进程）共同构成
// 「关闭即闲置 → 定时全量卸载」模型，对齐 Chrome MV3 的 idle unload。
const pluginIdleDegradeAfter = 10 * time.Minute

// pidFileData PID 文件结构
type pidFileData struct {
	Version   int            `json:"version"`
	PIDs      map[string]int `json:"pids"` // pluginID → PID
	CreatedAt string         `json:"created_at"`
}

// HostMethod 处理插件发起的回调请求
type HostMethod func(pluginID string, params json.RawMessage) (interface{}, error)

// Manager 插件管理器
type Manager struct {
	plugins     map[string]*PluginInstance
	mu          sync.RWMutex
	pluginsDir  string
	hostMethods map[string]HostMethod
	pidFilePath string
	pidMu       sync.Mutex

	// loadLocks 按 pluginID 串行化 LoadPlugin：崩溃自动重启(watchPlugin 退避 2-6s)与
	// 用户手动触发(EnsureLoaded / ReloadPlugin 启用)可能并发加载同 ID——旧实现 Start
	// 子进程与登记 map 之间无锁，后登记覆盖先登记，先启动的进程脱管泄漏。
	// per-ID 锁保证同 ID 串行，不同 ID 仍可并行（DiscoverAndLoad 依赖并发提速）。
	loadLocks   map[string]*sync.Mutex
	loadLocksMu sync.Mutex

	// EnableLazyPluginLoad 控制启动期是否仅登记插件（懒加载）。
	// true（默认）：DiscoverAndLoad 只 RegisterPlugin，后端进程/goja VM 延后到首次使用；
	// false：沿用旧行为，启动即对全部启用插件调用 LoadPlugin。
	// 作为一键回滚开关，发现懒加载异常时置 false 即可恢复旧链路。
	EnableLazyPluginLoad bool

	// idleDegradeAfter 闲置降级阈值（默认 pluginIdleDegradeAfter，可覆盖以适配测试）。
	idleDegradeAfter time.Duration
	// isWindowVisible 注入的窗口可见性判定（同包窗口管理器），nil 时保守跳过降级。
	isWindowVisible func(pluginID string) bool
	idleStopCh     chan struct{}
	idleStopOnce   sync.Once
}

// NewManager 创建插件管理器
func NewManager(pluginsDir string) *Manager {
	m := &Manager{
		plugins:     make(map[string]*PluginInstance),
		pluginsDir:  pluginsDir,
		hostMethods: make(map[string]HostMethod),
		pidFilePath: filepath.Join(filepath.Dir(pluginsDir), "plugin_pids.json"),
		loadLocks:   make(map[string]*sync.Mutex),

		EnableLazyPluginLoad: true,

		idleDegradeAfter: pluginIdleDegradeAfter,
		idleStopCh:       make(chan struct{}),
	}

	m.registerDefaultHostMethods()

	// 启动时清理上一次残留的插件进程
	m.cleanupOrphans()

	// 启动时恢复被中断的插件安装（解压中途崩溃留下的 *.rollback 标记）
	m.recoverInterruptedInstalls()

	// 后台闲置降级循环：将超过阈值未使用的后端进程降级为就绪（释放资源），
	// 下次使用经 EnsureLoaded 复活。进程退出时由 ShutdownAll 关闭 idleStopCh 停止。
	go m.idleDegradeLoop()

	return m
}

// recoverInterruptedInstalls 启动时扫描 *.rollback 标记，恢复上一次被中断的插件安装。
// 安装器会在解压前写 rollback 标记（内容是备份目录路径），defer/失败统一回滚；若进程在
// 解压中途崩溃（来不及走回滚），下次启动靠这里兜底：删除残缺新目录 + 还原备份。
func (m *Manager) recoverInterruptedInstalls() {
	marks, err := filepath.Glob(filepath.Join(m.pluginsDir, "*.rollback"))
	if err != nil {
		return
	}
	for _, mark := range marks {
		targetDir := strings.TrimSuffix(mark, ".rollback")
		data, rerr := os.ReadFile(mark)
		if rerr != nil {
			logger.W("无法读取 rollback 标记 %s，跳过: %v", mark, rerr)
			continue
		}
		backupDir := strings.TrimSpace(string(data))
		if rerr := rollbackInstall(targetDir, backupDir, mark); rerr != nil {
			logger.E("恢复中断安装 %s 失败: %v", targetDir, rerr)
		} else {
			logger.I("已恢复中断的插件安装：%s", targetDir)
		}
	}
}

// RegisterHostMethod 注册 Host Method
func (m *Manager) RegisterHostMethod(name string, handler HostMethod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hostMethods[name] = handler
}

// DiscoverAndLoad 扫描插件目录，加载「已启用」的插件。
//
// isEnabled 为启用判定回调：返回 false 的插件直接跳过——不启动后端进程、不进入插件列表。
// 传 nil 表示不过滤，加载磁盘上全部插件。
//
// 主程序必须传启用判定。磁盘目录只代表「曾经被放到了这里」，真正的「已安装且已启用」
// 只有数据库知道（插件页的启用开关、卸载都写 DB，磁盘目录可能残留）。此前这里无过滤
// 全量加载、再由生命周期按 DB 逐个停掉，等于把每个禁用插件先点火再灭火：native 插件
// 在 LoadPlugin 内会真的启动子进程，而停止要走 taskkill（单次约 0.9~1.3s）且全程串行，
// 磁盘上 40 个未注册插件能拖出 25 秒启动延迟。
//
// 并发加载：native 插件初始化最坏 15s，串行会 N×15s 阻塞主程序启动
func (m *Manager) DiscoverAndLoad(isEnabled func(pluginID string) bool) error {
	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在时不是错误
		}
		return err
	}

	type pluginJob struct {
		manifest PluginManifest
		dir      string
	}
	jobs := make([]pluginJob, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// 跳过备份/残留目录：安装升级时会生成 `<id>.bak.<version>` / `<id>.broken-<v>.bak`
		// 等含完整 plugin.json 的副本，不过滤会被当成新插件加载——卸载后重启又"复活"
		if strings.Contains(name, ".bak.") || strings.HasSuffix(name, ".bak") ||
			strings.Contains(name, ".broken") || strings.HasSuffix(name, ".old") {
			continue
		}
		manifestPath := filepath.Join(m.pluginsDir, name, "plugin.json")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		manifest, err := LoadManifest(manifestPath)
		if err != nil {
			logger.W("插件 %s 清单加载失败: %v", entry.Name(), err)
			continue
		}
		// 跳过当前平台不支持的插件
		if !IsPlatformSupported(manifest) {
			logger.W("跳过插件 %s（不支持当前平台 %s）", manifest.ID, runtime.GOOS)
			continue
		}
		// 已禁用插件不再在此处跳过：它必须登记进 m.plugins，否则 ListPlugins 不返回、
		// 管理页无法重新启用（禁用即消失、无法再启用的 bug）。真正的「启用/禁用」分流见下方：
		// 仅启用的插件才真正拉起后端进程（native 的 cmd.Start() 必须在启用判定之后）。
		jobs = append(jobs, pluginJob{manifest: *manifest, dir: filepath.Join(m.pluginsDir, entry.Name())})
	}

	// 懒加载（默认）：登记全部插件（含已禁用），不起后端进程 / goja VM。native 的 cmd.Start()
	// + initialize 握手延后到首次 EnsureLoaded（打开插件页 / 执行命令 / ShowPluginWindow 都经它）。
	// 直接消掉「启动时把全部 native 插件都拉成常驻进程」的开销——实测 26 个 native 合计
	// Private ~340 MB / RSS ~60 MB，而日常只会用到其中少数几个。
	// 置 EnableLazyPluginLoad=false 可一键回滚到「启动即全量加载」的旧链路。
	// 已禁用插件登记后压成 stopped：管理页可见且可重新启用，命令面板按 status 过滤掉 stopped
	//（见 CommandPalette.loadPluginIndex 的 p.status !== 'stopped' 过滤，与 DisablePlugin 只置 stopped 的契约一致）。
	if m.EnableLazyPluginLoad {
		for _, job := range jobs {
			m.RegisterPlugin(job.manifest, job.dir)
			if isEnabled != nil && !isEnabled(job.manifest.ID) {
				if inst, ok := m.plugins[job.manifest.ID]; ok {
					inst.SetStatus("stopped")
					inst.stopped.Store(true)
					inst.disabled.Store(true)
				}
			}
		}
		logger.I("插件懒加载：已登记 %d 个插件（含已禁用），后端进程延后到首次使用", len(jobs))
		return nil
	}

	var wg sync.WaitGroup
	for _, job := range jobs {
		// 已禁用插件：只登记、不启动进程（status=stopped，管理页可重新启用）
		if isEnabled != nil && !isEnabled(job.manifest.ID) {
			m.RegisterPlugin(job.manifest, job.dir)
			if inst, ok := m.plugins[job.manifest.ID]; ok {
				inst.SetStatus("stopped")
				inst.stopped.Store(true)
				inst.disabled.Store(true) // 与懒加载分支一致：禁用标记缺失会导致管理页不显示"已禁用"徽标
			}
			continue
		}
		wg.Add(1)
		go func(j pluginJob) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.E("plugin load worker panic: %v", r)
				}
			}()
			if err := m.LoadPlugin(j.manifest, j.dir); err != nil {
				logger.E("插件 %s 启动失败: %v", j.manifest.ID, err)
			}
		}(job)
	}
	wg.Wait()
	return nil
}

// RegisterPlugin 仅登记插件元信息（创建实例并写入 m.plugins），不启动后端进程 / 不起 goja
// VM / 不执行 initialize 握手。真正的启动延后到首次使用：ExecuteCommand / 打开插件页 /
// ShowPluginWindow 均经 EnsureLoaded 惰性拉起。
//
// 这是 P-2 懒加载的核心：把启动墙钟从「Σ(启用插件初始化)」降到「仅文件读取 + map 写入」，
// 启动耗时不再随启用插件数线性增长。none 运行时无后端进程，登记即视为可用，直接置 running。
func (m *Manager) RegisterPlugin(manifest PluginManifest, dir string) {
	inst := NewPluginInstance(manifest, dir)
	if manifest.Backend.Runtime == "none" {
		inst.SetStatus("running")
		close(inst.readyCh)
	} else {
		inst.SetStatus(statusRegistered)
	}
	m.mu.Lock()
	m.plugins[manifest.ID] = inst
	m.mu.Unlock()
}

// loadLock 获取指定插件的 per-ID 加载锁，返回解锁函数。
// 同 ID 的 LoadPlugin 串行执行；不同 ID 互不阻塞。
func (m *Manager) loadLock(pluginID string) func() {
	m.loadLocksMu.Lock()
	if m.loadLocks == nil {
		m.loadLocks = make(map[string]*sync.Mutex)
	}
	lk, ok := m.loadLocks[pluginID]
	if !ok {
		lk = &sync.Mutex{}
		m.loadLocks[pluginID] = lk
	}
	m.loadLocksMu.Unlock()
	lk.Lock()
	return lk.Unlock
}

// LoadPlugin 注册一个插件（none/goja 立即初始化，native 延迟到首次使用）
func (m *Manager) LoadPlugin(manifest PluginManifest, dir string) error {
	// per-ID 串行化：崩溃重启 / 用户手动加载并发触发同 ID 时，先到者完成后再放行后者
	unlock := m.loadLock(manifest.ID)
	defer unlock()

	// 双保险：若等待锁期间先行者已完成加载且实例健康，直接复用，不再停旧重启
	m.mu.RLock()
	if inst, ok := m.plugins[manifest.ID]; ok && inst.GetStatus() == "running" {
		m.mu.RUnlock()
		logger.I("插件 %s 已在运行，跳过重复加载", manifest.ID)
		return nil
	}
	m.mu.RUnlock()

	// 先获取插件ID并检查是否需要停止旧实例
	// registered 状态是「仅登记未启动」的懒加载占位实例，没有可停止的进程/VM，跳过避免误置 stopped 标记
	m.mu.Lock()
	if inst, ok := m.plugins[manifest.ID]; ok && inst.GetStatus() != statusRegistered {
		m.stopPlugin(inst, false)
	}
	m.mu.Unlock()

	switch manifest.Backend.Runtime {
	case "none":
		inst := NewPluginInstance(manifest, dir)
		inst.SetStatus("running")
		close(inst.readyCh)
		m.mu.Lock()
		m.plugins[manifest.ID] = inst
		m.mu.Unlock()
		return nil
	case "goja":
		entryPath, err := safePluginEntry(dir, manifest.Backend.Entry)
		if err != nil {
			return err
		}
		return m.loadGojaPlugin(manifest, dir, entryPath)
	case "native":
		entryPath, err := safePluginEntry(dir, manifest.Backend.Entry)
		if err != nil {
			return err
		}
		// sysutil.Command → Hide：子进程继承宿主的隐藏控制台（main 早期由
		// sysutil.InitHiddenConsole 建立），整个进程树共用 1 个 conhost；
		// 若宿主没有可继承的隐藏控制台，Hide 会自动退回 CREATE_NO_WINDOW 兜底。
		// 注意：不能改用 DETACHED_PROCESS(0x00000008)——它让进程脱离控制台，插件内部
		// 拉起的孙进程会各自弹出可见黑框。
		cmd := sysutil.Command(entryPath, manifest.Backend.Args...)
		cmd.Dir = dir

		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("创建 stdin pipe 失败: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("创建 stdout pipe 失败: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("创建 stderr pipe 失败: %w", err)
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.PluginE(manifest.ID, "stderr reader panic: %v", r)
				}
			}()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				logger.PluginW(manifest.ID, "%s", scanner.Text())
			}
		}()

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("插件进程启动失败: %w", err)
		}

		// 挂入 Job Object：主进程异常退出时由内核一并回收，不留孤儿进程。
		assignProcessToJob(uintptr(cmd.Process.Pid))

		inst := NewPluginInstance(manifest, dir)
		inst.Cmd = cmd
		inst.Stdin = stdin
		inst.Stdout = stdout
		inst.SetStatus(statusStarting)

		m.mu.Lock()
		m.plugins[manifest.ID] = inst
		m.mu.Unlock()

		go inst.readLoop(m)
		go m.watchPlugin(inst)
		<-inst.readyCh

		_, err = inst.Call("initialize", map[string]interface{}{
			"hostVersion": "3.0.0",
			"pluginDir":   dir,
		}, 15*time.Second)
		if err != nil {
			m.mu.Lock()
			if current, ok := m.plugins[manifest.ID]; ok && current == inst {
				delete(m.plugins, manifest.ID)
			}
			m.stopPlugin(inst, false)
			m.mu.Unlock()
			return fmt.Errorf("插件初始化失败: %w", err)
		}

		inst.SetStatus("running")
		return nil
	default:
		return ErrUnsupportedRuntime
	}
}

// safePluginEntry 校验插件 backend.entry 为插件目录内的相对路径（防 .. 路径穿越逃逸到任意可执行文件）
func safePluginEntry(dir, entry string) (string, error) {
	if entry == "" {
		return "", fmt.Errorf("backend.entry 不能为空")
	}
	if filepath.IsAbs(entry) {
		return "", fmt.Errorf("backend.entry 必须是相对路径: %s", entry)
	}
	clean := filepath.Clean(entry)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backend.entry 包含非法路径片段: %s", entry)
	}
	return filepath.Join(dir, clean), nil
}

// loadGojaPlugin 加载并执行 Goja JS 插件（在进程中运行，不启动子进程）
func (m *Manager) loadGojaPlugin(manifest PluginManifest, dir, entryPath string) error {
	jsCode, err := os.ReadFile(entryPath)
	if err != nil {
		return fmt.Errorf("读取插件 JS 文件失败: %w", err)
	}
	vm := goja.New()
	vm.Set("__pluginId", manifest.ID)
	vm.Set("__pluginDir", dir)

	dataDir := filepath.Join(platform.DefaultDataDir(), "data", manifest.ID)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建插件数据目录失败: %w", err)
	}
	dbPath := filepath.Join(dataDir, "data.db")
	pluginDB, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return fmt.Errorf("打开插件数据库失败: %w", err)
	}
	pluginDB.SetMaxOpenConns(1)

	vm.Set("api", m.gojaAPI(manifest, pluginDB))

	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("执行插件 JS 时 panic: %v", r)
			}
		}()
		// 顶层执行加超时：死循环/重型计算的插件脚本会卡死整个应用主线程
		timer := time.AfterFunc(10*time.Second, func() { vm.Interrupt("插件顶层脚本执行超时") })
		defer timer.Stop()
		_, err = vm.RunString(string(jsCode))
	}()
	if err != nil {
		pluginDB.Close() // 失败路径关闭数据库，避免泄漏
		return fmt.Errorf("执行插件 JS 失败: %w", err)
	}

	hasInit := vm.Get("handleInitialize") != nil && !goja.IsUndefined(vm.Get("handleInitialize"))
	hasExec := vm.Get("handleExecute") != nil && !goja.IsUndefined(vm.Get("handleExecute"))
	if !hasExec {
		pluginDB.Close() // 失败路径关闭数据库，避免泄漏
		return fmt.Errorf("插件需要导出 handleExecute 函数")
	}

	inst := NewPluginInstance(manifest, dir)
	inst.VM = vm
	inst.DB = pluginDB
	inst.SetStatus("running")
	close(inst.readyCh)
	m.mu.Lock()
	m.plugins[manifest.ID] = inst
	m.mu.Unlock()

	if hasInit {
		if _, ierr := inst.callGojaJS("handleInitialize", map[string]interface{}{}, 10*time.Second); ierr != nil {
			logger.PluginE(manifest.ID, "handleInitialize 执行失败: %v", ierr)
		}
	}
	return nil
}

// gojaAPI 构造注入给 goja 插件的 api 对象。
// 独立成方法而非内联在 loadGojaPlugin 中，便于单元测试直接断言绑定内容
// （无需真正启动 goja VM 即可验证转发行为）。
//
// 能力面：
//   - api.log / api.warn / api.error        写插件专属日志文件
//   - api.host(method, params)              转发到宿主 hostMethods 注册表，与 native 的
//     JSON-RPC 回调共用 invokeHostMethod，因此 http.get/post、host.notify、
//     host.dialog.*、host.clipboard.*、db.* 对 goja 插件同样可用（此前一概不可达）
//   - api.db.exec / api.db.query            插件私有 SQLite（data.db）的裸 SQL。
//     注意这与 native 的 db.*（宿主库中按 plugin_id 隔离的 KV）不是同一套存储
//
// 约束：api.host 是同步阻塞调用，受 callGojaJS 的超时（handleExecute 20s）限制。
// 需要用户交互的方法（host.dialog.open/save）在用户完成操作前就可能被 vm.Interrupt
// 打断，goja 插件不宜依赖这类方法；长耗时任务应走 taskId + 轮询的异步范式。
func (m *Manager) gojaAPI(manifest PluginManifest, pluginDB *sql.DB) map[string]interface{} {
	return map[string]interface{}{
		// 日志：写插件专属日志文件 plugin-YYYYMMDD.log（[plugin:<id>] 前缀），与主日志分离
		"log":   func(msg string) { logger.PluginI(manifest.ID, "%s", msg) },
		"warn":  func(msg string) { logger.PluginW(manifest.ID, "%s", msg) },
		"error": func(msg string) { logger.PluginE(manifest.ID, "%s", msg) },

		// 通用 host 转发：等价于 native 侧发起同名 JSON-RPC 请求，返回值/错误语义一致。
		// 未传参数时按空对象 {} 处理，避免各 handler 收到 null 而解析失败。
		"host": func(method string, params interface{}) (interface{}, error) {
			raw := json.RawMessage("{}")
			if params != nil {
				b, err := json.Marshal(params)
				if err != nil {
					return nil, fmt.Errorf("host 参数序列化失败: %w", err)
				}
				raw = b
			}
			return m.invokeHostMethod(manifest.ID, method, raw)
		},

		"db": map[string]interface{}{
			"exec": func(sql string, args ...interface{}) (map[string]interface{}, error) {
				res, e := pluginDB.Exec(sql, args...)
				if e != nil {
					return nil, e
				}
				id, _ := res.LastInsertId()
				ra, _ := res.RowsAffected()
				return map[string]interface{}{"lastId": id, "rowsAffected": ra}, nil
			},
			"query": func(sql string, args ...interface{}) ([]map[string]interface{}, error) {
				rows, e := pluginDB.Query(sql, args...)
				if e != nil {
					return nil, e
				}
				defer rows.Close()
				cols, _ := rows.Columns()
				var results []map[string]interface{}
				for rows.Next() {
					vals := make([]interface{}, len(cols))
					valPtrs := make([]interface{}, len(cols))
					for i := range vals {
						valPtrs[i] = &vals[i]
					}
					rows.Scan(valPtrs...)
					row := make(map[string]interface{})
					for i, c := range cols {
						switch v := vals[i].(type) {
						case []byte:
							row[c] = string(v)
						default:
							row[c] = v
						}
					}
					results = append(results, row)
				}
				return results, nil
			},
		},
	}
}

// UnloadPlugin 卸载插件（从内存移除）
func (m *Manager) UnloadPlugin(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.plugins[id]; ok {
		m.stopPlugin(inst, false)
		delete(m.plugins, id)
	}
}

// StopPlugin 停止插件但保留在列表中（禁用时调用）
func (m *Manager) StopPlugin(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.plugins[id]
	if !ok {
		return ErrPluginNotFound
	}
	m.stopPlugin(inst, true)
	// 注意：stopPlugin 已将 inst.Status 设置为 "stopped"
	return nil
}

// StopPluginOnWindowClose 插件 UI 窗口被用户关闭时调用。
// 新模型：插件后端是常驻后台进程，窗口只是它的一个视图。关闭窗口只销毁视图，
// 不杀后端进程——后端继续在后台运行，下次打开窗口直接复用（status 保持 running）。
// 仅 best-effort 通知插件「窗口已关闭」以便释放窗口相关资源；绝不置 stopped，
// 也不调 stopPlugin（否则会杀掉常驻后端，违背「关闭窗口不杀插件」）。
func (m *Manager) StopPluginOnWindowClose(id string) error {
	m.mu.RLock()
	inst, ok := m.plugins[id]
	m.mu.RUnlock()
	if !ok {
		return ErrPluginNotFound
	}
	// 后端无窗口概念（native 有进程、goja/none 无进程），仅 best-effort 通知，
	// 失败忽略：不阻塞窗口关闭、不影响后端常驻。
	if inst.Stdin != nil {
		go func() { _ = inst.SendNotification("window:closed", nil) }()
	}
	return nil
}

// KillPlugin 强制终止插件（管理页「停止进程」/ 插件窗口「强制关闭」入口）：
// 停进程并断自动重启（stopPlugin 置 stopped，watchPlugin 不会复活），
// 内部已连子进程树一并终止；再补杀目录内未被 m.plugins 跟踪的孤儿进程，
// 覆盖「进程锁住目录导致更新/卸载失败」。
// 杀进程 ≠ 禁用：终止后状态置回 registered（懒加载就绪占位），插件仍是启用态、
// 命令面板可见，下次打开窗口 / 执行命令经 EnsureLoaded 自动拉起——无需手动再启用。
// 保留内存实例（不 delete），否则插件会从 ListPlugins 凭空消失（状态撕裂）。
func (m *Manager) KillPlugin(id string) error {
	if !pluginIDRe.MatchString(id) {
		return fmt.Errorf("%w: 非法插件 ID: %q", ErrInvalidManifest, id)
	}
	m.mu.Lock()
	if inst, ok := m.plugins[id]; ok {
		m.stopPlugin(inst, true)
		// 杀进程后回到「就绪」而非「已停止」：用户意图是终结当前进程（卡死/锁目录），
		// 不是停用插件。registered 与启动期懒加载占位完全同态，EnsureLoaded 可按需复活。
		// GetStatus 在 disabled=true 时兜底 stopped，故禁用插件不受此处影响。
		inst.SetStatus(statusRegistered)
	}
	m.mu.Unlock()

	killProcessesLockingDir(filepath.Join(m.pluginsDir, id))
	return nil
}

// stopPlugin 停止插件子进程（内部共享逻辑，调用方需持 m.mu）。
// treeKill=true 时连其 spawn 的子进程（explorer / netsh / ping 等）一并终止，
// 防止反复开关节点插件时孤儿进程在全局 Job 中累积（"任务管理器进程数越来越多"）。
// 该参数仅在「用户关闭/禁用/强杀插件」这类明确的插件终止路径（StopPlugin / KillPlugin）
// 置 true；内部的重载、初始化失败、卸载等路径只杀主进程，不扩散到子进程树。
func (m *Manager) stopPlugin(inst *PluginInstance, treeKill bool) {
	logger.PluginI(inst.Manifest.ID, "宿主停止插件（treeKill=%v）", treeKill)
	inst.stopped.Store(true)
	// 发送 shutdown（goja/none 运行时无 stdin pipe，跳过）。
	// 非阻塞 best-effort：在独立 goroutine 中尝试，避免持有管理器锁期间被插件挂死
	//（SendNotification 内部虽有 2s 超时，但 stopPlugin 总在调用方持 m.mu 时执行，
	// 同步等待会给整把锁带来最长 2s 阻塞，冻结所有插件操作）。graceful shutdown 本身即尽力而为。
	if inst.Stdin != nil {
		go func() { _ = inst.SendNotification("shutdown", nil) }()
	}

	inst.SetStatus("stopped")
	inst.Close()

	// 关闭 goja 插件数据库
	if inst.DB != nil {
		inst.DB.Close()
	}

	// 终止进程：先置 stopped（阻止 watchPlugin 复活），再回收主进程。
	// treeKill 必须主进程尚存活时调用，否则 taskkill /T 找不到父进程会漏杀子进程。
	if inst.Cmd != nil && inst.Cmd.Process != nil {
		pid := inst.Cmd.Process.Pid
		if treeKill {
			killProcessTree(pid)
		} else {
			_ = inst.Cmd.Process.Kill()
		}
		_, _ = inst.Cmd.Process.Wait()
	}

	// 更新 PID 文件（调用者持有写锁，直接传 m.plugins 安全）
	m.safeWritePidFile(m.plugins)
}

// watchPlugin 等待插件退出，崩溃时自动重启（最多 3 次指数退避）
func (m *Manager) watchPlugin(inst *PluginInstance) {
	defer func() {
		if r := recover(); r != nil {
			logger.E("[PANIC] watchPlugin %s: %v", inst.Manifest.ID, r)
		}
	}()
	<-inst.doneCh

	if inst.stopped.Load() {
		return // 用户主动停止，不重启
	}

	logger.W("插件 %s 崩溃，尝试自动重启...", inst.Manifest.ID)
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(time.Duration(attempt*2) * time.Second) // 2s, 4s, 6s

		// 退避期间可能已被用户主动停止或触发安装/卸载（stopPlugin 置 stopped=true）：
		// 此时绝不能重启——否则新进程会锁住插件目录，导致安装备份 rename 失败
		//（"The process cannot access the file because it is being used by another process"）。
		if inst.stopped.Load() {
			return
		}

		// 重新加载插件
		if err := m.LoadPlugin(inst.Manifest, inst.Dir); err != nil {
			logger.E("插件 %s 重启第 %d 次失败: %v", inst.Manifest.ID, attempt, err)
			continue
		}
		logger.I("插件 %s 自动重启成功", inst.Manifest.ID)
		return
	}
	logger.W("插件 %s 已达最大重启次数，放弃", inst.Manifest.ID)
}

// PluginsDir 返回插件安装目录
func (m *Manager) PluginsDir() string {
	return m.pluginsDir
}

// callGojaJS 调用 goja 插件中导出的 JS 函数（带超时，防死循环卡死应用）。
// goja.Runtime 非线程安全，ExecuteCommand/handleInitialize 均可并发触发，故全程加 vmMu 串行化。
func (inst *PluginInstance) callGojaJS(fnName string, params map[string]interface{}, timeout time.Duration) (interface{}, error) {
	inst.vmMu.Lock()
	defer inst.vmMu.Unlock()
	if inst.VM == nil {
		return nil, fmt.Errorf("goja VM 未初始化")
	}
	fnVal := inst.VM.Get(fnName)
	if fnVal == nil || goja.IsUndefined(fnVal) {
		return nil, fmt.Errorf("插件未导出函数 %s", fnName)
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, fmt.Errorf("函数 %s 不可调用", fnName)
	}
	// 清除上一次超时可能残留的中断信号，避免误伤本次调用
	inst.VM.ClearInterrupt()
	// Interrupt 是 goja 异步中断机制，可由定时器 goroutine 在 vmMu 之外触发，
	// 用于打断阻塞在锁内的长任务；因此定时器不参与 vmMu 串行化。
	timer := time.AfterFunc(timeout, func() { inst.VM.Interrupt("插件函数执行超时") })
	defer timer.Stop()
	result, err := fn(goja.Undefined(), inst.VM.ToValue(params))
	if err != nil {
		return nil, err
	}
	return result.Export(), nil
}

// ExecuteCommand 执行插件命令（供 Wails 前端调用）
func (m *Manager) ExecuteCommand(pluginID, commandID string, input map[string]interface{}) (json.RawMessage, error) {
	m.MarkActive(pluginID)
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if !ok || inst.GetStatus() != "running" {
		// 「关窗即终止」后插件可能已停止：按需惰性复活，避免命令面板调用失败
		if err := m.EnsureLoaded(pluginID); err != nil {
			return nil, fmt.Errorf("插件 %s 未运行且无法加载: %w", pluginID, err)
		}
		m.mu.RLock()
		inst = m.plugins[pluginID]
		m.mu.RUnlock()
		if inst == nil {
			return nil, ErrPluginNotFound
		}
	}

	switch inst.Manifest.Backend.Runtime {
	case "native":
		if inst.GetStatus() != "running" {
			return nil, fmt.Errorf("插件 %s 未在运行（状态: %s）", pluginID, inst.GetStatus())
		}
		return inst.Call("plugin.execute", map[string]interface{}{
			"command": commandID,
			"input":   input,
		}, 20*time.Second)
	case "none":
		// 无后端插件：命令由前端自行处理，宿主不执行任何 host 逻辑。
		// 返回值仅表示「调用通道正常」，不等于「宿主已执行」——真实能力由 PluginInfo.Runtime 表达，
		// 调用方（尤其是 MCP/AI）须据此判断，不可把此返回值当作插件真的做了事。
		logger.PluginI(pluginID, "命令 %s 交由前端自处理（runtime=none，宿主未执行）", commandID)
		return json.RawMessage(`{"status":"ok","frontendOnly":true}`), nil
	case "goja":
		if inst.GetStatus() != "running" {
			return nil, fmt.Errorf("插件 %s 未在运行（状态: %s）", pluginID, inst.GetStatus())
		}
		result, err := inst.callGojaJS("handleExecute", map[string]interface{}{
			"command": commandID,
			"input":   input,
		}, 20*time.Second)
		if err != nil {
			logger.PluginE(pluginID, "handleExecute 执行失败 command=%s: %v", commandID, err)
			return nil, err
		}
		data, _ := json.Marshal(result)
		return data, nil
	default:
		return nil, fmt.Errorf("不支持的运行类型: %s", inst.Manifest.Backend.Runtime)
	}
}

// ListPlugins 列出所有插件（暴露给前端）
func (m *Manager) ListPlugins() []PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]PluginInfo, 0, len(m.plugins))
	for _, inst := range m.plugins {
		cmds := inst.Manifest.Commands
		if cmds == nil {
			cmds = []Command{}
		}
		result = append(result, PluginInfo{
			ID:              inst.Manifest.ID,
			Name:            inst.Manifest.Name,
			NameI18n:        inst.Manifest.NameI18n,
			Version:         inst.Manifest.Version,
			Description:     inst.Manifest.Description,
			DescriptionI18n: inst.Manifest.DescriptionI18n,
			Author:          inst.Manifest.Author,
			Category:        inst.Manifest.Category,
			Status:          inst.GetStatus(),
			HasFrontend:     inst.Manifest.Frontend.Enabled,
			Runtime:         inst.Manifest.Backend.Runtime,
			Commands:        cmds,
			Disabled:        inst.disabled.Load(),
		})
	}
	return result
}

// GetPlugin 获取插件实例
func (m *Manager) GetPlugin(id string) *PluginInstance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plugins[id]
}

// EnsureLoaded 确保插件已加载并运行中；未加载或已停止则按磁盘 manifest 惰性拉起。
// 用于「关窗即终止」后重新打开窗口 / 执行命令时按需复活，避免进程已停导致页面或命令失败。
func (m *Manager) EnsureLoaded(pluginID string) error {
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if ok && inst.GetStatus() == "running" && inst.Stdin != nil {
		return nil
	}
	// 已显式禁用的插件：禁止被任意调用方（命令面板 / AI·MCP / 残留窗口）经「惰性复活」
	// 静默拉起，否则内存 running 与 DB disabled 状态不一致、禁用形同虚设。
	if ok && inst.disabled.Load() {
		return fmt.Errorf("插件 %s 已禁用，拒绝加载", pluginID)
	}
	dir := filepath.Join(m.pluginsDir, pluginID)
	manifestPath := filepath.Join(dir, "plugin.json")
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	logger.PluginI(pluginID, "按需惰性复活插件")
	return m.LoadPlugin(*manifest, dir)
}

// ReloadPlugin 重新加载插件（启用时调用）
func (m *Manager) ReloadPlugin(id string) (*PluginManifest, error) {
	// id 直接参与路径拼接，必须过格式校验，防 "../" 穿越加载任意位置的 plugin.json
	if !pluginIDRe.MatchString(id) {
		return nil, fmt.Errorf("%w: 非法插件 ID: %q", ErrInvalidManifest, id)
	}
	dir := filepath.Join(m.pluginsDir, id)
	manifestPath := filepath.Join(dir, "plugin.json")

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	if err := m.LoadPlugin(*manifest, dir); err != nil {
		return nil, err
	}
	return manifest, nil
}

// UninstallPlugin 卸载插件（删除目录）
func (m *Manager) UninstallPlugin(id string) error {
	// id 直接参与路径拼接（Join + RemoveAll），必须过格式校验，
	// 防前端传入 "../x" 之类的 id 穿越删除插件目录之外的任意目录
	if !pluginIDRe.MatchString(id) {
		return fmt.Errorf("%w: 非法插件 ID: %q", ErrInvalidManifest, id)
	}
	// 停进程前先记下 PID：stopPlugin 的 Process.Kill 只杀主进程，
	// 兜底再用 taskkill /F /T 杀整棵进程树，防止子进程残留占用 exe 句柄
	var pid int
	m.mu.RLock()
	if inst, ok := m.plugins[id]; ok && inst.Cmd != nil && inst.Cmd.Process != nil {
		pid = inst.Cmd.Process.Pid
	}
	m.mu.RUnlock()
	// 先停进程并从内存移除：否则 Windows 上驻留的 native exe 会占用文件句柄，删除静默失败并留下孤儿进程
	m.UnloadPlugin(id)
	if pid > 0 {
		killProcessTree(pid)
	}

	dir := filepath.Join(m.pluginsDir, id)
	// Windows 上进程退出后文件句柄释放有延迟/残留（杀软扫描、WER、SearchIndexer 等），
	// 删除失败时按目录强杀残留进程并带退避重试；仍失败则给出明确操作指引
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			break
		} else {
			lastErr = err
		}
		if attempt < 4 {
			if runtime.GOOS == "windows" {
				killProcessesLockingDir(dir)
			}
			time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond) // 250ms, 500ms, 750ms, 1s
		}
	}
	if lastErr != nil {
		return fmt.Errorf("删除插件目录失败（可能仍有进程占用）: %w；请退出 QuickDock（托盘图标→退出）后重试卸载", lastErr)
	}
	// 联动清理同插件的备份/残留目录（<id>.bak.<ver> / <id>.broken-<ver>.bak）：
	// 它们是升级时留下的完整副本，含 plugin.json；不清理的话 DiscoverAndLoad 已过滤不加载，
	// 但残留目录会一直占磁盘，且手动放回/改名又会复活插件——卸载应同步清掉
	if entries, err2 := os.ReadDir(m.pluginsDir); err2 == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, id+".bak") || strings.HasPrefix(n, id+".broken") {
				if rerr := os.RemoveAll(filepath.Join(m.pluginsDir, n)); rerr == nil {
					logger.I("已清理插件 %s 的残留备份目录 %s", id, n)
				}
			}
		}
	}
	return nil
}

// GetFrontendPath 获取插件前端资源入口路径
func (m *Manager) GetFrontendPath(pluginID string) (string, error) {
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if !ok {
		return "", ErrPluginNotFound
	}
	if !inst.Manifest.Frontend.Enabled {
		return "", fmt.Errorf("插件 %s 未启用前端", pluginID)
	}
	return filepath.Join(inst.Dir, inst.Manifest.Frontend.Entry), nil
}

// ---- 孤儿进程清理 ----

// cleanupOrphans 启动时清理上一次残留的插件子进程
func (m *Manager) cleanupOrphans() {
	pidFile := m.pidFilePath

	// 如果 PID 文件不存在，说明上次正常退出
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		return
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		logger.W("读取 PID 文件失败: %v", err)
		os.Remove(pidFile)
		return
	}

	var pids pidFileData
	if err := json.Unmarshal(data, &pids); err != nil {
		logger.W("解析 PID 文件失败: %v", err)
		os.Remove(pidFile)
		return
	}

	// 清理所有记录的 PID
	for pluginID, pid := range pids.PIDs {
		if pid <= 0 {
			continue
		}
		if !processExists(pid) {
			continue
		}
		// 尝试终止进程
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// 二次确认：processExists 与 Kill 之间存在 PID 复用窗口（旧进程已退出、PID 被无关
		// 进程复用），仅再次校验仍存在以缩小误杀概率（仍非绝对，但显著降低风险）。
		if !processExists(pid) {
			continue
		}
		if err := proc.Kill(); err == nil {
			logger.W("清理孤儿进程 %q (PID %d)", pluginID, pid)
		}
		proc.Wait()
	}

	// 删除 PID 文件
	os.Remove(pidFile)
}

// processExists 验证 PID 对应的进程是否真实存在
// Windows 上 os.FindProcess 始终成功，需要额外验证避免误杀 PID 被重用的问题
func processExists(pid int) bool {
	// 先用 tasklist 验证进程是否存在（Windows）
	// 注意：主进程是 GUI 类型，直接 exec.Command 启动 tasklist（控制台程序）会弹 CMD 窗口
	cmd := sysutil.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH", "/FO", "CSV")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte(fmt.Sprintf(`"%d"`, pid)))
}

// safeWritePidFile 将指定插件快照的 PID 写入文件
// plugins: 已正确加锁保护的插件 map 快照
// 调用者必须确保传入的 map 在合适的锁保护下
func (m *Manager) safeWritePidFile(plugins map[string]*PluginInstance) {
	m.pidMu.Lock()
	defer m.pidMu.Unlock()

	pids := make(map[string]int)
	for id, inst := range plugins {
		if inst.GetStatus() == "running" && inst.Cmd != nil && inst.Cmd.Process != nil {
			pids[id] = inst.Cmd.Process.Pid
		}
	}

	data, err := json.Marshal(pidFileData{
		Version:   pidFileVersion,
		PIDs:      pids,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		logger.E("序列化 PID 文件数据失败: %v", err)
		return
	}
	os.WriteFile(m.pidFilePath, data, 0644)
}

// removePidFile 删除 PID 文件（正常退出时调用）
func (m *Manager) removePidFile() {
	m.pidMu.Lock()
	defer m.pidMu.Unlock()
	os.Remove(m.pidFilePath)
}

// ShutdownAll 停止所有插件并清理 PID 文件（主程序退出时调用）
func (m *Manager) ShutdownAll() {
	// 先停闲置降级循环：其 degradeIdlePlugins 会调 KillPlugin（持写锁），
	// 与下方 mu.Lock 并发将争用；停掉循环避免退出期冗余降级。
	m.stopIdleLoop()
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, inst := range m.plugins {
		logger.I("停止插件 %q", id)
		// 置 stopped：进程退出后 watchPlugin 读到 stopped=true 才不会把插件自动重启成孤儿进程
		inst.stopped.Store(true)
		// shutdown 通知在独立 goroutine 发送：SendNotification 内部有 2s 写入超时且会争用
		// inst.sendMu（与在途的 ExecuteCommand.Call 互斥），若在持有 m.mu 期间同步等待，
		// 会冻结整个插件管理器（含 ShutdownAll 自身）最长 2s×N；与 stopPlugin 的写法一致。
		if inst.Stdin != nil {
			go func() { _ = inst.SendNotification("shutdown", nil) }()
		}
		inst.SetStatus("stopped")
		inst.Close()
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.Cmd.Process.Kill()
			inst.Cmd.Wait()
		}
	}

	// 清理 PID 文件
	m.removePidFile()
}

// SetWindowVisibleChecker 注入窗口可见性判定（窗口管理器创建后调用一次）。
// 用于闲置降级时跳过「窗口正打开」的插件，避免打断正在使用的用户。
func (m *Manager) SetWindowVisibleChecker(fn func(pluginID string) bool) {
	m.isWindowVisible = fn
}

// MarkActive 标记插件最近被使用（窗口打开 / 命令执行），刷新其闲置计时。
// 实例不存在时静默 no-op（如尚未懒加载）。
func (m *Manager) MarkActive(pluginID string) {
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if ok {
		inst.MarkActive()
	}
}

// idleDegradeLoop 周期扫描并将闲置后端降级为就绪，直到 idleStopCh 关闭。
func (m *Manager) idleDegradeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.idleStopCh:
			return
		case <-ticker.C:
			m.degradeIdlePlugins()
		}
	}
}

// degradeIdlePlugins 将「窗口不可见、且最近无交互超过阈值」的 running 后端插件降级为就绪
//（degradePlugin → registered，下次使用经 EnsureLoaded 复活）。不碰可见窗口 / 已禁用 /
// 非 running / none 运行时（纯前端无后端进程）。
// 先快照 ID 再在锁外逐个降级：degradePlugin 内部会二次校验，避免与本次读锁之间发生实例替换
//（插件崩溃被 watchPlugin 重启 / 用户重新打开窗口）导致误杀正在使用的插件。
func (m *Manager) degradeIdlePlugins() {
	m.mu.RLock()
	idle := make([]string, 0)
	for id, inst := range m.plugins {
		if inst.GetStatus() != "running" {
			continue
		}
		if inst.disabled.Load() {
			continue
		}
		// none 运行时无后端进程，无需降级；窗口回收由窗口管理器独立负责，此处跳过避免
		// 纯前端插件在管理页无意义地闪成「就绪」。
		if inst.Manifest.Backend.Runtime == "none" {
			continue
		}
		// 窗口可见性检查：未注入检查器时保守跳过（宁可漏降也不误杀正在使用的插件）；
		// 注入后只有窗口正打开的插件才算使用中。
		if m.isWindowVisible == nil || m.isWindowVisible(id) {
			continue
		}
		if inst.idleFor() < m.idleDegradeAfter {
			continue
		}
		idle = append(idle, id)
	}
	m.mu.RUnlock()

	for _, id := range idle {
		if err := m.degradePlugin(id); err != nil {
			logger.W("[plugin-idle] 闲置降级 %s 失败: %v", id, err)
		} else {
			logger.I("[plugin-idle] 插件 %s 闲置 %s，已自动降级为就绪（释放后端进程）", id, m.idleDegradeAfter)
		}
	}
}

// degradePlugin 将指定插件降级为就绪（stopPlugin → registered），但先在写锁内二次校验
//「仍可降级」的全部条件，避免 degradeIdlePlugins 的快照与执行之间实例被替换（崩溃重启 /
// 用户重新打开窗口）而误杀刚复活 / 正被使用的插件。返回 ErrPluginNotFound 表示插件已不在
//（被卸载或实例已替换）；降级被二次校验否决时返回 nil（视为无需处理）。
// 与用户主动 KillPlugin 的区别：降级不扫描目录锁进程（闲置插件不锁目录），仅释放其后端进程。
func (m *Manager) degradePlugin(id string) error {
	if !pluginIDRe.MatchString(id) {
		return fmt.Errorf("%w: 非法插件 ID: %q", ErrInvalidManifest, id)
	}
	m.mu.Lock()
	inst, ok := m.plugins[id]
	if !ok {
		m.mu.Unlock()
		return ErrPluginNotFound
	}
	if inst.GetStatus() != "running" ||
		inst.disabled.Load() ||
		inst.Manifest.Backend.Runtime == "none" ||
		(m.isWindowVisible != nil && m.isWindowVisible(id)) ||
		inst.idleFor() < m.idleDegradeAfter {
		m.mu.Unlock()
		return nil
	}
	m.stopPlugin(inst, true)
	inst.SetStatus(statusRegistered)
	m.mu.Unlock()
	return nil
}

// stopIdleLoop 停止闲置降级后台循环（幂等），供 ShutdownAll 在退出前调用。
func (m *Manager) stopIdleLoop() {
	m.idleStopOnce.Do(func() { close(m.idleStopCh) })
}
