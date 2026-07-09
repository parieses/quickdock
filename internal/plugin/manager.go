package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// pidFileVersion 用于兼容未来格式变更
const pidFileVersion = 1

// pidFileData PID 文件结构
type pidFileData struct {
	Version   int              `json:"version"`
	PIDs      map[string]int   `json:"pids"`      // pluginID → PID
	CreatedAt string           `json:"created_at"`
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

	healthCheckStopCh chan struct{}
	healthCheckWg     sync.WaitGroup
}

// NewManager 创建插件管理器
func NewManager(pluginsDir string) *Manager {
	m := &Manager{
		plugins:     make(map[string]*PluginInstance),
		pluginsDir:  pluginsDir,
		hostMethods: make(map[string]HostMethod),
		pidFilePath: filepath.Join(filepath.Dir(pluginsDir), "plugin_pids.json"),
	}

	m.registerDefaultHostMethods()

	// 启动时清理上一次残留的插件进程
	m.cleanupOrphans()

	// 启动后台健康检查
	m.healthCheckStopCh = make(chan struct{})
	m.startHealthCheck()

	return m
}

// RegisterHostMethod 注册 Host Method
func (m *Manager) RegisterHostMethod(name string, handler HostMethod) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hostMethods[name] = handler
}

// DiscoverAndLoad 扫描插件目录，加载所有已安装插件
func (m *Manager) DiscoverAndLoad() error {
	entries, err := os.ReadDir(m.pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在时不是错误
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(m.pluginsDir, entry.Name(), "plugin.json")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			continue
		}

		manifest, err := LoadManifest(manifestPath)
		if err != nil {
			fmt.Printf("QuickDock: 插件 %s 清单加载失败: %v\n", entry.Name(), err)
			continue
		}
		if err := m.LoadPlugin(*manifest, filepath.Join(m.pluginsDir, entry.Name())); err != nil {
			fmt.Printf("QuickDock: 插件 %s 启动失败: %v\n", manifest.ID, err)
		}
	}
	return nil
}

// LoadPlugin 启动一个插件的子进程（none runtime 不启动进程，纯前端）
func (m *Manager) LoadPlugin(manifest PluginManifest, dir string) error {
	// 先获取插件ID并检查是否需要停止旧实例
	m.mu.Lock()
	if inst, ok := m.plugins[manifest.ID]; ok {
		m.stopPlugin(inst)
	}
	m.mu.Unlock()

	// none runtime：纯前端插件，不启动子进程
	if manifest.Backend.Runtime == "none" {
		inst := NewPluginInstance(manifest, dir)
		inst.Status = "running"
		close(inst.readyCh) // 无需等待
		m.mu.Lock()
		m.plugins[manifest.ID] = inst
		m.mu.Unlock()
		return nil
	}

	// 根据 runtime 构建启动命令
	var cmd *exec.Cmd
	entryPath := filepath.Join(dir, manifest.Backend.Entry)

	switch manifest.Backend.Runtime {
	case "native":
		cmd = exec.Command(entryPath, manifest.Backend.Args...)
	case "node":
		cmd = exec.Command("node", entryPath)
	case "python":
		cmd = exec.Command("python", entryPath)
	case "powershell":
		cmd = exec.Command("powershell", "-File", entryPath)
	case "wasm":
		return fmt.Errorf("wasm runtime 尚在开发中，暂不支持")
	}

	cmd.Dir = dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建 stdin pipe 失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout pipe 失败: %w", err)
	}

	// stderr 通过 pipe 加插件 ID 前缀后输出，便于识别各插件日志
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建 stderr pipe 失败: %w", err)
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			fmt.Printf("[plugin %s] %s\n", manifest.ID, scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("插件进程启动失败: %w", err)
	}

	inst := NewPluginInstance(manifest, dir)
	inst.Cmd = cmd
	inst.Stdin = stdin
	inst.Stdout = stdout
	inst.Status = "starting"

	// 加入插件列表（只加写锁很短时间）
	m.mu.Lock()
	if existing, ok := m.plugins[manifest.ID]; ok && existing != inst {
		// 并发冲突：在上次解锁后另一个 goroutine 已加载了同一插件
		// 停止当前实例，保留已有实例
		m.mu.Unlock()
		inst.Close()
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.Cmd.Process.Kill()
		}
		return fmt.Errorf("插件 %s 并发加载冲突，另一个实例已优先注册", manifest.ID)
	}
	m.plugins[manifest.ID] = inst
	m.mu.Unlock()

	// 后台读取 stdout（启动后立即开始，先于 initialize 发送）
	go inst.readLoop(m)

	// 后台等待进程退出并监控崩溃自动重启
	go m.watchPlugin(inst)

	// 等待 readLoop 就绪 ← P0 修复
	<-inst.readyCh

	// 发送 initialize 请求
	_, err = inst.Call("initialize", map[string]interface{}{
		"hostVersion": "3.0.0",
		"pluginDir":   dir,
	}, 15*time.Second)
	if err != nil {
		m.mu.Lock()
		// 只删除自己的实例（避免并发场景下误删其他 goroutine 的实例）
		if current, ok := m.plugins[manifest.ID]; ok && current == inst {
			delete(m.plugins, manifest.ID)
		}
		m.stopPlugin(inst)
		m.mu.Unlock()
		return fmt.Errorf("插件初始化失败: %w", err)
	}

	inst.Status = "running"

	// 写入 PID 文件（取快照，避免在无锁状态下直接读 map）
	m.mu.RLock()
	pidSnapshot := make(map[string]*PluginInstance, len(m.plugins))
	for k, v := range m.plugins {
		pidSnapshot[k] = v
	}
	m.mu.RUnlock()
	m.safeWritePidFile(pidSnapshot)

	return nil
}

// UnloadPlugin 卸载插件（从内存移除）
func (m *Manager) UnloadPlugin(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.plugins[id]; ok {
		m.stopPlugin(inst)
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
	m.stopPlugin(inst)
	// 注意：stopPlugin 已将 inst.Status 设置为 "stopped"
	return nil
}

// stopPlugin 停止插件子进程
func (m *Manager) stopPlugin(inst *PluginInstance) {
	inst.stopped.Store(true)
	// 发送 shutdown（允许失败，可能已经崩溃）
	inst.SendNotification("shutdown", nil)

	inst.Status = "stopped"
	inst.Close()

	// 终止进程
	if inst.Cmd != nil && inst.Cmd.Process != nil {
		inst.Cmd.Process.Kill()
		inst.Cmd.Wait()
	}

	// 更新 PID 文件（调用者持有写锁，直接传 m.plugins 安全）
	m.safeWritePidFile(m.plugins)
}

// watchPlugin 等待插件退出，崩溃时自动重启（最多 3 次指数退避）
func (m *Manager) watchPlugin(inst *PluginInstance) {
	<-inst.doneCh

	if inst.stopped.Load() {
		return // 用户主动停止，不重启
	}

	fmt.Printf("QuickDock: 插件 %s 崩溃，尝试自动重启...\n", inst.Manifest.ID)
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(time.Duration(attempt*2) * time.Second) // 2s, 4s, 6s

		// 重新加载插件
		if err := m.LoadPlugin(inst.Manifest, inst.Dir); err != nil {
			fmt.Printf("QuickDock: 插件 %s 重启第 %d 次失败: %v\n", inst.Manifest.ID, attempt, err)
			continue
		}
		fmt.Printf("QuickDock: 插件 %s 自动重启成功\n", inst.Manifest.ID)
		return
	}
	fmt.Printf("QuickDock: 插件 %s 已达最大重启次数，放弃\n", inst.Manifest.ID)
}

// startHealthCheck 启动后台健康检查协程（每 30 秒 ping 所有运行中插件）
func (m *Manager) startHealthCheck() {
	m.healthCheckWg.Add(1)
	go func() {
		defer m.healthCheckWg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.healthCheckStopCh:
				return
			case <-ticker.C:
				m.pingAll()
			}
		}
	}()
}

// stopHealthCheck 停止后台健康检查
func (m *Manager) stopHealthCheck() {
	close(m.healthCheckStopCh)
	m.healthCheckWg.Wait()
}

// pingAll 对所有运行中的插件发送 ping
func (m *Manager) pingAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.plugins))
	for id, inst := range m.plugins {
		if inst.Status == "running" && inst.Manifest.Backend.Runtime != "none" {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range ids {
		m.pingOne(id)
	}
}

// pingOne 对单个插件发送 ping，超过 3 次标记为 unresponsive
func (m *Manager) pingOne(pluginID string) {
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if !ok || inst.Status != "running" {
		return
	}

	_, err := inst.Call("host.ping", nil, 5*time.Second)
	if err == nil {
		// ping 成功，重置计数器
		m.mu.Lock()
		inst.MissedPings = 0
		if inst.Status == "unresponsive" {
			inst.Status = "running"
			fmt.Printf("QuickDock: 插件 %s 恢复响应\n", pluginID)
		}
		m.mu.Unlock()
		return
	}

	// ping 失败，递增计数器
	m.mu.Lock()
	inst.MissedPings++
	if inst.MissedPings >= 3 && inst.Status == "running" {
		inst.Status = "unresponsive"
		inst.UnresponsiveAt = time.Now()
		fmt.Printf("QuickDock: 插件 %s 连续 %d 次无响应，标记为 unresponsive\n", pluginID, inst.MissedPings)
	}
	m.mu.Unlock()
}

// PluginsDir 返回插件安装目录
func (m *Manager) PluginsDir() string {
	return m.pluginsDir
}

// ExecuteCommand 执行插件命令（供 Wails 前端调用）
func (m *Manager) ExecuteCommand(pluginID, commandID string, input map[string]interface{}) (json.RawMessage, error) {
	m.mu.RLock()
	inst, ok := m.plugins[pluginID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrPluginNotFound
	}

	// none runtime：纯前端插件，无后端 RPC
	if inst.Manifest.Backend.Runtime == "none" {
		return json.RawMessage(`{"status":"ok","frontendOnly":true}`), nil
	}

	return inst.Call("plugin.execute", map[string]interface{}{
		"command": commandID,
		"input":   input,
	}, 10*time.Second)
}

// ListPlugins 列出所有插件（暴露给前端）
func (m *Manager) ListPlugins() []PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]PluginInfo, 0, len(m.plugins))
	for _, inst := range m.plugins {
		result = append(result, PluginInfo{
			ID:          inst.Manifest.ID,
			Name:        inst.Manifest.Name,
			Version:     inst.Manifest.Version,
			Description: inst.Manifest.Description,
			Author:      inst.Manifest.Author,
			Status:      inst.Status,
			HasFrontend: inst.Manifest.Frontend.Enabled,
			Commands:    inst.Manifest.Commands,
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

// ReloadPlugin 重新加载插件（启用时调用）
func (m *Manager) ReloadPlugin(id string) (*PluginManifest, error) {
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
	dir := filepath.Join(m.pluginsDir, id)
	return os.RemoveAll(dir)
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
		fmt.Printf("QuickDock: 读取 PID 文件失败: %v\n", err)
		os.Remove(pidFile)
		return
	}

	var pids pidFileData
	if err := json.Unmarshal(data, &pids); err != nil {
		fmt.Printf("QuickDock: 解析 PID 文件失败: %v\n", err)
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
		if err := proc.Kill(); err == nil {
			fmt.Printf("QuickDock: 清理孤儿进程 %q (PID %d)\n", pluginID, pid)
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
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH", "/FO", "CSV").Output()
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
		if inst.Status == "running" && inst.Cmd != nil && inst.Cmd.Process != nil {
			pids[id] = inst.Cmd.Process.Pid
		}
	}

	data, _ := json.Marshal(pidFileData{
		Version:   pidFileVersion,
		PIDs:      pids,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
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
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, inst := range m.plugins {
		fmt.Printf("QuickDock: 停止插件 %q\n", id)
		inst.SendNotification("shutdown", nil)
		inst.Status = "stopped"
		inst.Close()
		if inst.Cmd != nil && inst.Cmd.Process != nil {
			inst.Cmd.Process.Kill()
			inst.Cmd.Wait()
		}
	}

	// 清理 PID 文件
	m.removePidFile()
}
