package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// ---- 权限校验 ----

// checkPermission 检查插件是否有权调用指定方法
func (m *Manager) checkPermission(pluginID string, method string) error {
	inst := m.GetPlugin(pluginID)
	if inst == nil {
		return ErrPluginNotFound
	}

	// log.* / host.ping / db.* 无需额外权限（db.* 已按 plugin_id 强隔离）
	// clipboard / network / filesystem 三类能力由 plugin.json 的 permissions 显式授权，
	// 实现体由 services.RegisterPluginHostMethods 在启动时注入。
	switch {
	case method == "host.clipboard.read" || method == "host.clipboard.write":
		if !inst.Manifest.Permissions.Clipboard {
			return fmt.Errorf("%w: 插件 %q 没有 clipboard 权限", ErrPermissionDenied, pluginID)
		}
	case method == "http.get" || method == "http.post":
		if !inst.Manifest.Permissions.Network {
			return fmt.Errorf("%w: 插件 %q 没有 network 权限", ErrPermissionDenied, pluginID)
		}
	case strings.HasPrefix(method, "host.dialog."):
		if !inst.Manifest.Permissions.Filesystem {
			return fmt.Errorf("%w: 插件 %q 没有 filesystem 权限", ErrPermissionDenied, pluginID)
		}
	}

	return nil
}

// ---- Host Method 注册 ----

// registerDefaultHostMethods 注册不依赖 services 层的基础 Host Method。
// http.* / db.* / host.clipboard.* / host.dialog.* / host.notify 的真实实现位于
// services/plugin_host.go，由 ServiceStartup 通过 InjectHostMethod 覆盖注入；
// 这里保留 host.notify 的日志版本作为注入前的兜底。
func (m *Manager) registerDefaultHostMethods() {
	// 日志（路由到插件专属日志文件，与宿主主日志分离）
	m.RegisterHostMethod("log.info", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.Message != "" {
			logger.PluginI(pluginID, "%s", p.Message)
		}
		return nil, nil
	})

	m.RegisterHostMethod("log.error", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.Message != "" {
			logger.PluginE(pluginID, "%s", p.Message)
		}
		return nil, nil
	})

	m.RegisterHostMethod("log.warn", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.Message != "" {
			logger.PluginW(pluginID, "%s", p.Message)
		}
		return nil, nil
	})

	// 通知（实际通知由 services 层注册覆盖；注入前兜底记到插件日志，不丢）
	m.RegisterHostMethod("host.notify", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil {
			logger.PluginI(pluginID, "notify: %s - %s", p.Title, p.Message)
		}
		return nil, nil
	})

	// 健康检查 ping
	m.RegisterHostMethod("host.ping", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"pong": true, "time": time.Now().Unix()}, nil
	})

	m.RegisterHostMethod("ui.show", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"status": "ok"}, nil
	})
	m.RegisterHostMethod("ui.hide", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"status": "ok"}, nil
	})
}

// ---- handleCallback 的安全版本 ----

// handleCallback 处理插件发起的回调请求/通知（带权限校验）
// 由 readLoop goroutine 调用；rawLine 为该行原始 JSON，用于区分"无 id 的通知"
// 与"id 恰好为 0 的请求"（JSON-RPC 规范中 id=0 是合法请求 ID）
func (m *Manager) handleCallback(inst *PluginInstance, req *RPCRequest, rawLine []byte) {
	var probe struct {
		ID *int64 `json:"id"`
	}
	if json.Unmarshal(rawLine, &probe) == nil && probe.ID == nil {
		return // 通知（无 ID 字段）不需要响应
	}

	// 权限检查
	if err := m.checkPermission(inst.Manifest.ID, req.Method); err != nil {
		logger.PluginW(inst.Manifest.ID, "host 方法 %s 被拒绝: %v", req.Method, err)
		resp := MakeError(req.ID, -32001, err.Error())
		inst.sendMu.Lock()
		inst.Stdin.Write(resp)
		inst.sendMu.Unlock()
		return
	}

	m.mu.RLock()
	handler, ok := m.hostMethods[req.Method]
	m.mu.RUnlock()
	if !ok {
		logger.PluginW(inst.Manifest.ID, "调用未知 host 方法: %s", req.Method)
		resp := MakeError(req.ID, -32601, fmt.Sprintf("未知的 host 方法: %s", req.Method))
		inst.sendMu.Lock()
		inst.Stdin.Write(resp)
		inst.sendMu.Unlock()
		return
	}

	result, err := handler(inst.Manifest.ID, req.Params)
	if err != nil {
		logger.PluginE(inst.Manifest.ID, "host 方法 %s 执行失败: %v", req.Method, err)
		resp := MakeError(req.ID, -1, err.Error())
		inst.sendMu.Lock()
		inst.Stdin.Write(resp)
		inst.sendMu.Unlock()
		return
	}

	resp := MakeResponse(req.ID, result)
	inst.sendMu.Lock()
	inst.Stdin.Write(resp)
	inst.sendMu.Unlock()
}

// InjectHostMethod 供 services 层注入实际 Host Method 实现
// 会覆盖默认的占位方法
func (m *Manager) InjectHostMethod(name string, handler HostMethod) {
	m.RegisterHostMethod(name, handler)
}
