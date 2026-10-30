package plugin

import "quickdock/services"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"quickdock/internal/logger"
	mcpsrv "quickdock/internal/mcp"
	"quickdock/internal/platform"
	"quickdock/internal/plugin"
	"quickdock/internal/sysutil"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// ===== 插件 Host API 真实实现 =====
//
// internal/plugin 只负责 JSON-RPC 收发与权限校验，具体能力由本文件在启动时
// 通过 PluginMgr.InjectHostMethod 注入，避免 internal/plugin 反向依赖 services。
//
// 已实现方法：
//	host.clipboard.read / host.clipboard.write  （需 permissions.clipboard）
//	host.notify                                  （无需权限）
//	host.dialog.open / host.dialog.save          （需 permissions.filesystem 的对话框能力）
//	host.fs.read/list/stat/exists/write/mkdir    （需 permissions.filesystem 的对应 read/write scope）
//	http.get / http.post                         （需 permissions.network；域名白名单逐 host 校验 + 重定向二次校验）
//	host.shell.open                              （需 permissions.shell；目标前缀白名单）
//	host.process.list                            （无需权限，内置能力）
//	host.process.kill                           （需 permissions.processKill；高危，默认拒绝）
//	host.process.exec                           （需 permissions.process；程序名白名单，高危，默认拒绝）
//	host.mcp.call                                （无需权限，等级门由 mcpsrv.Call 把关）
//	host.ai.ask                                  （无需权限，复用宿主 AI 档案，一次性问答不落库；
//	                                              带 streamId 时沿途下发 host.ai.delta 增量）
//	host.ai.available                            （无需权限，预检宿主是否已配置可用 AI 档案；
//	                                              返回 available/reason/profile/model，避免盲目调用 host.ai.ask）
//	db.get / db.set / db.delete / db.list        （无需权限，按 plugin_id 强隔离）

const (
	pluginHTTPTimeout   = 15 * time.Second // 单次插件 HTTP 请求超时
	pluginHTTPMaxBody   = 2 << 20          // 响应体上限 2 MiB，防插件拉大文件撑爆内存
	pluginDataMaxKey    = 256              // 存储 key 长度上限
	pluginDataMaxValue  = 256 << 10        // 单条存储值上限 256 KiB
	pluginDataMaxList   = 500              // db.list 返回条数上限
	pluginHTTPMaxRedirs = 5
	pluginURLMaxLen     = 8 << 10 // url 长度上限 8 KiB，防超长 URL 拖慢 / 触发异常

	// host.ai.ask 的入参上限来自 internal/plugin（与 goja 的 api.aiStream 共用同一份，
	// 否则插件换个调用方式就能绕过闸门）。此处拦一道避免无谓往返，最终仍由 AIAsk 兜底裁剪。
	pluginAIQuestionMaxRune = plugin.AIQuestionMaxRune
	pluginAISystemMaxRune   = plugin.AISystemMaxRune

	// pluginAIStreamChunk 增量合批阈值（字节）。逐 token 推送会同时打满
	// Wails 事件总线与插件 stdin，量级与 SSH 面板保持一致。
	pluginAIStreamChunk = 40

	// pluginAIStreamMaxBytes 单次流式下发到插件的增量总量上限。
	//
	// 为什么要设上限：native 插件若在等响应时不读 stdin（模板就是单线程循环），
	// 管道写满会让 writeWithTimeout 超时并把实例标记成 writeBroken——那是整条
	// 通信链断掉，代价远大于"流式体验没做好"。24 KiB 低于 Windows 管道默认
	// 64 KiB 缓冲，正常长度的回答触不到；超限后只停发增量，最终文本仍随响应返回。
	pluginAIStreamMaxBytes = 24 << 10
)

// 屏幕取色等场景临时隐藏宿主窗口时的可见性快照：host.window.show 据此只还原
// 原本可见的窗口，避免取色结束后误弹出取色前就已隐藏的窗口（如主面板）。
var (
	pickWindowHiddenMain    bool
	pickWindowHiddenPalette bool
	pickWindowHiddenPlugin  bool
)

// pluginNetworkPerm 取插件声明的网络权限（用于 HTTP 域名白名单校验）。
func (svc *PluginService) pluginNetworkPerm(pluginID string) plugin.NetworkPerm {
	if svc.App.PluginMgr == nil {
		return plugin.NetworkPerm{}
	}
	if inst := svc.App.PluginMgr.GetPlugin(pluginID); inst != nil {
		return inst.Manifest.Permissions.Network
	}
	return plugin.NetworkPerm{}
}

// pluginShellPerm 取插件声明的 shell 权限（用于 host.shell.open 目标白名单校验）。
func (svc *PluginService) pluginShellPerm(pluginID string) plugin.ShellPerm {
	if svc.App.PluginMgr == nil {
		return plugin.ShellPerm{}
	}
	if inst := svc.App.PluginMgr.GetPlugin(pluginID); inst != nil {
		return inst.Manifest.Permissions.Shell
	}
	return plugin.ShellPerm{}
}

// emitPluginAIDelta 把 AI 增量推给 web 插件（iframe 形态）。
//
// 为什么走 Wails 事件再转一道，而不是直接 postMessage：增量产生在 Go 后端，
// 而插件 iframe 的窗口对象只有宿主前端持有（usePluginHost），Go 侧拿不到。
// 于是路径是「Go → 宿主前端事件 → 当前插件 iframe」。
//
// 载荷里必须带 pluginId：事件是全局广播的，宿主前端据此只转发给当前插件，
// 别的插件窗口收不到别人的模型输出。
func (svc *PluginService) emitPluginAIDelta(pluginID, streamID, delta string) {
	app := svc.App.App()
	if app == nil {
		return
	}
	app.Event.Emit("plugin:ai-delta", map[string]string{
		"pluginId": pluginID,
		"streamId": streamID,
		"delta":    delta,
	})
}

// RegisterPluginHostMethods 注入全部 Host API 实现。
// 必须在 DB 就绪之后调用（db.* 依赖 svc.App.DB），由 ServiceStartup 触发。
func (svc *PluginService) RegisterPluginHostMethods() {
	if svc.App.PluginMgr == nil {
		return
	}

	// ---- 剪贴板 ----
	svc.App.PluginMgr.InjectHostMethod("host.clipboard.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"text": platform.GetClipboardText()}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("host.clipboard.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if !services.SetClipboardText(arg.Text) {
			// 返回 success:false 而不是抛错：插件侧对「复制失败」通常只需要一次重试或提示，
			// 让它们自己决定要不要当成异常，避免插件把偶发的剪贴板占用当成宿主故障。
			return map[string]interface{}{"success": false, "error": "系统剪贴板被其他程序占用"}, nil
		}
		return map[string]interface{}{"success": true}, nil
	})

	// ---- 系统通知 ----
	svc.App.PluginMgr.InjectHostMethod("host.notify", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Title   string `json:"title"`
			Message string `json:"message"`
			Body    string `json:"body"`
		}
		_ = json.Unmarshal(params, &arg)
		body := arg.Message
		if body == "" {
			body = arg.Body
		}
		if arg.Title == "" {
			arg.Title = "快启坞插件"
		}
		if svc.App.Notifier == nil {
			// 通知服务不可用时退回日志，不让插件调用直接失败
			logger.PluginW(pluginID, "notify 不可用（通知服务未初始化）: %s - %s", arg.Title, body)
			return map[string]interface{}{"success": false, "reason": "notifier unavailable"}, nil
		}
		err := svc.App.Notifier.SendNotification(notifications.NotificationOptions{
			ID:    "plugin-" + pluginID + "-" + time.Now().Format("20060102150405.000"),
			Title: arg.Title,
			Body:  body,
		})
		if err != nil {
			return nil, fmt.Errorf("发送通知失败: %w", err)
		}
		return map[string]interface{}{"success": true}, nil
	})

	// ---- 文件对话框 ----
	svc.App.PluginMgr.InjectHostMethod("host.dialog.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.App() == nil {
			return nil, fmt.Errorf("应用未初始化")
		}
		var arg struct {
			Title   string `json:"title"`
			Filters []struct {
				Name    string `json:"name"`
				Pattern string `json:"pattern"`
			} `json:"filters"`
		}
		_ = json.Unmarshal(params, &arg)
		if arg.Title == "" {
			arg.Title = "选择文件"
		}
		dlg := svc.App.App().Dialog.OpenFile().SetTitle(arg.Title).AttachToWindow(svc.dialogParentWindow())
		for _, f := range arg.Filters {
			if f.Pattern != "" {
				name := f.Name
				if name == "" {
					name = f.Pattern
				}
				dlg = dlg.AddFilter(name, f.Pattern)
			}
		}
		path, err := dlg.PromptForSingleSelection()
		// 用户取消在部分平台返回 error 而非空串，统一按"取消"处理而不是报错
		if err != nil || path == "" {
			return map[string]interface{}{"canceled": true, "path": ""}, nil
		}
		return map[string]interface{}{"canceled": false, "path": path}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("host.dialog.save", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.App() == nil {
			return nil, fmt.Errorf("应用未初始化")
		}
		var arg struct {
			Title       string `json:"title"`
			DefaultName string `json:"defaultName"`
			Filters     []struct {
				Name    string `json:"name"`
				Pattern string `json:"pattern"`
			} `json:"filters"`
		}
		_ = json.Unmarshal(params, &arg)
		if arg.Title == "" {
			arg.Title = "保存文件"
		}
		dlg := svc.App.App().Dialog.SaveFile().SetMessage(arg.Title).AttachToWindow(svc.dialogParentWindow())
		if arg.DefaultName != "" {
			dlg = dlg.SetFilename(arg.DefaultName)
		}
		for _, f := range arg.Filters {
			if f.Pattern != "" {
				name := f.Name
				if name == "" {
					name = f.Pattern
				}
				dlg = dlg.AddFilter(name, f.Pattern)
			}
		}
		path, err := dlg.PromptForSingleSelection()
		if err != nil || path == "" {
			return map[string]interface{}{"canceled": true, "path": ""}, nil
		}
		return map[string]interface{}{"canceled": false, "path": path}, nil
	})

	// ---- HTTP ----
	// allowHost 用于重定向二次校验：初始 URL 的 host 已在 checkPermission 校验，
	// 但 302 可能把请求引到白名单外的域名，必须在每次跳转时再核一次。
	netAllow := func(pluginID string) func(string) bool {
		perm := svc.pluginNetworkPerm(pluginID)
		return func(rawURL string) bool { return perm.AllowsHost(rawURL) }
	}

	svc.App.PluginMgr.InjectHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		return doPluginHTTP(pluginID, http.MethodGet, arg.URL, arg.Headers, "", "", netAllow(pluginID))
	})

	svc.App.PluginMgr.InjectHostMethod("http.post", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			Body        string            `json:"body"`
			ContentType string            `json:"contentType"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		return doPluginHTTP(pluginID, http.MethodPost, arg.URL, arg.Headers, arg.Body, arg.ContentType, netAllow(pluginID))
	})

	// ---- 系统打开：以系统默认方式打开 URL / 文件 / 目录 ----
	// 走 sysutil.OpenDetached（禁裸 exec），目标须命中插件声明的 shell 白名单。
	svc.App.PluginMgr.InjectHostMethod("host.shell.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if strings.TrimSpace(arg.Target) == "" {
			return nil, fmt.Errorf("target 不能为空")
		}
		perm := svc.pluginShellPerm(pluginID)
		if !perm.AllowsTarget(arg.Target) {
			return nil, fmt.Errorf("目标 %q 不在 permissions.shell 白名单内", arg.Target)
		}
		if err := sysutil.OpenDetached(arg.Target, ""); err != nil {
			return nil, fmt.Errorf("打开 %q 失败: %w", arg.Target, err)
		}
		return map[string]interface{}{"success": true}, nil
	})

	// ---- 执行程序（高危，需显式声明 permissions.process）----
	// 权限校验已在 internal/plugin/host.go 的 checkPermission 完成：
	// 未声明 permissions.process 一律拒绝；带白名单时程序名必须在白名单内。
	// 这里只负责真正把进程跑起来。
	svc.App.PluginMgr.InjectHostMethod("host.process.exec", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Program string   `json:"program"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		program := strings.TrimSpace(arg.Program)
		if program == "" {
			return nil, fmt.Errorf("program 不能为空")
		}
		// 参数数量上限：防止插件用超大参数列表撑爆宿主内存/命令行长度。
		if len(arg.Args) > 64 {
			return nil, fmt.Errorf("args 数量超出上限（最多 64 个）")
		}
		for _, a := range arg.Args {
			if len(a) > 4096 {
				return nil, fmt.Errorf("单个参数过长（上限 4096 字节）")
			}
		}

		// 超时兜底：宿主不能被插件的进程拖住（插件可传 "timeout" 毫秒，默认 10s）。
		timeoutMs := 10000
		var raw struct {
			Timeout int `json:"timeout"`
		}
		_ = json.Unmarshal(params, &raw)
		if raw.Timeout > 0 {
			timeoutMs = raw.Timeout
		}
		if timeoutMs > 120000 {
			timeoutMs = 120000
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()

		cmd := sysutil.CommandContext(ctx, program, arg.Args...)
		// 合并 stdout/stderr：插件看到的是程序真实输出，不做静默丢弃。
		out, err := cmd.CombinedOutput()
		res := map[string]interface{}{
			"success":  err == nil,
			"output":   string(out),
			"exitCode": cmd.ProcessState.ExitCode(),
		}
		if err != nil {
			// 进程非零退出不等于宿主失败：把错误信息带回，由插件自行判断。
			res["error"] = err.Error()
		}
		return res, nil
	})

	// ---- 进程列表（全量，按内存降序）----
	svc.App.PluginMgr.InjectHostMethod("host.process.list", func(pluginID string, params json.RawMessage) (interface{}, error) {
		procs, err := sysutil.ListProcesses()
		if err != nil {
			return nil, fmt.Errorf("枚举进程失败: %w", err)
		}
		out := make([]map[string]interface{}, 0, len(procs))
		for _, p := range procs {
			out = append(out, map[string]interface{}{
				"pid":      p.PID,
				"name":     p.Name,
				"memBytes": p.MemBytes,
			})
		}
		return map[string]interface{}{"processes": out, "count": len(out)}, nil
	})

	// ---- 结束进程（高危，需显式声明 permissions.processKill）----
	svc.App.PluginMgr.InjectHostMethod("host.process.kill", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if arg.PID <= 0 {
			return nil, fmt.Errorf("pid 必须为正整数")
		}
		if err := sysutil.KillProcess(arg.PID); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true, "pid": arg.PID}, nil
	})

	// ---- 加密 / 解密（替代 iframe 里不可用的 window.crypto.subtle）----
	// iframe 的 sandbox 不含 allow-same-origin → opaque origin → crypto.subtle 不可用，
	// 插件（如 vault）只能降级成异或混淆。这里把 AES-256-GCM + PBKDF2 放到宿主持有完成。
	// 无需 permissions：加密本身不触碰任何受控资源，密钥材料只存在于单次调用内。
	// 明文上限 1 MiB、PBKDF2 100000 次（与 vault 前端参数一致，保证新旧路径可互解）。
	svc.App.PluginMgr.InjectHostMethod("host.crypto.encrypt", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Password  string `json:"password"`
			Salt      string `json:"salt"`
			Plaintext string `json:"plaintext"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		salt, err := plugin.B64(arg.Salt)
		if err != nil {
			return nil, err
		}
		plain, err := plugin.B64(arg.Plaintext)
		if err != nil {
			return nil, err
		}
		iv, data, err := plugin.EncryptForPlugin(arg.Password, salt, plain)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"iv": iv, "data": data}, nil
	})
	svc.App.PluginMgr.InjectHostMethod("host.crypto.decrypt", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Password string `json:"password"`
			Salt     string `json:"salt"`
			IV       string `json:"iv"`
			Data     string `json:"data"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		salt, err := plugin.B64(arg.Salt)
		if err != nil {
			return nil, err
		}
		plain, err := plugin.DecryptForPlugin(arg.Password, salt, arg.IV, arg.Data)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"plaintext": plugin.B64Enc(plain)}, nil
	})
	// 密码学随机数（生成 salt / IV / 随机密码）。
	svc.App.PluginMgr.InjectHostMethod("host.crypto.random", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Bytes int `json:"bytes"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if arg.Bytes <= 0 {
			arg.Bytes = 16 // 默认 128-bit
		}
		s, err := plugin.RandomForPlugin(arg.Bytes)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"base64": s}, nil
	})

	// ---- MCP 工具 ----
	// 复用宿主内置 MCP Server 已注册的工具（item/note/todo/clipboard/env/port…），
	// 插件无需重复实现检索与业务能力，且与 AI 客户端共享同一能力面。
	// 等级门由 mcpsrv.Call 统一把关：默认 maxLvl=LevelWrite，LevelRisk 工具
	// （process_kill / system_command 等）自动被拒——插件侧无需额外权限声明。
	// 未注册与等级不足都归类为执行失败（-1），错误文案本身已说明原因。
	svc.App.PluginMgr.InjectHostMethod("host.mcp.call", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var arg struct {
			Tool string         `json:"tool"`
			Args map[string]any `json:"args"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if strings.TrimSpace(arg.Tool) == "" {
			return nil, fmt.Errorf("tool 不能为空")
		}
		res, err := mcpsrv.Call(arg.Tool, arg.Args)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"tool": arg.Tool, "result": res}, nil
	})

	// ---- AI 能力 ----
	//
	// 让插件用宿主的 AI 档案做一次性问答：插件自己不带 API Key，
	// 也不用各自实现流式解析与重试，凭据始终留在 Go 后端。
	//
	// 不设权限门：AI 档案是用户自己配的凭据，插件能提问不等于能读到凭据
	//（返回的只有模型正文）。若后续要按插件区分额度/模型，再加 scope。
	svc.App.PluginMgr.InjectHostMethod("host.ai.ask", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.AskAIFn == nil {
			return nil, fmt.Errorf("AI 能力不可用")
		}
		var arg struct {
			// System 可选。留空则用档案里配置的 SystemPrompt。
			System string `json:"system"`
			// Question 必填。
			Question string `json:"question"`
			// TimeoutSec 可选，上限由 AIAsk 内部收敛到 120s。
			TimeoutSec int `json:"timeoutSec"`
			// StreamID 可选。非空即开启流式：宿主沿途下发增量，最终文本仍随响应返回。
			//
			// 用「有没有 streamId」而不是另开一个 host.ai.askStream 方法：
			// 三条运行时（native / goja / web）的增量投递方式各不相同，但请求语义相同。
			// 拆两个方法会让插件作者以为拿到了两套能力，实际只是同一件事的两个入口。
			StreamID string `json:"streamId"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		question := strings.TrimSpace(arg.Question)
		if question == "" {
			return nil, fmt.Errorf("question 不能为空")
		}
		// 入参上限：插件是第三方代码，不能假设它会自觉控制长度。
		// 与 ai_ask.go 的常量同量级，这里先拦一道，省掉一次无谓的网络往返。
		// 超长只截断不报错：插件传来的 system 很长是常见诉求（如携带完整 API 参考），
		// 硬拒会让整次调用直接失败；截断到上限后仍是一段可用的系统提示词，调用照常成功。
		if n := len([]rune(question)); n > pluginAIQuestionMaxRune {
			logger.PluginW(pluginID, "AI question 超长（上限 %d 字符，当前 %d），已截断", pluginAIQuestionMaxRune, n)
			question = string([]rune(question)[:pluginAIQuestionMaxRune])
		}
		if n := len([]rune(arg.System)); n > pluginAISystemMaxRune {
			logger.PluginW(pluginID, "AI system 超长（上限 %d 字符，当前 %d），已截断", pluginAISystemMaxRune, n)
			arg.System = string([]rune(arg.System)[:pluginAISystemMaxRune])
		}
		// 流式：按 streamID 沿途下发增量（合批，避免逐 token 推事件）。
		// 没有 streamId 时 onToken 为 nil，走一次性路径。
		// 条件里带上 AskAIFnStream != nil：流式实现可能未装配（测试/裁剪构建），
		// 此时静默退化为一次性问答，好过插件拿一个 nil 函数直接 panic。
		streamID := strings.TrimSpace(arg.StreamID)
		var onToken func(string)
		var flush func()
		if streamID != "" && svc.App.AskAIFnStream != nil {
			var buf strings.Builder
			sent := 0
			// stdio 通道写失败后停发：增量是 best-effort，同一个错误不该每个 token 都撞一次。
			notifyBroken := false
			flush = func() {
				if buf.Len() == 0 {
					return
				}
				delta := buf.String()
				buf.Reset()
				// 超上限后丢弃剩余增量：只影响"看到多少"，不影响最终结果。
				if sent >= pluginAIStreamMaxBytes {
					return
				}
				sent += len(delta)
				svc.emitPluginAIDelta(pluginID, streamID, delta)
				if notifyBroken {
					return
				}
				if err := svc.App.PluginMgr.NotifyPlugin(pluginID, "host.ai.delta", map[string]string{
					"streamId": streamID,
					"delta":    delta,
				}); err != nil {
					notifyBroken = true
					logger.PluginW(pluginID, "AI 增量下发失败，本次流式改为只返回最终结果: %v", err)
				}
			}
			onToken = func(delta string) {
				buf.WriteString(delta)
				if buf.Len() >= pluginAIStreamChunk {
					flush()
				}
			}
		}
		var text, model string
		var err error
		if onToken != nil {
			text, model, err = svc.App.AskAIFnStream(arg.System, question, arg.TimeoutSec, onToken)
			// 结尾不足一个合批阈值的尾巴也要送出去，否则最后几个字会凭空消失。
			flush()
		} else {
			text, model, err = svc.App.AskAIFn(arg.System, question, arg.TimeoutSec)
		}
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"text": text, "model": model}, nil
	})

	// host.ai.available：让插件在调用 host.ai.ask 前先判断宿主是否配置了可用的 AI 档案，
	// 避免「盲目调用 → 拿到未配置/不可用 的裸错」。
	//
	// 无权限门（与 host.ai.ask 一致）；不触碰任何凭据，只回报激活档案的可用性与元信息。
	// 返回 { available, reason, profile, model }：
	//   available=false 时 reason ∈ { no_profile, no_endpoint, unavailable }，
	//   插件据此提示「请在 设置 → AI 中配置 AI 档案」而非发起注定失败的调用。
	svc.App.PluginMgr.InjectHostMethod("host.ai.available", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.AIAvailableFn == nil {
			return map[string]interface{}{"available": false, "reason": "unavailable"}, nil
		}
		available, reason, profile, model := svc.App.AIAvailableFn()
		return map[string]interface{}{
			"available": available,
			"reason":    reason,
			"profile":   profile,
			"model":     model,
		}, nil
	})

	// ---- 插件专属存储（按 plugin_id 强隔离，插件无法跨插件读写）----
	svc.App.PluginMgr.InjectHostMethod("db.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		key, err := pluginDataKey(params)
		if err != nil {
			return nil, err
		}
		value, err := svc.App.DB.GetPluginData(pluginID, key)
		if err != nil {
			// 键不存在不是错误，返回 found=false 让插件自行处理默认值
			return map[string]interface{}{"found": false, "value": ""}, nil
		}
		return map[string]interface{}{"found": true, "value": value}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("db.set", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		var arg struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(params, &arg); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if err := validatePluginDataKey(arg.Key); err != nil {
			return nil, err
		}
		if len(arg.Value) > pluginDataMaxValue {
			return nil, fmt.Errorf("value 超过 %d 字节上限", pluginDataMaxValue)
		}
		if err := svc.App.DB.SetPluginData(pluginID, arg.Key, arg.Value); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("db.delete", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		key, err := pluginDataKey(params)
		if err != nil {
			return nil, err
		}
		if err := svc.App.DB.DeletePluginData(pluginID, key); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("db.list", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if svc.App.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		all, err := svc.App.DB.ListPluginData(pluginID)
		if err != nil {
			return nil, err
		}
		truncated := false
		if len(all) > pluginDataMaxList {
			trimmed := make(map[string]string, pluginDataMaxList)
			n := 0
			for k, v := range all {
				if n >= pluginDataMaxList {
					break
				}
				trimmed[k] = v
				n++
			}
			all = trimmed
			truncated = true
		}
		return map[string]interface{}{"data": all, "truncated": truncated}, nil
	})

	// ---- 文件系统（host.fs.*，实现在 plugin_fs.go）----
	svc.registerFSHostMethods()

	// ---- 窗口显隐（供插件在屏幕取色等场景临时隐藏宿主窗口，避免遮挡取样区域）----
	// 同时管控「主窗口 + 命令面板窗口」：内联插件渲染在面板窗口里，仅隐藏主窗口盖不住它。
	// 隐藏前记录各自原可见性，恢复时只还原原本可见的窗口，避免取色后误弹出原本隐藏的窗口。
	// 无额外权限门槛（纯 UI 便利，低风险）；同步 WindowFlags 防止热键 toggle 状态错位。
	svc.App.PluginMgr.InjectHostMethod("host.window.hide", func(pluginID string, params json.RawMessage) (interface{}, error) {
		mainWasVisible := svc.App.MainWindow != nil && svc.App.MainWindow.IsVisible()
		paletteWin := svc.App.GetPaletteWindow()
		paletteWasVisible := paletteWin != nil && paletteWin.IsVisible()
		pickWindowHiddenMain = mainWasVisible
		pickWindowHiddenPalette = paletteWasVisible
		pluginWinVisible := svc.App.PluginWindowMgr != nil && svc.App.PluginWindowMgr.IsWindowVisible(pluginID)
		pickWindowHiddenPlugin = pluginWinVisible
		if mainWasVisible && svc.App.MainWindow != nil {
			svc.App.MainWindow.Hide()
			if svc.App.Flags != nil {
				svc.App.Flags.Main.Store(false)
				svc.App.Flags.Clipboard.Store(false)
			}
		}
		if paletteWasVisible && paletteWin != nil {
			paletteWin.Hide()
			if svc.App.Flags != nil {
				svc.App.Flags.Palette.Store(false)
			}
		}
		// 独立插件窗口（如「在窗口中打开」模式）：同样临时隐藏，取色后才恢复
		if pluginWinVisible && svc.App.PluginWindowMgr != nil {
			svc.App.PluginWindowMgr.Hide(pluginID)
		}
		return map[string]interface{}{"success": true}, nil
	})

	svc.App.PluginMgr.InjectHostMethod("host.window.show", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if pickWindowHiddenMain && svc.App.MainWindow != nil {
			svc.App.MainWindow.Show()
			svc.App.MainWindow.Focus()
			if svc.App.Flags != nil {
				svc.App.Flags.Main.Store(true)
			}
		}
		if pickWindowHiddenPalette {
			if paletteWin := svc.App.GetPaletteWindow(); paletteWin != nil {
				paletteWin.Show()
				paletteWin.Focus()
				if svc.App.Flags != nil {
					svc.App.Flags.Palette.Store(true)
				}
			}
		}
		// 恢复被临时隐藏的独立插件窗口（若原本可见）
		if pickWindowHiddenPlugin && svc.App.PluginWindowMgr != nil {
			svc.App.PluginWindowMgr.ShowWindow(pluginID)
		}
		return map[string]interface{}{"success": true}, nil
	})

	logger.I("插件 Host API 已注入（clipboard / notify / dialog / http / fs / mcp / db / window）；插件日志写入 plugin-YYYYMMDD.log")
}

// CallPluginHostMethod 供宿主前端（插件 iframe 桥接）按插件身份代发任意 host 方法。
//
// 存在的意义：none 运行时插件跑在 iframe 里，`fetch` 跨域被 CORS 拦死，也没有
// 任何宿主能力；而 native / goja 各自有专属通道。与其为每个新能力加一个前端绑定，
// 不如加这一个通用转发——三端从此收敛到同一个 invokeHostMethod 入口，
// 后续新增 host 方法前端无需再改。
//
// ⚠️ 安全约定：pluginID 由宿主侧状态给出（usePluginHost 的 opts.pluginId() 取自
// 当前打开的插件），绝不可从 iframe 的 postMessage payload 读取，否则插件可冒充
// 其它插件调用 db.* 越权读写。
func (svc *PluginService) CallPluginHostMethod(pluginID, method, params string) *services.ApiResult {
	if svc.App.PluginMgr == nil {
		return services.FailMsg("插件管理器未初始化")
	}
	if strings.TrimSpace(pluginID) == "" {
		return services.FailMsg("pluginID 不能为空")
	}
	if strings.TrimSpace(method) == "" {
		return services.FailMsg("method 不能为空")
	}
	raw := json.RawMessage(params)
	if strings.TrimSpace(params) == "" {
		raw = json.RawMessage("{}")
	}
	result, err := svc.App.PluginMgr.InvokeHostMethod(pluginID, method, raw)
	if err != nil {
		return services.Fail(err)
	}
	if result == nil {
		result = map[string]interface{}{}
	}
	return services.Ok(result)
}

// pluginDataKey 解析并校验只含 key 的参数体
func pluginDataKey(params json.RawMessage) (string, error) {
	var arg struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(params, &arg); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if err := validatePluginDataKey(arg.Key); err != nil {
		return "", err
	}
	return arg.Key, nil
}

func validatePluginDataKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("key 不能为空")
	}
	if len(key) > pluginDataMaxKey {
		return fmt.Errorf("key 超过 %d 字节上限", pluginDataMaxKey)
	}
	return nil
}

// doPluginHTTP 执行插件发起的 HTTP 请求，限制协议、超时与响应体大小。
// allowHost 为可选回调：每次重定向时二次校验最终 URL 的域名，防止 302 把请求
// 引到白名单外的站点（初始 URL 的 host 已在 checkPermission 校验过）。
func doPluginHTTP(pluginID, method, rawURL string, headers map[string]string, body, contentType string, allowHost func(string) bool) (interface{}, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url 不能为空")
	}
	if len(rawURL) > pluginURLMaxLen {
		return nil, fmt.Errorf("url 过长（超过 %d 字符）", pluginURLMaxLen)
	}
	if strings.ContainsAny(rawURL, "\r\n") {
		return nil, fmt.Errorf("url 包含非法换行字符")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("url 非法: %w", err)
	}
	// 只允许 http/https，杜绝 file:// 读本地文件、自定义协议触发外部程序
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("仅支持 http/https 协议，收到: %s", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url 缺少主机名")
	}

	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		// 不允许插件伪造 Host，其余头放行
		if strings.EqualFold(k, "Host") {
			continue
		}
		req.Header.Set(k, v)
	}
	if method == http.MethodPost && req.Header.Get("Content-Type") == "" {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "QuickDock-Plugin/"+pluginID)
	}

	client := &http.Client{
		Timeout: pluginHTTPTimeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= pluginHTTPMaxRedirs {
				return fmt.Errorf("重定向次数超过 %d 次", pluginHTTPMaxRedirs)
			}
			if allowHost != nil && !allowHost(r.URL.String()) {
				return fmt.Errorf("重定向目标 %s 不在插件 network 白名单内", r.URL.String())
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 多读 1 字节以判断是否被截断
	data, err := io.ReadAll(io.LimitReader(resp.Body, pluginHTTPMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	truncated := false
	if len(data) > pluginHTTPMaxBody {
		data = data[:pluginHTTPMaxBody]
		truncated = true
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return map[string]interface{}{
		"status":    resp.StatusCode,
		"ok":        resp.StatusCode >= 200 && resp.StatusCode < 300,
		"headers":   respHeaders,
		"body":      string(data),
		"truncated": truncated,
	}, nil
}
