// Package webdavsrv 提供进程内 WebDAV 服务端。
//
// 注意与 internal/webdav 区分：那个包是「备份同步」用的 WebDAV 客户端，本包只做服务端，
// 供环境管理页把本地某个目录以 WebDAV 协议共享出去。
//
// 生命周期与 internal/mcp 一致：宿主进程内单例，由环境管理页启停，无独立二进制、无版本。
package webdavsrv

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"quickdock/internal/logger"
)

// DefaultPort WebDAV 默认监听端口。避开项目已占用的 minio(9000/9001)、mailpit(8025)、mcp(9230)。
const DefaultPort = 9080

// Config 启动所需的全部参数（快照语义：改动需重启服务才生效，与其它运行时一致）。
type Config struct {
	Addr     string // 监听地址，空=127.0.0.1
	Port     int    // 监听端口
	Root     string // 共享根目录
	Username string // Basic Auth 用户名
	Password string // Basic Auth 密码
	ReadOnly bool   // 只读模式：拒绝一切写方法
}

// Server WebDAV 服务端（进程内单例）。
type Server struct {
	mu  sync.Mutex
	srv *http.Server
	ln  net.Listener
}

// Default 进程内唯一实例。
var Default = &Server{}

// Start 监听并开始服务，返回实际监听地址（host:port）。
func (s *Server) Start(cfg Config) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return "", errors.New("WebDAV 服务已在运行")
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return "", errors.New("共享根目录不能为空")
	}
	// WebDAV 会把本机目录暴露到网络，账号密码是底线，不允许留空。
	if cfg.Username == "" || cfg.Password == "" {
		return "", errors.New("必须设置用户名与密码")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return "", fmt.Errorf("共享根目录无效: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建共享根目录失败: %w", err)
	}
	host := strings.TrimSpace(cfg.Addr)
	if host == "" {
		host = "127.0.0.1"
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = DefaultPort
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return "", err
	}
	dav := &webdav.Handler{
		Prefix:     "/",
		FileSystem: webdav.Dir(root),
		// LockSystem 必须提供：缺省下 LOCK/UNLOCK 返回 501，Windows 资源管理器与
		// macOS Finder 都会因此挂载失败。
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				logger.W("[webdav] %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}
	cfg.Root = root
	s.ln = ln
	s.srv = &http.Server{
		Handler:           guard(cfg, dav),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	addr := ln.Addr().String()
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.E("[webdav] 监听异常: %v", err)
		}
	}()
	logger.I("[webdav] 服务已启动 url=http://%s/ root=%s readonly=%v", addr, root, cfg.ReadOnly)
	return addr, nil
}

// Stop 关闭监听。未运行时返回 nil。
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	s.srv, s.ln = nil, nil
	logger.I("[webdav] 服务已停止")
	return err
}

// Running 服务是否在运行。
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Addr 实际监听地址（host:port），未运行返回空串。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Port 实际监听端口，未运行返回 0。
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return 0
	}
	if tcp, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// guard 在 webdav.Handler 外层依次做：Basic Auth → 路径规范化 → Depth 限制 → 目录索引页 → 只读拦截。
func guard(cfg Config, dav *webdav.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r, cfg.Username, cfg.Password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="QuickDock WebDAV"`)
			http.Error(w, "需要认证", http.StatusUnauthorized)
			return
		}
		// 反斜杠是 Windows 的路径分隔符，但 path.Clean 不认它，放行等于绕开规范化逃出根目录。
		if strings.ContainsRune(r.URL.Path, '\\') {
			http.Error(w, "非法路径", http.StatusBadRequest)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		if !withinRoot(cfg.Root, clean) {
			http.Error(w, "非法路径", http.StatusForbidden)
			return
		}
		r.URL.Path = clean
		// 客户端常发 Depth: infinity，大目录会被递归遍历到超时；统一限制为单层。
		if r.Method == "PROPFIND" && strings.EqualFold(strings.TrimSpace(r.Header.Get("Depth")), "infinity") {
			r.Header.Set("Depth", "1")
		}
		// x/net/webdav 只实现协议本身，不提供 HTML 目录页；浏览器直接访问会报错，
		// 这里补一个只读索引页，避免用户第一反应是「打不开」。
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && isDir(localPath(cfg.Root, clean)) {
			serveDirIndex(w, cfg.Root, clean)
			return
		}
		if cfg.ReadOnly && writeMethod(r.Method) {
			http.Error(w, "服务运行在只读模式，禁止修改", http.StatusForbidden)
			return
		}
		dav.ServeHTTP(w, r)
	})
}

// authOK 常数时间比较，避免用户名/密码被时序探测。
func authOK(r *http.Request, user, pass string) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
}

// writeMethod 判定会改动数据的请求方法（只读模式下拒绝）。
func writeMethod(m string) bool {
	switch m {
	case http.MethodPut, http.MethodDelete, http.MethodPost,
		"MKCOL", "MOVE", "COPY", "PROPPATCH", "LOCK", "UNLOCK":
		return true
	}
	return false
}

// localPath 把已规范化的 URL 路径映射到本地绝对路径。
// filepath.Join 只做拼接与 Clean，不会因段以分隔符开头而跳到盘符根，故不会逃逸。
func localPath(root, cleanURLPath string) string {
	return filepath.Join(root, filepath.FromSlash(cleanURLPath))
}

// withinRoot 纵深防御：Clean 后仍再算一次相对路径，确认没跑到根目录之外。
func withinRoot(root, cleanURLPath string) bool {
	rel, err := filepath.Rel(root, localPath(root, cleanURLPath))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// serveDirIndex 渲染极简目录索引页（仅 GET/HEAD，且本服务的唯一 HTML 输出）。
func serveDirIndex(w http.ResponseWriter, root, urlPath string) {
	entries, err := os.ReadDir(localPath(root, urlPath))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	base := strings.TrimSuffix(urlPath, "/")
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><title>QuickDock WebDAV</title>")
	b.WriteString("<style>body{font:14px/1.7 system-ui,sans-serif;margin:24px;color:#222}")
	b.WriteString("a{color:#06c;text-decoration:none}a:hover{text-decoration:underline}")
	b.WriteString("li{margin:2px 0}.s{color:#888;margin-left:8px;font-size:12px}</style>")
	b.WriteString("<h3>")
	b.WriteString(html.EscapeString(urlPath))
	b.WriteString("</h3><ul>")
	if urlPath != "/" {
		b.WriteString("<li><a href=\"")
		b.WriteString(html.EscapeString(parentURL(base)))
		b.WriteString("\">..</a></li>")
	}
	for _, e := range entries {
		name := e.Name()
		href := base + "/" + url.QueryEscape(name)
		display := name
		size := ""
		if e.IsDir() {
			href += "/"
			display += "/"
		} else if fi, ferr := e.Info(); ferr == nil {
			size = humanSize(fi.Size())
		}
		b.WriteString("<li><a href=\"")
		b.WriteString(html.EscapeString(href))
		b.WriteString("\">")
		b.WriteString(html.EscapeString(display))
		b.WriteString("</a>")
		if size != "" {
			b.WriteString("<span class=\"s\">")
			b.WriteString(size)
			b.WriteString("</span>")
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

func parentURL(base string) string {
	if i := strings.LastIndex(base, "/"); i > 0 {
		return base[:i] + "/"
	}
	return "/"
}

func humanSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return strconv.FormatInt(n, 10) + " B"
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
