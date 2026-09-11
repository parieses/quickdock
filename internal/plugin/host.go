package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"quickdock/internal/logger"
)

// ---- 权限校验 ----

// fsHostSpec 声明一个 host.fs.* 方法所需的能力与必须校验的路径参数。
//
// 这张表是**默认拒绝**的依据：不在表内的 host.fs.* 一律按「未知方法」拒绝，
// 所以新增文件能力必须同时登记在这里，否则插件调用会被拒——避免再出现
// 「注册了方法却没人管控」的情况（历史上前例是 ui.show/ui.hide 的空占位）。
type fsHostSpec struct {
	action     string   // FSActionRead / FSActionWrite
	pathFields []string // params 中必须逐个通过白名单校验的路径字段名
}

var fsHostMethods = map[string]fsHostSpec{
	"host.fs.read":   {action: FSActionRead, pathFields: []string{"path"}},
	"host.fs.list":   {action: FSActionRead, pathFields: []string{"path"}},
	"host.fs.stat":   {action: FSActionRead, pathFields: []string{"path"}},
	"host.fs.exists": {action: FSActionRead, pathFields: []string{"path"}},
	"host.fs.write":  {action: FSActionWrite, pathFields: []string{"path"}},
	"host.fs.mkdir":  {action: FSActionWrite, pathFields: []string{"path"}},
	// remove 归 write：把文件从目录里挪走与往目录里写东西是同一级别的破坏力，
	// 只读 scope 不该获得「删掉这个文件」的能力。
	"host.fs.remove": {action: FSActionWrite, pathFields: []string{"path"}},
	// move 需要**双路径**都在 write scope 内：源少了写权限就是「移走无权移走的文件」，
	// 目标少了写权限等于凭空获得落点。任一越界即拒绝。
	"host.fs.move": {action: FSActionWrite, pathFields: []string{"from", "to"}},
}

// checkPermission 检查插件是否有权调用指定方法。
// params 参与校验是因为文件能力必须比对路径 scope——只判断「有没有 filesystem 权限」
// 是不够的，那只说明插件声明过文件能力，不代表它有权碰这个具体路径。
func (m *Manager) checkPermission(pluginID string, method string, params json.RawMessage) error {
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
		net := inst.Manifest.Permissions.Network
		if !net.Granted() {
			return fmt.Errorf("%w: 插件 %q 没有 network 权限", ErrPermissionDenied, pluginID)
		}
		// 域名白名单：只在声明了具体域名时才校验初始 URL 的 host；
		// 全放行（network:true）直接过。重定向后的最终 host 由 doPluginHTTP
		// 的 CheckRedirect 二次校验，避免被 302 绕过。
		if u, ok := jsonStringField(params, "url"); ok && !net.AllowsHost(u) {
			return fmt.Errorf("%w: 插件 %q 无权访问域名 %s（不在 permissions.network 白名单内）", ErrPermissionDenied, pluginID, u)
		}
	case strings.HasPrefix(method, "host.dialog."):
		if !inst.Manifest.Permissions.Filesystem.Dialog {
			return fmt.Errorf("%w: 插件 %q 没有 filesystem 权限", ErrPermissionDenied, pluginID)
		}
	case strings.HasPrefix(method, "host.fs."):
		return checkFSPermission(inst, method, params)
	case method == "host.shell.open":
		if !inst.Manifest.Permissions.Shell.Granted() {
			return fmt.Errorf("%w: 插件 %q 没有 shell 权限", ErrPermissionDenied, pluginID)
		}
		if t, ok := jsonStringField(params, "target"); ok && !inst.Manifest.Permissions.Shell.AllowsTarget(t) {
			return fmt.Errorf("%w: 插件 %q 无权打开目标 %s（不在 permissions.shell 白名单内）", ErrPermissionDenied, pluginID, t)
		}
	case method == "host.process.kill":
		if !inst.Manifest.Permissions.ProcessKill {
			return fmt.Errorf("%w: 插件 %q 没有 processKill 权限（kill 是高危操作，需显式声明 permissions.processKill）", ErrPermissionDenied, pluginID)
		}
	}

	return nil
}

// checkFSPermission 校验 host.fs.* 调用：能力声明 + 逐路径 scope 比对。
// 路径先解析为绝对真实路径再比对，阻断 .. 与符号链接穿越。
func checkFSPermission(inst *PluginInstance, method string, params json.RawMessage) error {
	spec, ok := fsHostMethods[method]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownHostMethod, method)
	}

	perm := inst.Manifest.Permissions.Filesystem
	if !perm.Granted() {
		return fmt.Errorf("%w: 插件 %q 没有 filesystem 权限", ErrPermissionDenied, inst.Manifest.ID)
	}

	var args map[string]json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil {
		return fmt.Errorf("参数解析失败: %w", err)
	}

	for _, field := range spec.pathFields {
		var raw string
		if v, ok := args[field]; ok {
			_ = json.Unmarshal(v, &raw)
		}
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("%s 缺少参数 %q", method, field)
		}
		real, err := ResolveRealPath(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		if !perm.Allows(spec.action, real) {
			return fmt.Errorf("%w: 插件 %q 无权 %s 路径 %s（不在 permissions.filesystem.%s 白名单内）",
				ErrPermissionDenied, inst.Manifest.ID, spec.action, real, spec.action)
		}
	}
	return nil
}

// jsonStringField 从 JSON-RPC params 中安全取出一个 string 字段。
// 字段不存在或不是字符串时返回 ok=false，调用方据此决定是否参与校验。
func jsonStringField(params json.RawMessage, field string) (string, bool) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(params, &args); err != nil {
		return "", false
	}
	v, ok := args[field]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
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

	// 注：ui.show / ui.hide 曾在此注册为空占位——没有任何实现，恒返回
	// {"status":"ok"} 的假成功，插件无法据此判断是否真的生效。两者既无插件调用、
	// 也未被 plugin-dev-guide 文档化，宿主侧的插件窗口管理由 PluginWindowMgr
	// 承担，故于 2026-09-10 移除。若日后要开放窗口控制能力，应在
	// services/plugin/plugin_host.go 注入真实实现（PluginService.ShowPluginWindow /
	// HidePluginWindow），不要在 registerDefaultHostMethods 里再放占位。
}

// ---- host 方法统一调用入口 ----

// invokeHostMethod 执行一次 host 方法调用：权限校验 → 查表 → 执行。
// native 的 JSON-RPC 回调（handleCallback）与 goja 的 api.host 转发共用本入口，
// 保证多条调用通路的权限语义与错误分类完全一致——新增能力只需注册进 hostMethods，
// 各运行时自动可用。
//
// 返回的 error 可用 errors.Is 区分，调用方据此回不同错误码：
//   - ErrPermissionDenied  → -32001（插件未声明对应权限）
//   - ErrUnknownHostMethod → -32601（方法不存在）
//   - 其它                 → -1（实现内部失败）
func (m *Manager) invokeHostMethod(pluginID, method string, params json.RawMessage) (interface{}, error) {
	if err := m.checkPermission(pluginID, method, params); err != nil {
		return nil, err
	}

	m.mu.RLock()
	handler, ok := m.hostMethods[method]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHostMethod, method)
	}

	return handler(pluginID, params)
}

// ---- handleCallback 的安全版本 ----

// InvokeHostMethod 供宿主侧调用方（services 层的 iframe 代发桥）按插件身份执行 host 方法。
// 与 native 的 JSON-RPC 回调、goja 的 api.host 共用 invokeHostMethod，
// 因此权限校验、方法查找与错误分类三端完全一致。
//
// ⚠️ 安全约定：pluginID 必须由**宿主侧状态**给出（当前打开的插件），
// 绝不能取自插件可伪造的 postMessage payload——否则插件能冒充其它插件调用
// db.* 等按 plugin_id 隔离的方法，越权读写他人数据。
func (m *Manager) InvokeHostMethod(pluginID, method string, params json.RawMessage) (interface{}, error) {
	return m.invokeHostMethod(pluginID, method, params)
}

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

	result, err := m.invokeHostMethod(inst.Manifest.ID, req.Method, req.Params)
	if err != nil {
		code := -1
		switch {
		case errors.Is(err, ErrPermissionDenied):
			code = -32001
			logger.PluginW(inst.Manifest.ID, "host 方法 %s 被拒绝: %v", req.Method, err)
		case errors.Is(err, ErrUnknownHostMethod):
			code = -32601
			logger.PluginW(inst.Manifest.ID, "调用未知 host 方法: %s", req.Method)
		default:
			logger.PluginE(inst.Manifest.ID, "host 方法 %s 执行失败: %v", req.Method, err)
		}
		resp := MakeError(req.ID, code, err.Error())
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
