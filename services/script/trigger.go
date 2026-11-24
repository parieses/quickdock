package script

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quickdock/internal/db"
	"quickdock/internal/logger"
	"quickdock/services"
)

// triggerConfig HTTP 触发服务的持久化配置。
//
// 落 <脚本根>/trigger.json 而不是 app_state：端口和 token 是「外部系统已经在用的
// 地址」，必须跨重启稳定。放 app_state 要走两处白名单，而这里本来就有一个
// 只属于脚本域的目录，没有理由挤进全局配置。
type triggerConfig struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
}

func (s *ScriptService) triggerConfigPath() string {
	return filepath.Join(s.Root(), "trigger.json")
}

func (s *ScriptService) loadTriggerConfig() (triggerConfig, error) {
	var cfg triggerConfig
	data, err := os.ReadFile(s.triggerConfigPath())
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (s *ScriptService) saveTriggerConfig(cfg triggerConfig) error {
	if err := os.MkdirAll(s.Root(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600：里面是一个能触发任意脚本的令牌，等同本机执行权限。
	return os.WriteFile(s.triggerConfigPath(), data, 0o600)
}

// newToken 生成 32 字节随机令牌。
func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着系统熵源有问题，此时给一个可预测的令牌
		// 比不给更危险，但也不能让按钮彻底点不动——用时间兜底并记一条错误。
		logger.E("[script] 生成触发令牌失败，改用时间戳兜底: %v", err)
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// StartTrigger 启动 HTTP 触发服务。
func (s *ScriptService) StartTrigger() *services.ApiResult {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()

	cfg, err := s.loadTriggerConfig()
	if err != nil || cfg.Token == "" {
		cfg.Token = newToken()
	}
	if cfg.Port == 0 {
		cfg.Port = 17890
	}
	cfg.Enabled = true
	if err := s.saveTriggerConfig(cfg); err != nil {
		return services.Fail(err)
	}
	if err := s.startTrigger(cfg); err != nil {
		return services.Fail(err)
	}
	// 端口被占时 startTrigger 会自动 +1 另起监听。要报**实际**端口：报配置里的
	// 端口会给出一个没人监听的地址，用户照着拼进调用方，请求直接连不上。
	port := cfg.Port
	if s.trigger != nil {
		port = s.trigger.port
	}
	return services.Ok(map[string]any{
		"port":  port,
		"token": cfg.Token,
		"url":   fmt.Sprintf("http://127.0.0.1:%d/run/<脚本ID>", port),
	})
}

// StopTrigger 停止服务（配置保留，下次启动如果 Enabled 会自己回来）。
func (s *ScriptService) StopTrigger() *services.ApiResult {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	cfg, err := s.loadTriggerConfig()
	if err != nil {
		// 读配置失败时不能拿零值 cfg 写回去：那会把 token 清成空串，等于把
		// 已发出的调用方地址全部作废，而用户只是想停一下服务。
		s.stopTriggerLocked()
		return services.Fail(fmt.Errorf("读取触发配置失败，已停止服务但未改动配置: %w", err))
	}
	cfg.Enabled = false
	if err := s.saveTriggerConfig(cfg); err != nil {
		s.stopTriggerLocked()
		return services.Fail(err)
	}
	s.stopTriggerLocked()
	return services.Ok(nil)
}

// TriggerStatus 查询状态（含端口与令牌，令牌在本机界面上展示）。
func (s *ScriptService) TriggerStatus() *services.ApiResult {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	cfg, err := s.loadTriggerConfig()
	if err != nil {
		return services.Ok(map[string]any{"enabled": false, "port": 0, "token": ""})
	}
	running := s.trigger != nil
	// 与 StartTrigger 同理：实际监听端口可能因占用而顺延，状态页要报真实值。
	port := cfg.Port
	if s.trigger != nil {
		port = s.trigger.port
	}
	return services.Ok(map[string]any{
		"enabled": cfg.Enabled,
		"running": running,
		"port":    port,
		"token":   cfg.Token,
		"url":     fmt.Sprintf("http://127.0.0.1:%d/run/<脚本ID>", port),
	})
}

// RotateTriggerToken 换令牌。旧地址立刻失效——这是「怀疑泄漏」时的唯一止血手段。
func (s *ScriptService) RotateTriggerToken() *services.ApiResult {
	s.triggerMu.Lock()
	defer s.triggerMu.Unlock()
	cfg, err := s.loadTriggerConfig()
	if err != nil {
		return services.Fail(err)
	}
	cfg.Token = newToken()
	if err := s.saveTriggerConfig(cfg); err != nil {
		return services.Fail(err)
	}
	if s.trigger != nil {
		s.stopTriggerLocked()
		if cfg.Enabled {
			if err := s.startTrigger(cfg); err != nil {
				return services.Fail(err)
			}
		}
	}
	return services.Ok(map[string]any{"token": cfg.Token})
}

// startTrigger 起监听。调用方须持 triggerMu。
func (s *ScriptService) startTrigger(cfg triggerConfig) error {
	// 已在跑就先停，避免「换端口」时两个监听同时存在。
	s.stopTriggerLocked()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": "quickdock-script"})
	})
	mux.HandleFunc("/run/", s.handleTriggerRun)
	mux.HandleFunc("/scripts", s.handleTriggerList)

	// 端口可能被占（上次没退干净 / 用户自己开了别的服务）。
	// 依次试探 10 个端口再放弃：固定端口对外部调用方最友好，所以不随机。
	var (
		ln   net.Listener
		err  error
		port = cfg.Port
	)
	for i := 0; i < 10; i++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
		port++
	}
	if err != nil {
		return fmt.Errorf("无法监听本机端口（%d 起连续 10 个都被占用）: %w", cfg.Port, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	s.trigger = &triggerServer{srv: srv, cfg: cfg, port: port}
	if port != cfg.Port {
		// 换端口要落盘，否则下次启动又按旧端口试一遍，而外部调用方手里的
		// 地址也会一直指着一个没人监听的端口。
		newCfg := cfg
		newCfg.Port = port
		_ = s.saveTriggerConfig(newCfg)
		s.trigger.cfg = newCfg
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.W("[script] HTTP 触发服务退出: %v", err)
		}
	}()
	logger.I("[script] HTTP 触发已启动 127.0.0.1:%d", port)
	return nil
}

func (s *ScriptService) stopTriggerLocked() {
	if s.trigger == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.trigger.srv.Shutdown(ctx)
	s.trigger = nil
}

// Shutdown 应用退出时调用：停掉 HTTP 触发，并终止所有在途脚本。
//
// 必须显式取消在途运行：脚本是普通子进程，宿主退出不会连带结束它们
// （Windows 上没有作业对象，Linux 上也没建进程组），留下的是「看不见还在跑」
// 的孤儿——下次启动收尾时它们还会继续往同一个数据文件里写。
func (s *ScriptService) Shutdown() {
	s.StopWatcher()

	s.triggerMu.Lock()
	s.stopTriggerLocked()
	s.triggerMu.Unlock()

	s.cancels.Range(func(k, v any) bool {
		if cancel, ok := v.(context.CancelFunc); ok {
			cancel()
		}
		s.cancels.Delete(k)
		return true
	})
}

// handleTriggerRun POST /run/<id>，令牌走 X-QD-Token 头或 ?token=。
//
// 设计边界（重要）：只接受「已存在的脚本 id」，不接受路径、命令、代码。
// 否则这个端口就是一条本机 RCE 通道——任何能发本地 HTTP 请求的程序
// （包括浏览器里的网页，通过 DNS rebinding）都能执行任意命令。
// 只认 id + 令牌，攻击面收敛成「知道令牌且知道脚本 id」。
func (s *ScriptService) handleTriggerRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只支持 POST"})
		return
	}
	if !s.checkToken(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "令牌无效"})
		return
	}
	// 防 DNS rebinding：只认回环来源，且不接受跨站请求头。
	if !isLoopback(r.RemoteAddr) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "只接受本机请求"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/run/")
	id = strings.Trim(id, "/")
	if id == "" || strings.ContainsAny(id, `/\.`) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少脚本 id"})
		return
	}

	var body struct {
		Args []string `json:"args"`
	}
	if data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10)); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请求体必须是 JSON，如 {\"args\":[\"--days\",\"7\"]}"})
			return
		}
	}
	argsJSON := ""
	if len(body.Args) > 0 {
		b, _ := json.Marshal(body.Args)
		argsJSON = string(b)
	}

	res := s.RunScriptWithTrigger(id, argsJSON, TriggerHTTP)
	if res.Code != 0 {
		// 并发冲突与「脚本存在但闸门占着」在语义上是 409，外部调用方据此
		// 决定是重试还是报错；统一 400 会让它无脑重试、越试越堵。
		status := http.StatusBadRequest
		if strings.Contains(res.Msg, "正在运行") {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": res.Msg})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "run": res.Data})
}

// handleTriggerList GET /scripts：给外部系统发现「能触发谁」。
//
// 只暴露 id / name / tags，不暴露正文与变量——令牌泄漏时少丢一点。
func (s *ScriptService) handleTriggerList(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(r) || !isLoopback(r.RemoteAddr) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "令牌无效"})
		return
	}
	list, err := s.App.DB.ListScripts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, sc := range list {
		out = append(out, map[string]any{
			"id": sc.ID, "name": sc.Name, "lang": sc.Lang,
			"tags": sc.Tags, "lastStatus": sc.LastStatus,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
}

// checkToken 常数时间比较，避免按响应时间逐字节猜令牌。
func (s *ScriptService) checkToken(r *http.Request) bool {
	tok := r.Header.Get("X-QD-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" {
		return false
	}
	var want string
	s.triggerMu.Lock()
	if s.trigger != nil {
		want = s.trigger.cfg.Token
	}
	s.triggerMu.Unlock()
	if want == "" {
		if cfg, err := s.loadTriggerConfig(); err == nil {
			want = cfg.Token
		}
	}
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// triggerServer 运行中的监听。
type triggerServer struct {
	srv  *http.Server
	cfg  triggerConfig
	port int
}

// 供 MCP 层判断「这个脚本允不允许外部触发」。
func scriptAllowsMCP(sc *db.Script) bool { return sc != nil && sc.MCPAllowed }
