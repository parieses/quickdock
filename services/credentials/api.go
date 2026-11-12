package credentials

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quickdock/internal/credentials"
	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/internal/platform"
	"quickdock/services"
)

// 本地 HTTP API：浏览器扩展的唯一通信通道。
//
// 为什么不是 Chrome 官方的 Native Messaging：
//   - Native Messaging 会由浏览器拉起**第二个 quickdock.exe**，直接撞 Wails 的
//     单实例锁（main.go 的 SingleInstance），要在单实例判断之前插一个哑管道分支；
//   - 还要写注册表 + 一个 manifest JSON，换机器/换扩展 ID 都要重来。
//
// 本地 HTTP 的代价是「任何本地进程都能连」，所以补了三道门：
//  1. 只绑 127.0.0.1（不监听局域网）；
//  2. Origin 必须是扩展来源（网页 JS 无法伪造 Origin 头）；
//  3. Bearer 密钥（用户在密码库页点「开启」时签发，DPAPI 加密后落 app_state，
//     不过期；关闭开关即吊销）。
//
// 已知残留风险：同用户的本地进程若拿到 token 即可读密码。这与 DPAPI 的边界一致
// （同用户进程本就能解 app_state 里的密钥），不因本 API 而变差。

const (
	// defaultAPIPort 扩展默认连接的端口。被占用时依次向上试，实际端口通过 Status 告知前端。
	defaultAPIPort = 9240
	apiPortTries   = 10

	// settingExtToken 扩展长期密钥（DPAPI 加密后存 app_state）。
	//
	// 有值 = 浏览器集成处于「开启」状态：本地 API 会启动，扩展凭它鉴权。
	// 没有过期时间——用户说的是「开着就一直能用」，而不是一次性配对。
	settingExtToken = "cred_ext_token"
	// settingExtPort 上一次成功监听的端口，便于下次优先复用（也让扩展少一次探测）。
	settingExtPort = "cred_ext_port"
)

// localAPI 本地 HTTP 服务句柄。
type localAPI struct {
	srv  *http.Server
	port int
}

// StartAPI 启动本地 API（幂等：已启动则直接返回当前端口）。
//
// 由 services/lifecycle.go 在 DB 就绪后调用。失败不阻断应用启动——
// 密码库本身在应用内可用，只是浏览器扩展连不上，这种降级比「应用打不开」好得多。
func (s *CredentialsService) StartAPI() int {
	s.mu.Lock()
	if s.api != nil {
		port := s.api.port
		s.mu.Unlock()
		return port
	}
	s.mu.Unlock()

	var ln net.Listener
	var port int
	// 优先复用上次的端口：扩展在 options 页里存过端口，变了就要用户再改一次。
	preferred := s.preferredPort()
	for i := 0; i < apiPortTries; i++ {
		p := preferred + i
		if p > 65535 {
			break
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err == nil {
			ln, port = l, p
			break
		}
	}
	if ln == nil {
		logger.W("[credentials] 本地 API 启动失败（端口 %d~%d 均被占用）", preferred, preferred+apiPortTries-1)
		return 0
	}

	srv := &http.Server{
		Handler:           http.HandlerFunc(s.handleAPI),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	s.mu.Lock()
	s.api = &localAPI{srv: srv, port: port}
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.W("[credentials] 本地 API 异常退出: %v", err)
		}
	}()
	if s.App != nil && s.App.DB != nil {
		_ = s.App.DB.SetSetting(settingExtPort, strconv.Itoa(port))
	}
	logger.I("[credentials] 浏览器扩展 API 已监听 127.0.0.1:%d", port)
	return port
}

// StopAPI 关闭本地 API（应用退出时调用）。
func (s *CredentialsService) StopAPI() {
	s.mu.Lock()
	api := s.api
	s.api = nil
	s.mu.Unlock()
	if api != nil && api.srv != nil {
		_ = api.srv.Close()
		logger.I("[credentials] 浏览器扩展 API 已关闭")
	}
}

// apiPort 返回当前监听端口；0 表示未启动。
func (s *CredentialsService) apiPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.api == nil {
		return 0
	}
	return s.api.port
}

// preferredPort 上次监听的端口，没有记录时用默认值。
func (s *CredentialsService) preferredPort() int {
	if s.App == nil || s.App.DB == nil {
		return defaultAPIPort
	}
	raw, err := s.App.DB.GetSetting(settingExtPort)
	if err != nil {
		return defaultAPIPort
	}
	if p, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && p > 1024 && p <= 65535 {
		return p
	}
	return defaultAPIPort
}

// ---- 浏览器集成开关 ----

// EnableExtension 打开浏览器集成：起本地 API + 签发长期密钥（已有则沿用）。
//
// 与原「一次性配对码」的差别：密钥不过期、不消费，开关开着就一直可用。
// 换来的是「密钥等同于密码库的访问权」，所以它只在用户点开关时下发，
// 且随时可以在同一个开关上关掉（关 = 吊销密钥 + 停监听）。
func (s *CredentialsService) EnableExtension() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	port := s.StartAPI()
	if port == 0 {
		return failCode(CodeExtensionError, "本地服务启动失败，端口被占用")
	}
	key := s.readToken()
	if key == "" {
		k, err := s.issueToken()
		if err != nil {
			return services.Fail(err)
		}
		key = k
	}
	logger.I("[credentials] 浏览器集成已开启（127.0.0.1:%d）", port)
	return services.Ok(map[string]any{"enabled": true, "port": port, "key": key})
}

// DisableExtension 关闭浏览器集成：吊销密钥并停掉本地 API。
//
// 两步都要做：只清密钥会留下一个「谁来都能试 token」的监听端口，
// 只停端口会留下一个「重开就复活」的密钥。
func (s *CredentialsService) DisableExtension() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	// 先吊销再停监听：反过来的话，正在飞的请求还能用一个仍然有效的密钥通过校验。
	if err := s.App.DB.SetSetting(settingExtToken, ""); err != nil {
		return services.Fail(err)
	}
	s.StopAPI()
	logger.I("[credentials] 浏览器集成已关闭")
	return services.Ok(nil)
}

// RevealExtensionKey 取回当前密钥（页面「查看 / 复制」用）。
//
// 密钥在库里是 DPAPI 密文，只有本进程能解；这里显式暴露成前端可复制的字符串，
// 因为用户换浏览器配置、重装扩展时都要重新粘一次。
func (s *CredentialsService) RevealExtensionKey() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	key := s.readToken()
	if key == "" {
		return failCode(CodeExtensionError, "浏览器集成未开启")
	}
	return services.Ok(map[string]any{"key": key, "port": s.apiPort()})
}

// CopyExtensionKey 把当前密钥写进剪贴板。
//
// 由后端写而不是前端 navigator.clipboard：services.SetClipboardText 会登记
// 回环令牌，剪贴板捕获协程据此跳过，密钥不会进 clipboard_entries 历史表。
// 刻意不做 30 秒清除——用户常要在 Chrome 和 Edge 里各粘一次。
func (s *CredentialsService) CopyExtensionKey() *services.ApiResult {
	if r := services.CheckDB(s.App.DB); r != nil {
		return failCode(CodeDBUnavailable, "数据库未就绪")
	}
	key := s.readToken()
	if key == "" {
		return failCode(CodeExtensionError, "浏览器集成未开启")
	}
	if !services.SetClipboardText(key) {
		return services.FailMsg("写剪贴板失败")
	}
	return services.Ok(map[string]any{"port": s.apiPort()})
}

// extensionPaired 是否处于开启状态（有密钥）。
func (s *CredentialsService) extensionPaired() bool {
	if s.App == nil || s.App.DB == nil {
		return false
	}
	raw, err := s.App.DB.GetSetting(settingExtToken)
	return err == nil && strings.TrimSpace(raw) != ""
}

// StartAPIIfEnabled 只在「已开启」时才起监听（应用启动钩子用）。
//
// 关掉开关的用户不该有一个常驻的回环端口在那儿等请求——
// 端口不监听，本地其它进程连试探的机会都没有。
func (s *CredentialsService) StartAPIIfEnabled() int {
	if !s.extensionPaired() {
		return 0
	}
	return s.StartAPI()
}

// issueToken 生成并落库一个新的扩展 token（明文只在本次响应里出现）。
func (s *CredentialsService) issueToken() (string, error) {
	if s.App == nil || s.App.DB == nil {
		return "", fmt.Errorf("数据库未就绪")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 token 失败: %w", err)
	}
	token := fmt.Sprintf("%x", buf)
	enc, err := platform.EncryptSecret(token)
	if err != nil {
		return "", fmt.Errorf("token 加密失败: %w", err)
	}
	if err := s.App.DB.SetSetting(settingExtToken, enc); err != nil {
		return "", fmt.Errorf("token 落库失败: %w", err)
	}
	return token, nil
}

// readToken 取回已配对的 token 明文（仅用于比对）。
func (s *CredentialsService) readToken() string {
	if s.App == nil || s.App.DB == nil {
		return ""
	}
	raw, err := s.App.DB.GetSetting(settingExtToken)
	if err != nil || raw == "" {
		return ""
	}
	plain, err := platform.DecryptSecret(raw)
	if err != nil {
		logger.W("[credentials] 扩展 token 解密失败: %v", err)
		return ""
	}
	return plain
}

// ---- HTTP 处理 ----

// handleAPI 路由分发。
//
// 用最朴素的方法+路径分派而不是引入路由器：总共 6 个端点，
// 且路径前缀统一，多一个依赖换不来任何东西。
func (s *CredentialsService) handleAPI(w http.ResponseWriter, r *http.Request) {
	// 记一行请求日志：扩展侧出问题（密钥不对、应用没开）时，用户往往
	// 只看到「登录完了密码没进库」，日志是唯一能分清「没请求」和「请求被拒」的地方。
	// 只记方法、路径、状态码 —— 这里流过的可能是明文密码，绝不落 body。
	rec := &statusRecorder{ResponseWriter: w}
	w = rec
	defer func() {
		logger.I("[credentials] %s %s -> %d", r.Method, r.URL.Path, rec.code())
	}()

	origin := r.Header.Get("Origin")

	// Origin 白名单：只认扩展来源。
	//
	// 网页 JS 发起的跨站请求会被浏览器强制带上页面的 Origin（https://evil.com），
	// 无法伪造成 chrome-extension:// —— 这一步就把「网页偷密码」挡在门外。
	// 无 Origin 的请求（curl / 本地脚本）留给 token 兜底，便于调试。
	if origin != "" && !strings.HasPrefix(origin, "chrome-extension://") &&
		!strings.HasPrefix(origin, "moz-extension://") {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := r.URL.Path
	if !strings.HasPrefix(path, "/vault/") {
		writeAPI(w, http.StatusNotFound, map[string]any{"code": 1, "msg": "not found"})
		return
	}

	// /vault/status 不需要 token（扩展启动时先问「本机开没开」），其余一律要 token。
	switch path {
	case "/vault/status":
		s.apiStatus(w)
		return
	}

	if !s.checkBearer(r) {
		writeAPI(w, http.StatusUnauthorized, map[string]any{
			"code": 1,
			"msg":  "连接密钥无效，或 QuickDock 里已关闭「浏览器集成」",
		})
		return
	}

	switch path {
	case "/vault/match":
		s.apiMatch(w, r)
	case "/vault/reveal":
		s.apiReveal(w, r)
	case "/vault/save":
		s.apiSave(w, r)
	case "/vault/touch":
		s.apiTouch(w, r)
	case "/vault/list":
		s.apiList(w)
	default:
		writeAPI(w, http.StatusNotFound, map[string]any{"code": 1, "msg": "not found"})
	}
}

// checkBearer 校验 Authorization: Bearer <token>。
func (s *CredentialsService) checkBearer(r *http.Request) bool {
	want := s.readToken()
	if want == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	got = strings.TrimSpace(got)
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *CredentialsService) apiStatus(w http.ResponseWriter) {
	// 只暴露「能不能连」，不暴露条目数 —— 未开启的调用方不该拿到任何库信息。
	writeAPI(w, http.StatusOK, map[string]any{
		"ok":         true,
		"enabled":    s.extensionPaired(),
		"needsPair":  !s.extensionPaired(),
		"app":        "quickdock",
		"apiVersion": 1,
		"port":       s.apiPort(),
	})
}

func (s *CredentialsService) apiMatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	port, _ := strconv.Atoi(q.Get("port"))
	list, err := credentials.Match(credentials.MatchQuery{
		Kind:     normalizeKind(q.Get("kind")),
		Host:     q.Get("host"),
		Port:     port,
		Username: q.Get("username"),
	})
	if err != nil {
		writeAPI(w, http.StatusOK, map[string]any{"code": 1, "msg": err.Error()})
		return
	}
	// 只回扩展需要的字段：绝不把 notes/tags 这类用户私事带出去。
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, map[string]any{
			"id":        c.ID,
			"name":      c.Name,
			"username":  c.Username,
			"host":      c.Host,
			"hasSecret": c.HasSecret,
			"favorite":  c.Favorite,
			"useCount":  c.UseCount,
		})
	}
	writeAPI(w, http.StatusOK, map[string]any{"ok": true, "items": out})
}

func (s *CredentialsService) apiReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, map[string]any{"code": 1, "msg": "method not allowed"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeAPI(w, http.StatusBadRequest, map[string]any{"code": 1, "msg": "请求格式错误"})
		return
	}
	sec, err := credentials.Reveal(strings.TrimSpace(req.ID))
	if err != nil {
		writeAPI(w, http.StatusOK, map[string]any{"code": 1, "msg": err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": sec.Username,
		"secret":   sec.Secret,
	})
}

func (s *CredentialsService) apiSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, map[string]any{"code": 1, "msg": "method not allowed"})
		return
	}
	var in CredentialInput
	if err := json.NewDecoder(limitedBody(r)).Decode(&in); err != nil {
		writeAPI(w, http.StatusBadRequest, map[string]any{"code": 1, "msg": "请求格式错误"})
		return
	}
	// 扩展只会存网页登录：类型固定为 login，来源标记为 browser，
	// 这样用户能在密码库页里一眼分清「网页密码」和「我手动建的」。
	in.Kind = db.CredKindLogin
	if in.Source == "" {
		in.Source = "browser"
	}
	res := s.SaveCredential(in)
	writeAPI(w, http.StatusOK, map[string]any{"code": res.Code, "msg": res.Msg, "data": res.Data})
}

func (s *CredentialsService) apiTouch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, map[string]any{"code": 1, "msg": "method not allowed"})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(limitedBody(r)).Decode(&req); err != nil {
		writeAPI(w, http.StatusBadRequest, map[string]any{"code": 1, "msg": "请求格式错误"})
		return
	}
	if err := credentials.Touch(strings.TrimSpace(req.ID)); err != nil {
		writeAPI(w, http.StatusOK, map[string]any{"code": 1, "msg": err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *CredentialsService) apiList(w http.ResponseWriter) {
	list, err := credentials.List()
	if err != nil {
		writeAPI(w, http.StatusOK, map[string]any{"code": 1, "msg": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, map[string]any{
			"id": c.ID, "name": c.Name, "username": c.Username,
			"host": c.Host, "kind": c.Kind, "hasSecret": c.HasSecret,
		})
	}
	writeAPI(w, http.StatusOK, map[string]any{"ok": true, "items": out})
}

// limitedBody 返回限长后的请求体。
//
// 这是个本地回环服务，没必要接受大请求；顺手把「误发一个几十 MB 的 body」
// 变成明确的错误而不是一次内存峰值。
func limitedBody(r *http.Request) io.Reader {
	return http.MaxBytesReader(nil, r.Body, maxAPIBody)
}

// maxAPIBody 单次请求体上限：密码条目正常只有几百字节，64KB 足够宽松。
const maxAPIBody = 64 << 10

// statusRecorder 只为了拿到最终写出的状态码（handler 里分支太多，
// 逐个分支记日志容易漏），不改变任何响应行为。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) code() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func writeAPI(w http.ResponseWriter, status int, payload map[string]any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
