package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---- 权限校验 ----

// checkPermission 检查插件是否有权调用指定方法
func (m *Manager) checkPermission(pluginID string, method string) error {
	inst := m.GetPlugin(pluginID)
	if inst == nil {
		return ErrPluginNotFound
	}

	// 内部方法不需要权限
	if strings.HasPrefix(method, "log.") || strings.HasPrefix(method, "host.") {
		// 具体权限按方法细分
	}

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

// registerDefaultHostMethods 注册所有内置 Host Method
func (m *Manager) registerDefaultHostMethods() {
	// 日志
	m.RegisterHostMethod("log.info", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.Message != "" {
			fmt.Printf("QuickDock [plugin %s info]: %s\n", pluginID, p.Message)
		}
		return nil, nil
	})

	m.RegisterHostMethod("log.error", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.Message != "" {
			fmt.Printf("QuickDock [plugin %s ERROR]: %s\n", pluginID, p.Message)
		}
		return nil, nil
	})

	// 通知（通过标准输出打日志，实际通知由 services 层注册覆盖）
	m.RegisterHostMethod("host.notify", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(params, &p); err == nil {
			fmt.Printf("QuickDock [plugin %s notify]: %s - %s\n", pluginID, p.Title, p.Message)
		}
		return nil, nil
	})

	// 占位：以下方法由 services 层注册实际实现
	m.RegisterHostMethod("host.clipboard.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("host.clipboard.read 尚未注册实际实现")
	})
	m.RegisterHostMethod("host.clipboard.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("host.clipboard.write 尚未注册实际实现")
	})
	m.RegisterHostMethod("host.dialog.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("host.dialog.open 尚未注册实际实现")
	})
	m.RegisterHostMethod("host.dialog.save", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("host.dialog.save 尚未注册实际实现")
	})
	m.RegisterHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("http.get 尚未注册实际实现")
	})
	m.RegisterHostMethod("http.post", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("http.post 尚未注册实际实现")
	})
	m.RegisterHostMethod("db.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("db.get 尚未注册实际实现")
	})
	m.RegisterHostMethod("db.set", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("db.set 尚未注册实际实现")
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
// 由 readLoop goroutine 调用
func (m *Manager) handleCallback(inst *PluginInstance, req *RPCRequest) {
	// 通知（无 ID）不需要响应
	if req.ID == 0 {
		return
	}

	// 权限检查
	if err := m.checkPermission(inst.Manifest.ID, req.Method); err != nil {
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
		resp := MakeError(req.ID, -32601, fmt.Sprintf("未知的 host 方法: %s", req.Method))
		inst.sendMu.Lock()
		inst.Stdin.Write(resp)
		inst.sendMu.Unlock()
		return
	}

	result, err := handler(inst.Manifest.ID, req.Params)
	if err != nil {
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
