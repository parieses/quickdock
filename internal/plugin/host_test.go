package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dop251/goja"
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

// TestInvokeHostMethodIdentityIsolation 锁定「身份与权限严格绑定，不可脱钩」。
//
// 宿主侧唯一能证明插件身份的就是 invokeHostMethod 的 pluginID 参数，权限全部按该身份
// 查 manifest 判定。因此桥（CallPluginHostMethod）必须取自宿主侧状态，绝不可采信插件
// 可伪造的 postMessage payload——否则插件传入他人 ID 即可借他人权限调用 db.* 越权读写。
// 本用例确保：身份一变，权限面立刻随之变化，任何「身份与权限脱钩」的实现都会在此失败。
func TestInvokeHostMethodIdentityIsolation(t *testing.T) {
	const granted = "test.iso.granted" // 声明了 clipboard
	const denied = "test.iso.denied"   // 未声明任何权限

	m := newTestManager()
	addTestPlugin(m, granted, Permissions{Clipboard: true})
	addTestPlugin(m, denied, Permissions{})

	// handler 回显身份，用于确认「权限判定的身份」与「执行时用的身份」是同一个
	m.RegisterHostMethod("host.clipboard.read", func(pluginID string, _ json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"plugin": pluginID}, nil
	})

	t.Run("有权限身份放行且 handler 收到该身份", func(t *testing.T) {
		got, err := m.invokeHostMethod(granted, "host.clipboard.read", nil)
		if err != nil {
			t.Fatalf("已授权却报错: %v", err)
		}
		if got.(map[string]interface{})["plugin"] != granted {
			t.Errorf("handler 收到的身份不符: %v", got)
		}
	})

	t.Run("无权限身份被拒", func(t *testing.T) {
		_, err := m.invokeHostMethod(denied, "host.clipboard.read", nil)
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("期望 ErrPermissionDenied，得到: %v", err)
		}
	})

	t.Run("身份不可借用：无权限插件传有权限插件的 ID 即可越权（故桥必须取自宿主状态）", func(t *testing.T) {
		// 刻意以 granted 身份调用：权限判定完全跟随传入身份，本身不做来源校验。
		// 这条用例把该事实固定下来——正因如此，桥传错/伪造身份的后果是直接的越权，
		// 安全边界在调用方，任何改动都应先确认桥仍以宿主状态为准。
		_, err := m.invokeHostMethod(granted, "host.clipboard.read", nil)
		if err != nil {
			t.Fatalf("身份即权限，granted 应放行: %v", err)
		}
	})

	t.Run("空身份按未知插件处理，不放行", func(t *testing.T) {
		_, err := m.invokeHostMethod("", "host.clipboard.read", nil)
		if !errors.Is(err, ErrPluginNotFound) {
			t.Fatalf("空身份期望 ErrPluginNotFound，得到: %v", err)
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

	// pluginDB / vm 传 nil：本用例只走 host 转发，不会触碰 api.db 与回调。
	api := m.gojaAPI(PluginManifest{ID: pid}, nil, nil)

	for _, key := range []string{"log", "warn", "error", "host", "aiStream", "db"} {
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
		addTestPlugin(m, pid, Permissions{ProcessKill: ProcessKillPerm{All: true}})
		_, err := m.invokeHostMethod(pid, "host.process.kill", json.RawMessage(`{"pid":1234}`))
		if err != nil {
			t.Fatalf("已授权应放行: %v", err)
		}
	})
	t.Run("process.kill 白名单不含目标时拒绝", func(t *testing.T) {
		const pid = "test.kill.scoped"
		addTestPlugin(m, pid, Permissions{ProcessKill: ProcessKillPerm{Programs: []string{"chrome"}}})
		// 查不到进程名（PID 1234 不存在）时也必须拒绝：证明不了清白就不能砍。
		_, err := m.invokeHostMethod(pid, "host.process.kill", json.RawMessage(`{"pid":1234}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("取不到进程名应拒绝，得到: %v", err)
		}
	})
	t.Run("process.kill 缺 pid 参数时拒绝", func(t *testing.T) {
		const pid = "test.kill.nopid"
		addTestPlugin(m, pid, Permissions{ProcessKill: ProcessKillPerm{Programs: []string{"chrome"}}})
		_, err := m.invokeHostMethod(pid, "host.process.kill", json.RawMessage(`{}`))
		if !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("缺 pid 应拒绝，得到: %v", err)
		}
	})
}

// TestGoJaAIStream 锁定 goja 插件的流式通道。
//
// goja 插件既没有 stdin（native 的 host.ai.delta 通知走不到）也没有前端
// （Wails 事件 plugin:ai-delta 走不到），增量只能靠 api.aiStream 的回调。
// 这条测试真的起一个 goja VM 跑 JS，确认「回调类型转换」这件事在运行期成立——
// 光看 Go 侧签名正确，不代表 goja 能把 JS 函数塞进 func(FunctionCall) Value。
func TestGoJaAIStream(t *testing.T) {
	const pid = "test.goja.aistream"
	m := newTestManager()

	var gotSystem, gotQuestion string
	m.SetAIStreamFn(func(system, question string, timeoutSec int, onToken func(string)) (string, string, error) {
		gotSystem, gotQuestion = system, question
		if onToken != nil {
			onToken("你")
			onToken("好")
		}
		return "你好", "test-model", nil
	})

	vm := goja.New()
	vm.Set("api", m.gojaAPI(PluginManifest{ID: pid}, nil, vm))

	// 增量必须逐段到达回调，且最终结果照常返回（不是二选一）。
	v, err := vm.RunString(`
		var deltas = [];
		var r = api.aiStream({ question: '你好', system: 'sys' }, function (d) { deltas.push(d); });
		JSON.stringify({ deltas: deltas.join(''), text: r.text, model: r.model });
	`)
	if err != nil {
		t.Fatalf("JS 调用 api.aiStream 失败: %v", err)
	}
	var out struct {
		Deltas string `json:"deltas"`
		Text   string `json:"text"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal([]byte(v.String()), &out); err != nil {
		t.Fatalf("结果解析失败: %v（原始: %s）", err, v.String())
	}
	if out.Deltas != "你好" {
		t.Errorf("增量回调收到 %q，期望 %q", out.Deltas, "你好")
	}
	if out.Text != "你好" || out.Model != "test-model" {
		t.Errorf("最终结果不符: text=%q model=%q", out.Text, out.Model)
	}
	if gotSystem != "sys" || gotQuestion != "你好" {
		t.Errorf("入参未送达: system=%q question=%q", gotSystem, gotQuestion)
	}
}

// TestGoJaAIStreamGuards 锁定入参闸门：与 host.ai.ask 同一份上限，
// 插件换个调用方式不能绕过长度限制。
func TestGoJaAIStreamGuards(t *testing.T) {
	m := newTestManager()
	m.SetAIStreamFn(func(system, question string, timeoutSec int, onToken func(string)) (string, string, error) {
		return "ok", "m", nil
	})
	vm := goja.New()
	vm.Set("api", m.gojaAPI(PluginManifest{ID: "test.goja.guard"}, nil, vm))

	t.Run("question 为空时报错", func(t *testing.T) {
		_, err := vm.RunString(`api.aiStream({ question: '   ' }, function () {})`)
		if err == nil {
			t.Fatal("空 question 应报错")
		}
	})

	t.Run("question 超长时报错", func(t *testing.T) {
		long := strings.Repeat("x", AIQuestionMaxRune+1)
		_, err := vm.RunString(`api.aiStream({ question: '` + long + `' }, function () {})`)
		if err == nil {
			t.Fatal("超长 question 应报错")
		}
	})

	t.Run("宿主未注入实现时报错", func(t *testing.T) {
		blank := newTestManager()
		vm2 := goja.New()
		vm2.Set("api", blank.gojaAPI(PluginManifest{ID: "test.goja.noai"}, nil, vm2))
		if _, err := vm2.RunString(`api.aiStream({ question: 'hi' }, function () {})`); err == nil {
			t.Fatal("未注入 AI 实现时应报错，而不是静默返回空文本")
		}
	})
}

// TestNotifyPluginNoop 锁定「不存在的插件 / goja 插件」都静默跳过。
//
// 流式增量是 best-effort：插件可能已退出、也可能根本没起进程。
// 这时 panic 或报错都会让一次正常的 AI 问答变成失败。
func TestNotifyPluginNoop(t *testing.T) {
	m := newTestManager()
	if err := m.NotifyPlugin("not.exist", "host.ai.delta", map[string]string{"delta": "x"}); err != nil {
		t.Errorf("未知插件应静默跳过，得到: %v", err)
	}
	// goja 实例没有 stdin，同样跳过。
	m.plugins["test.goja"] = NewPluginInstance(PluginManifest{ID: "test.goja"}, "")
	if err := m.NotifyPlugin("test.goja", "host.ai.delta", map[string]string{"delta": "x"}); err != nil {
		t.Errorf("goja 插件应静默跳过，得到: %v", err)
	}
}
