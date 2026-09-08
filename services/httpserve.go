package services

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// HTTPServer 一个「目录 → 可访问静态服务」的配置项。
type HTTPServer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`            // 要对外提供服务的本地目录
	Port    int    `json:"port"`           // 监听端口（http://localhost:<port>）
	Running bool   `json:"running"`        // 是否应/正在运行（持久化 + 实际运行态由 Manager 维护）
}

type httpServerEntry struct {
	HTTPServer
	ln  net.Listener
	srv *http.Server
}

// HTTPServeManager 管理多个静态文件服务（创建/启停/删除/列表），持久化到 httpserve/servers.json。
type HTTPServeManager struct {
	mu      sync.Mutex
	file    string
	servers map[string]*httpServerEntry
}

// 包级单例：随 AppService 一起被引用，懒初始化到用户数据目录。
var httpServe = newHTTPServeManager()

func newHTTPServeManager() *HTTPServeManager {
	dir := filepath.Join(platform.DefaultDataDir(), "httpserve")
	_ = os.MkdirAll(dir, 0755)
	return newHTTPServeManagerAt(dir)
}

// newHTTPServeManagerAt 在指定目录创建 manager（测试可注入临时目录）。
func newHTTPServeManagerAt(dir string) *HTTPServeManager {
	_ = os.MkdirAll(dir, 0755)
	m := &HTTPServeManager{
		file:    filepath.Join(dir, "servers.json"),
		servers: map[string]*httpServerEntry{},
	}
	m.load()
	m.ResumeRunning()
	return m
}

// ResumeRunning 应用启动时调用：拉起所有之前标记为运行（running=true）且当前未运行的服务。
// 失败（如端口被外部占用）忽略，保留 running 标记以便下次启动再试。
func (h *HTTPServeManager) ResumeRunning() {
	h.mu.Lock()
	pending := make([]string, 0)
	for _, s := range h.servers {
		if s.Running && s.srv == nil {
			pending = append(pending, s.ID)
		}
	}
	h.mu.Unlock()
	for _, id := range pending {
		_ = h.Start(id)
	}
}

func (h *HTTPServeManager) load() {
	data, err := os.ReadFile(h.file)
	if err != nil {
		return
	}
	var list []HTTPServer
	if json.Unmarshal(data, &list) == nil {
		for _, s := range list {
			h.servers[s.ID] = &httpServerEntry{HTTPServer: s}
		}
	}
}

func (h *HTTPServeManager) persist() error {
	h.mu.Lock()
	list := make([]HTTPServer, 0, len(h.servers))
	for _, s := range h.servers {
		list = append(list, s.HTTPServer)
	}
	h.mu.Unlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(h.file, data, 0644)
}

// List 返回全部已配置的服务，Running 反映当前真实运行态（而非仅持久化标记）。
func (h *HTTPServeManager) List() []HTTPServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]HTTPServer, 0, len(h.servers))
	for _, s := range h.servers {
		item := s.HTTPServer
		item.Running = s.srv != nil
		out = append(out, item)
	}
	return out
}

// Create 新增一个服务（校验端口/名称唯一与目录存在性）。
// port<=0 表示**自动分配空闲端口**（默认：写死端口在多个服务之间必撞，且容易和本机已有服务冲突）；
// 需要固定端口时才显式传具体值。
func (h *HTTPServeManager) Create(name, dir string, port int) (*HTTPServer, error) {
	dir = filepath.Clean(dir)
	if dir == "" {
		return nil, fmt.Errorf("目录为空")
	}
	if port > 65535 {
		return nil, fmt.Errorf("端口无效（1-65535）")
	}
	h.mu.Lock()
	if port <= 0 {
		p, err := h.pickFreePortLocked()
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		port = p
	}
	for _, s := range h.servers {
		if s.Port == port {
			h.mu.Unlock()
			return nil, fmt.Errorf("端口 %d 已被占用", port)
		}
		if s.Name == name {
			h.mu.Unlock()
			return nil, fmt.Errorf("名称「%s」已存在", name)
		}
	}
	entry := &httpServerEntry{HTTPServer: HTTPServer{
		ID:   fmt.Sprintf("%d", time.Now().UnixNano()),
		Name: name,
		Dir:  dir,
		Port: port,
	}}
	h.servers[entry.ID] = entry
	h.mu.Unlock()
	// 不在持锁期间调用 persist（persist 内部会再次加锁，sync.Mutex 不可重入）
	if err := h.persist(); err != nil {
		h.mu.Lock()
		delete(h.servers, entry.ID)
		h.mu.Unlock()
		return nil, err
	}
	// 返回值的拷贝而非 &entry.HTTPServer：entry 已加入 h.servers，直接返回其字段指针会让调用方
	// 在锁外持有 map 内部对象，与后续 Stop/Start 的写并发（data race）。
	cp := entry.HTTPServer
	return &cp, nil
}

// pickFreePortLocked 在 20000-60000 之间随机挑一个「本管理器未占用 + 当前可监听」的端口。
// 随机而非递增扫描：多个服务同时创建时不会都挤在 20000 附近。
func (h *HTTPServeManager) pickFreePortLocked() (int, error) {
	for i := 0; i < 100; i++ {
		p := 20000 + rand.Intn(40001)
		taken := false
		for _, s := range h.servers {
			if s.Port == p {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		ln.Close()
		return p, nil
	}
	return 0, fmt.Errorf("未能自动分配空闲端口")
}

// Start 在指定端口启动静态文件服务（goroutine 中 Serve）。
// 关键字段（ln/srv/Running）的写入在持锁区间内完成，与 List/ResumeRunning 的持锁读构成一致视图，
// 消除此前「释放锁后再写 entry」导致的数据竞争（-race 必报）。
func (h *HTTPServeManager) Start(id string) error {
	h.mu.Lock()
	entry, ok := h.servers[id]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("服务不存在")
	}
	if entry.srv != nil {
		h.mu.Unlock()
		return fmt.Errorf("已在运行")
	}
	if _, err := os.Stat(entry.Dir); err != nil {
		h.mu.Unlock()
		return fmt.Errorf("目录不存在: %s", entry.Dir)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", entry.Port))
	if err != nil {
		h.mu.Unlock()
		return fmt.Errorf("监听端口 %d 失败: %w", entry.Port, err)
	}
	// 绑定 127.0.0.1 仅本机可访问；并用中间件禁用目录列举：请求指向目录且无 index.html 时返回 403，
	// 避免本机/局域网任意程序遍历用户选定目录的文件结构。
	fileServer := http.FileServer(http.Dir(entry.Dir))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			idx := filepath.Join(entry.Dir, filepath.Clean(r.URL.Path), "index.html")
			if _, statErr := os.Stat(idx); statErr != nil {
				http.Error(w, "directory listing disabled", http.StatusForbidden)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler}
	entry.ln = ln
	entry.srv = srv
	entry.HTTPServer.Running = true
	h.mu.Unlock()

	// 持久化需在释放锁后进行：persist() 内部会再次取锁，持锁调用会死锁。
	if err := h.persist(); err != nil {
		h.mu.Lock()
		ln.Close()
		entry.ln = nil
		entry.srv = nil
		entry.HTTPServer.Running = false
		h.mu.Unlock()
		logger.E("[httpserve] 服务 %s 启动后持久化失败，已回滚: %v", id, err)
		return err
	}
	go func() { _ = srv.Serve(ln) }()
	logger.I("[httpserve] 已启动服务 %s port=%d", id, entry.Port)
	return nil
}

// Stop 停止服务。
func (h *HTTPServeManager) Stop(id string) error {
	h.mu.Lock()
	entry, ok := h.servers[id]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("服务不存在")
	}
	if entry.srv == nil {
		h.mu.Unlock()
		return nil
	}
	srv := entry.srv
	ln := entry.ln
	entry.srv = nil
	entry.ln = nil
	entry.HTTPServer.Running = false
	h.mu.Unlock()

	// 关闭在锁外进行，避免阻塞其它调用；字段已在锁内清空，List 读到的始终是一致态。
	_ = srv.Close()
	if ln != nil {
		_ = ln.Close()
	}
	if err := h.persist(); err != nil {
		logger.E("[httpserve] 服务 %s 停止后持久化失败: %v", id, err)
	}
	logger.I("[httpserve] 已停止服务 %s", id)
	return nil
}

// Delete 停止并删除服务。
func (h *HTTPServeManager) Delete(id string) error {
	_ = h.Stop(id)
	h.mu.Lock()
	delete(h.servers, id)
	h.mu.Unlock()
	return h.persist()
}

// IsRunning 判断某服务是否正在运行。
func (h *HTTPServeManager) IsRunning(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.servers[id] != nil && h.servers[id].srv != nil
}
