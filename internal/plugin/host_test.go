package plugin

import (
	"encoding/json"
	"errors"
	"testing"
)

// newTestManager 手工构造 Manager，不调用 NewManager。
// NewManager 会执行 cleanupOrphans（按 pid 文件杀进程）、recoverInterruptedInstalls
// 并启动后台健康检查 goroutine，在单测里既有副作用也不可控。
// 本包测试与被测代码同包，直接装配所需字段即可。
func newTestManager() *Manager {
	return &Manager{
		plugins:     make(map[string]*PluginInstance),
		hostMethods: make(map[string]HostMethod),
	}
}

// addTestPlugin 登记一个插件实例（checkPermission 依赖 GetPlugin 能查到 manifest）。
func addTestPlugin(m *Manager, id string, perms Permissions) {
	m.plugins[id] = NewPluginInstance(PluginManifest{ID: id, Permissions: perms}, "")
}

// TestInvokeHostMethod 覆盖统一入口的三类结果与错误分类。
// 分类直接决定 handleCallback 回给插件的 JSON-RPC 错误码，错了插件侧无法区分
// 「没权限」和「方法不存在」。
func TestInvokeHostMethod(t *testing.T) {
	const pid = "test.invoke"

	m := newTestManager()
	addTestPlugin(m, pid, Permissions{})

	m.RegisterHostMethod("echo.ok", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			N int `json:"n"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]interface{}{"plugin": pluginID, "n": p.N}, nil
	})
	m.RegisterHostMethod("fail.boom", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return nil, errors.New("boom")
	})
	m.RegisterHostMethod("host.clipboard.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"text": "clip"}, nil
	})
	m.RegisterHostMethod("host.clipboard.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	})

	t.Run("正常分发：参数透传且带 pluginID", func(t *testing.T) {
		got, err := m.invokeHostMethod(pid, "echo.ok", json.RawMessage(`{"n":7}`))
		if err != nil {
			t.Fatalf("不应报错: %v", err)
		}
		res, ok := got.(map[string]interface{})
		if !ok {
			t.Fatalf("返回值类型不符: %T", got)
		}
		if res["n"] != 7 {
			t.Errorf("参数未透传: n=%v", res["n"])
		}
		if res["plugin"] != pid {
			t.Errorf("pluginID 未传入 handler: %v", res["plugin"])
		}
	})

	t.Run("未知方法返回 ErrUnknownHostMethod", func(t *testing.T) {
		_, err := m.invokeHostMethod(pid, "nope.nope", nil)
		if !errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("期望 ErrUnknownHostMethod，得到: %v", err)
		}
	})

	t.Run("未声明权限被拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod(pid, "host.clipboard.read", nil)
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("声明权限后放行", func(t *testing.T) {
		const pid2 = "test.perm"
		addTestPlugin(m, pid2, Permissions{Clipboard: true})
		got, err := m.invokeHostMethod(pid2, "host.clipboard.read", nil)
		if err != nil {
			t.Fatalf("已授权却报错: %v", err)
		}
		if got.(map[string]interface{})["text"] != "clip" {
			t.Errorf("返回值不符: %v", got)
		}
	})

	t.Run("实现内部错误不被误判为权限/未知方法", func(t *testing.T) {
		_, err := m.invokeHostMethod(pid, "fail.boom", nil)
		if err == nil {
			t.Fatal("应返回错误")
		}
		if errors.Is(err, ErrPermissionDenied) || errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("内部错误被误分类（会导致错误的 JSON-RPC 错误码）: %v", err)
		}
	})

	t.Run("插件不存在返回 ErrPluginNotFound", func(t *testing.T) {
		_, err := m.invokeHostMethod("ghost.plugin", "echo.ok", nil)
		if !errors.Is(err, ErrPluginNotFound) {
			t.Fatalf("期望 ErrPluginNotFound，得到: %v", err)
		}
	})
}

// TestInvokeHostMethodBridgeEntry 验证 iframe 代发桥走的导出入口。
// PluginService.CallPluginHostMethod 经此转发，必须与 native 回调、goja api.host
// 同源：权限校验生效、错误分类一致、pluginID 按调用方传入的身份透传。
func TestInvokeHostMethodBridgeEntry(t *testing.T) {
	const pid = "test.bridge"

	m := newTestManager()
	addTestPlugin(m, pid, Permissions{Network: NetworkPerm{All: true}})

	m.RegisterHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"plugin": pluginID}, nil
	})
	m.RegisterHostMethod("host.clipboard.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"text": "clip"}, nil
	})

	t.Run("已授权方法可代发", func(t *testing.T) {
		got, err := m.InvokeHostMethod(pid, "http.get", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("代发失败: %v", err)
		}
		if got.(map[string]interface{})["plugin"] != pid {
			t.Errorf("pluginID 未按传入身份透传: %v", got)
		}
	})

	t.Run("权限校验同样生效（iframe 不绕过）", func(t *testing.T) {
		_, err := m.InvokeHostMethod(pid, "host.clipboard.read", nil)
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("未知方法分类一致", func(t *testing.T) {
		_, err := m.InvokeHostMethod(pid, "nope.nope", nil)
		if !errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("期望 ErrUnknownHostMethod，得到: %v", err)
		}
	})

	t.Run("未知插件被拒", func(t *testing.T) {
		_, err := m.InvokeHostMethod("ghost.plugin", "http.get", nil)
		if !errors.Is(err, ErrPluginNotFound) {
			t.Fatalf("期望 ErrPluginNotFound，得到: %v", err)
		}
	})
}

// TestGojaAPIHostForwarding 验证 goja 插件通过 api.host 能真正打到宿主注册表。
// 这是本次改动的核心交付：此前 goja 只有 api.log 与 api.db，
// http/notify/dialog/clipboard 一概不可达。
func TestGojaAPIHostForwarding(t *testing.T) {
	const pid = "test.goja"

	m := newTestManager()
	// 显式声明 network：http.get 受 checkPermission 管控，未声明会被拒——
	// goja 转发与 native 回调共用同一套权限校验，不存在绕过。
	// clipboard 刻意不声明，供下方「权限拒绝」子用例验证。
	addTestPlugin(m, pid, Permissions{Network: NetworkPerm{All: true}})

	m.RegisterHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]interface{}{"url": p.URL, "from": pluginID, "status": 200}, nil
	})
	m.RegisterHostMethod("host.clipboard.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	})

	// pluginDB 传 nil：本用例只走 host 转发，不会触碰 api.db 的闭包。
	api := m.gojaAPI(PluginManifest{ID: pid}, nil)

	for _, key := range []string{"log", "warn", "error", "host", "db"} {
		if api[key] == nil {
			t.Fatalf("api.%s 缺失（goja 能力面被削弱）", key)
		}
	}

	hostFn, ok := api["host"].(func(string, interface{}) (interface{}, error))
	if !ok {
		t.Fatalf("api.host 签名不符，goja 里无法按 api.host(method, params) 调用: %T", api["host"])
	}

	t.Run("转发到注册表并回传结果", func(t *testing.T) {
		got, err := hostFn("http.get", map[string]interface{}{"url": "https://example.com"})
		if err != nil {
			t.Fatalf("转发失败: %v", err)
		}
		res, ok := got.(map[string]interface{})
		if !ok {
			t.Fatalf("返回值类型不符: %T", got)
		}
		if res["url"] != "https://example.com" {
			t.Errorf("参数未送达 handler: url=%v", res["url"])
		}
		if res["from"] != pid {
			t.Errorf("pluginID 未正确传递: %v", res["from"])
		}
	})

	t.Run("不传参数按空对象处理", func(t *testing.T) {
		got, err := hostFn("http.get", nil)
		if err != nil {
			t.Fatalf("空参数调用失败: %v", err)
		}
		if got.(map[string]interface{})["url"] != "" {
			t.Errorf("空参数应解析为零值: %v", got)
		}
	})

	t.Run("权限拒绝与 native 同源", func(t *testing.T) {
		_, err := hostFn("host.clipboard.write", map[string]interface{}{"text": "x"})
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("未知方法报错", func(t *testing.T) {
		_, err := hostFn("nope.nope", nil)
		if !errors.Is(err, ErrUnknownHostMethod) {
			t.Fatalf("期望 ErrUnknownHostMethod，得到: %v", err)
		}
	})
}

// TestCheckPermissionNetworkShellProcess 锁定三类新增权限的校验路径：
// 域名白名单（http）、目标前缀白名单（shell）、高危开关（process.kill）。
// 这些路径在 checkPermission 内完成，返回 ErrPermissionDenied 会直接被 handleCallback
// 映射为 -32001，插件侧据此知道自己没权限；与 native/goja/iframe 三端同源。
func TestCheckPermissionNetworkShellProcess(t *testing.T) {
	m := newTestManager()
	m.RegisterHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	m.RegisterHostMethod("host.shell.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})
	m.RegisterHostMethod("host.process.kill", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"ok": true}, nil
	})

	scoped := "test.scoped"
	addTestPlugin(m, scoped, Permissions{
		Network: NetworkPerm{Hosts: []string{"https://api.github.com"}},
		Shell:   ShellPerm{Targets: []string{"https://github.com"}},
	})

	t.Run("http 白名单内放行", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "http.get", json.RawMessage(`{"url":"https://api.github.com/x"}`))
		if err != nil {
			t.Fatalf("白名单内应放行: %v", err)
		}
	})
	t.Run("http 白名单外拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "http.get", json.RawMessage(`{"url":"https://evil.com"}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})
	t.Run("http userinfo 绕过被拒", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "http.get", json.RawMessage(`{"url":"https://evil.com@api.github.com"}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("shell 白名单内放行", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "host.shell.open", json.RawMessage(`{"target":"https://github.com/parieses"}`))
		if err != nil {
			t.Fatalf("白名单内应放行: %v", err)
		}
	})
	t.Run("shell 白名单外拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "host.shell.open", json.RawMessage(`{"target":"https://evil.com"}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	// process.kill：默认无权限
	t.Run("process.kill 默认拒绝", func(t *testing.T) {
		_, err := m.invokeHostMethod(scoped, "host.process.kill", json.RawMessage(`{"pid":1234}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})
	t.Run("process.kill 显式授权后放行", func(t *testing.T) {
		const pid = "test.kill"
		addTestPlugin(m, pid, Permissions{ProcessKill: true})
		_, err := m.invokeHostMethod(pid, "host.process.kill", json.RawMessage(`{"pid":1234}`))
		if err != nil {
			t.Fatalf("已授权应放行: %v", err)
		}
	})
}
