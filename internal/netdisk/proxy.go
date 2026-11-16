package netdisk

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/logger"
	"quickdock/internal/platform"
)

// ProxyServer 把远端文件转成 WebView 能直接拉取的本地地址。
//
// 为什么必须转一道，而不是把网关地址直接给前端：
//   - <video>/<img> 只会发裸请求，带不上 Basic Auth；把密码塞进 URL 会进入
//     DOM、历史记录和截图，等于把网关密码印在界面上。
//   - Range 必须原样透传，否则视频拖不动进度条、大图加载慢（要靠断点分段）。
//   - 网关返回 401/500 时要给前端一句能看懂的话，而不是一个空白播放器。
//
// 安全边界与 fsview 的媒体服务保持一致：只绑 127.0.0.1（外网访问不到）、路径带
// 16 字节随机令牌（同机其它进程扫不到）、只接受 GET/HEAD、只读不写。
type ProxyServer struct {
	base   string
	ln     net.Listener
	client func() (*Client, error)
}

// textProbeBytes 文本/代码预览只取开头这么多。再大用户也不会在面板里逐行读完，
// 而整份拉取（几百 MB 的日志）会把网关带宽和本机内存一起吃掉。
const textProbeBytes = 512 << 10

// proxyStateFile 记录代理的端口与令牌，让「网盘 → 下载器」的地址跨重启不变。
//
// 为什么必须固定：交给 aria2 的 URL 形如 http://127.0.0.1:PORT/<token>/f?p=...，
// 端口和令牌都写在 URL 里。会话恢复时 aria2 拿到的还是上次那串地址，端口/令牌
// 一变就全是连接失败——用户看到的就是「重启后网盘任务全变成错误」。
// 令牌是 16 字节随机数，绑死在本机回环上，落盘文件 0600，与网关密码同等对待。
const proxyStateFile = "proxy.json"

type proxyState struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func proxyStatePath() string {
	return filepath.Join(platform.DefaultDataDir(), "netdisk", proxyStateFile)
}

func loadProxyState() proxyState {
	var st proxyState
	data, err := os.ReadFile(proxyStatePath())
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return proxyState{}
	}
	return st
}

func saveProxyState(st proxyState) {
	p := proxyStatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		logger.W("[netdisk] 保存预览代理配置失败（目录）: %v", err)
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		logger.W("[netdisk] 保存预览代理配置失败: %v", err)
	}
}

func validProxyToken(t string) bool {
	if len(t) != 32 {
		return false
	}
	_, err := hex.DecodeString(t)
	return err == nil
}

// listenProxy 优先复用上次的端口与令牌；端口被占（别的程序抢了）才换一套。
// 换端口时同步换令牌：旧地址彻底失效，避免两份地址同时能用、谁也不知道哪份是活的。
func listenProxy(prev proxyState) (net.Listener, proxyState, error) {
	if prev.Port > 0 && validProxyToken(prev.Token) {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(prev.Port))
		if ln, err := net.Listen("tcp", addr); err == nil {
			return ln, prev, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, proxyState{}, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		ln.Close()
		return nil, proxyState{}, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, proxyState{Port: port, Token: hex.EncodeToString(raw)}, nil
}

// StartProxy 启动代理服务。clientFn 延迟取客户端：网关可能还没启动，
// 那属于「取数时再报错」而不是「服务起不来」。
func StartProxy(clientFn func() (*Client, error)) (*ProxyServer, error) {
	ln, st, err := listenProxy(loadProxyState())
	if err != nil {
		return nil, err
	}
	saveProxyState(st)
	prefix := "/" + st.Token
	p := &ProxyServer{
		base:   "http://" + ln.Addr().String() + prefix,
		ln:     ln,
		client: clientFn,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/f", p.handle)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// 不设 WriteTimeout：一集 4GB 的片子要连续写十几分钟，超时会被误杀。
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.W("[netdisk] 预览代理退出: %v", err)
		}
	}()
	logger.I("[netdisk] 预览代理已启动 %s", p.base)
	return p, nil
}

// URL 把远端路径拼成可拉取地址。text=true 时只取开头一段（文本预览用）。
func (p *ProxyServer) URL(remotePath string, text bool) string {
	if p == nil {
		return ""
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(remotePath))
	u := p.base + "/f?p=" + enc
	if text {
		u += "&text=1"
	}
	return u
}

// Base 返回不带参数的根地址，供前端自行拼（Status 里下发给页面）。
func (p *ProxyServer) Base() string {
	if p == nil {
		return ""
	}
	return p.base
}

// Close 关闭监听。进程退出时调用即可。
func (p *ProxyServer) Close() {
	if p != nil && p.ln != nil {
		p.ln.Close()
	}
}

func (p *ProxyServer) handle(w http.ResponseWriter, r *http.Request) {
	// CORS 一律在最前面放行：媒体标签不需要它，但文本预览走的是 fetch（跨源），
	// 而且失败分支（http.Error）也要带上它，否则前端只能看到「CORS 错误」而不是
	// 我们辛苦提炼的网关原文。访问控制靠 URL 里的随机令牌，不靠 Origin。
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Range")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("p"))
	if err != nil || len(raw) == 0 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	remote := string(raw)
	wantText := r.URL.Query().Get("text") == "1"

	c, err := p.client()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.fullURL(remote), nil)
	if err != nil {
		http.Error(w, "无法构造请求: "+err.Error(), http.StatusBadGateway)
		return
	}
	c.auth(req)
	if rng := r.Header.Get("Range"); rng != "" {
		// 浏览器发起的拖动/分段请求，原样透传。
		req.Header.Set("Range", rng)
	} else if wantText {
		req.Header.Set("Range", "bytes=0-"+strconv.Itoa(textProbeBytes-1))
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		req.Header.Set("If-Modified-Since", ims)
	}

	resp, err := c.dlClient.Do(req)
	if err != nil {
		http.Error(w, "无法连接网盘网关: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := errSnippet(head)
		if msg == "" {
			msg = "网关返回 HTTP " + strconv.Itoa(resp.StatusCode)
		}
		http.Error(w, msg, resp.StatusCode)
		return
	}

	h := w.Header()
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if h.Get("Content-Type") == "" {
		// 网关有时不给类型（尤其是代理下载模式），按扩展名兜一层，
		// 否则 WebView 拿到 text/plain 就不会把 <video> 当媒体解码。
		if ct := mime.TypeByExtension(strings.ToLower(path.Ext(remote))); ct != "" {
			h.Set("Content-Type", ct)
		}
	}
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	body := io.Reader(resp.Body)
	if wantText && resp.StatusCode == http.StatusOK {
		// 网关无视了 Range（部分驱动的代理下载走整文件回源），这里再兜一层上限，
		// 免得「预览一个 500MB 的日志」变成真的把 500MB 拉进内存。
		body = io.LimitReader(resp.Body, textProbeBytes)
	}
	if _, err := io.Copy(w, body); err != nil {
		logger.W("[netdisk] 预览代理传输中断 path=%s: %v", remote, err)
	}
}
