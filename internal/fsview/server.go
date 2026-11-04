package fsview

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// MediaServer 是图片 / PDF / 音视频的本地读取通道。
//
// 为什么不走 IPC 传字节：一张 8MB 的原图经 JSON 序列化会变成 11MB 的 base64 字符串，
// 而视频动辄几十上百 MB，传一次面板就卡死。这里给 WebView 一个 127.0.0.1 的地址，
// 由 Chromium 自己按需拉取 —— 顺带白拿了 Range 支持，视频可以拖动进度条。
//
// 为什么不复用 Wails 的 AssetServer 路由：开发模式下窗口直接连 vite devserver，
// AssetOptions.Handler 整个不参与，dev 里预览会 404。独立监听端口在两种模式下行为一致。
//
// 安全性：只绑 127.0.0.1（外网访问不到），路径带 16 字节随机令牌（同机其它进程扫不到），
// 只接受 GET/HEAD 且只服务普通文件。
type MediaServer struct {
	base string
	ln   net.Listener
}

// StartMediaServer 启动媒体服务。失败返回 error，由调用方决定退化成「只显示元数据」。
func StartMediaServer() (*MediaServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		ln.Close()
		return nil, err
	}
	prefix := "/" + hex.EncodeToString(token)
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/media", handleMedia)

	s := &MediaServer{
		base: "http://" + ln.Addr().String() + prefix,
		ln:   ln,
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// 不设 WriteTimeout：大视频的长时间拖动式请求会被它误杀。
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.W("[fsview] 媒体服务退出: %v", err)
		}
	}()
	logger.I("[fsview] 媒体服务已启动 %s", s.base)
	return s, nil
}

// URL 把绝对路径变成可拉取地址。
func (s *MediaServer) URL(absPath string) string {
	if s == nil {
		return ""
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(absPath))
	return s.base + "/media?p=" + enc
}

// ThumbURL 是 URL 的缩略版：服务端把图片缩到 width 边以内再返回。
//
// 预览区只有几百像素高，让 WebView 去解码一张 8000×6000 的原图（解码后约 190MB）
// 纯属浪费，快速划过几张就能让面板卡住。width 超过上限时服务端会自行夹紧。
func (s *MediaServer) ThumbURL(absPath string, width int) string {
	if s == nil {
		return ""
	}
	enc := base64.RawURLEncoding.EncodeToString([]byte(absPath))
	return s.base + "/media?p=" + enc + "&w=" + strconv.Itoa(width)
}

// Close 关闭监听。进程退出时调用即可，平时不需要。
func (s *MediaServer) Close() {
	if s != nil && s.ln != nil {
		s.ln.Close()
	}
}

func handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("p"))
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	abs := string(raw)
	// 只服务绝对路径的普通文件：挡掉目录、设备文件，也挡掉 ".." 之类的相对拼接。
	if !filepath.IsAbs(abs) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	// 文件随时可能被别的程序改写，缓存一份旧内容比不缓存更糟。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// ?w= 走缩略：只有图片会命中，其它类型 thumbnail 直接返回 false 落回原图路径。
	// 放在 Content-Type 推断之前，是因为缩略结果的类型跟原扩展名不一定一致
	// （PNG 仍编 PNG，JPEG 仍编 JPEG，但由编码器说了算更可靠）。
	if raw := r.URL.Query().Get("w"); raw != "" {
		if width, err := strconv.Atoi(raw); err == nil && width > 0 {
			if data, mimeType, ok := thumbnail(abs, info, width); ok {
				w.Header().Set("Content-Type", mimeType)
				http.ServeContent(w, r, info.Name(), info.ModTime(), bytes.NewReader(data))
				return
			}
		}
	}

	// 显式指定 PDF 类型：Windows 注册表里 .pdf 的 mime 映射并不可靠，
	// 给成 application/octet-stream 时 WebView2 会变成下载而不是内联显示。
	if strings.EqualFold(filepath.Ext(abs), ".pdf") {
		w.Header().Set("Content-Type", "application/pdf")
	} else if ct := mime.TypeByExtension(filepath.Ext(abs)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// ServeContent 自带 Range 支持与 If-Modified-Since 处理。
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
