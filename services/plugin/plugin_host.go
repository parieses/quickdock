package plugin

import "quickdock/services"

import (
	"bytes"
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
//	host.mcp.call                                （无需权限，等级门由 mcpsrv.Call 把关）
//	db.get / db.set / db.delete / db.list        （无需权限，按 plugin_id 强隔离）

const (
	pluginHTTPTimeout   = 15 * time.Second // 单次插件 HTTP 请求超时
	pluginHTTPMaxBody   = 2 << 20          // 响应体上限 2 MiB，防插件拉大文件撑爆内存
	pluginDataMaxKey    = 256              // 存储 key 长度上限
	pluginDataMaxValue  = 256 << 10        // 单条存储值上限 256 KiB
	pluginDataMaxList   = 500              // db.list 返回条数上限
	pluginHTTPMaxRedirs = 5
	pluginURLMaxLen     = 8 << 10 // url 长度上限 8 KiB，防超长 URL 拖慢 / 触发异常
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
		services.SetClipboardText(arg.Text)
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

	logger.I("插件 Host API 已注入（clipboard / notify / dialog / http / fs / mcp / db）；插件日志写入 plugin-YYYYMMDD.log")
}

// CallPluginHostMethod 供宿主前端（插件 iframe 桥接）按插件身份代发任意 host 方法。
//
// 存在的意义：none 运行时插件跑在 iframe 里，`fetch` 跨域被 CORS 拦死，也没有
// 任何宿主能力；而 native / goja 各自有专属通道。与其为每个新能力加一个前端绑定，
// 不如加这一个通用转发——三端从此收敛到同一个 invokeHostMethod 入口，
// 后续新增 host 方法前端无需再改。
//
// ⚠️ 安全约定：pluginID 由宿主侧状态给出（usePluginHost 的 opts.pluginId() 取自
// 当前打开的插件），**绝不可从 iframe 的 postMessage payload 读取**，否则插件可冒充
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
