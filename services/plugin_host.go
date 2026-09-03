package services

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
	"quickdock/internal/platform"

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
//	host.dialog.open / host.dialog.save          （需 permissions.filesystem）
//	http.get / http.post                         （需 permissions.network）
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

// pluginHTTPClient 插件专用 HTTP 客户端：独立超时与重定向上限，不复用宿主的其它 client。
var pluginHTTPClient = &http.Client{
	Timeout: pluginHTTPTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= pluginHTTPMaxRedirs {
			return fmt.Errorf("重定向次数超过 %d 次", pluginHTTPMaxRedirs)
		}
		return nil
	},
}

// RegisterPluginHostMethods 注入全部 Host API 实现。
// 必须在 DB 就绪之后调用（db.* 依赖 a.DB），由 ServiceStartup 触发。
func (a *AppService) RegisterPluginHostMethods() {
	if a.PluginMgr == nil {
		return
	}

	// ---- 剪贴板 ----
	a.PluginMgr.InjectHostMethod("host.clipboard.read", func(pluginID string, params json.RawMessage) (interface{}, error) {
		return map[string]interface{}{"text": platform.GetClipboardText()}, nil
	})

	a.PluginMgr.InjectHostMethod("host.clipboard.write", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		SetClipboardText(p.Text)
		return map[string]interface{}{"success": true}, nil
	})

	// ---- 系统通知 ----
	a.PluginMgr.InjectHostMethod("host.notify", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			Title   string `json:"title"`
			Message string `json:"message"`
			Body    string `json:"body"`
		}
		_ = json.Unmarshal(params, &p)
		body := p.Message
		if body == "" {
			body = p.Body
		}
		if p.Title == "" {
			p.Title = "快启坞插件"
		}
		if a.Notifier == nil {
			// 通知服务不可用时退回日志，不让插件调用直接失败
			logger.PluginW(pluginID, "notify 不可用（通知服务未初始化）: %s - %s", p.Title, body)
			return map[string]interface{}{"success": false, "reason": "notifier unavailable"}, nil
		}
		err := a.Notifier.SendNotification(notifications.NotificationOptions{
			ID:    "plugin-" + pluginID + "-" + time.Now().Format("20060102150405.000"),
			Title: p.Title,
			Body:  body,
		})
		if err != nil {
			return nil, fmt.Errorf("发送通知失败: %w", err)
		}
		return map[string]interface{}{"success": true}, nil
	})

	// ---- 文件对话框 ----
	a.PluginMgr.InjectHostMethod("host.dialog.open", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.app == nil {
			return nil, fmt.Errorf("应用未初始化")
		}
		var p struct {
			Title   string `json:"title"`
			Filters []struct {
				Name    string `json:"name"`
				Pattern string `json:"pattern"`
			} `json:"filters"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Title == "" {
			p.Title = "选择文件"
		}
		dlg := a.app.Dialog.OpenFile().SetTitle(p.Title).AttachToWindow(a.dialogParentWindow())
		for _, f := range p.Filters {
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

	a.PluginMgr.InjectHostMethod("host.dialog.save", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.app == nil {
			return nil, fmt.Errorf("应用未初始化")
		}
		var p struct {
			Title       string `json:"title"`
			DefaultName string `json:"defaultName"`
			Filters     []struct {
				Name    string `json:"name"`
				Pattern string `json:"pattern"`
			} `json:"filters"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Title == "" {
			p.Title = "保存文件"
		}
		dlg := a.app.Dialog.SaveFile().SetMessage(p.Title).AttachToWindow(a.dialogParentWindow())
		if p.DefaultName != "" {
			dlg = dlg.SetFilename(p.DefaultName)
		}
		for _, f := range p.Filters {
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
	a.PluginMgr.InjectHostMethod("http.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		return doPluginHTTP(pluginID, http.MethodGet, p.URL, p.Headers, "", "")
	})

	a.PluginMgr.InjectHostMethod("http.post", func(pluginID string, params json.RawMessage) (interface{}, error) {
		var p struct {
			URL         string            `json:"url"`
			Headers     map[string]string `json:"headers"`
			Body        string            `json:"body"`
			ContentType string            `json:"contentType"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		return doPluginHTTP(pluginID, http.MethodPost, p.URL, p.Headers, p.Body, p.ContentType)
	})

	// ---- 插件专属存储（按 plugin_id 强隔离，插件无法跨插件读写）----
	a.PluginMgr.InjectHostMethod("db.get", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		key, err := pluginDataKey(params)
		if err != nil {
			return nil, err
		}
		value, err := a.DB.GetPluginData(pluginID, key)
		if err != nil {
			// 键不存在不是错误，返回 found=false 让插件自行处理默认值
			return map[string]interface{}{"found": false, "value": ""}, nil
		}
		return map[string]interface{}{"found": true, "value": value}, nil
	})

	a.PluginMgr.InjectHostMethod("db.set", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		var p struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if err := validatePluginDataKey(p.Key); err != nil {
			return nil, err
		}
		if len(p.Value) > pluginDataMaxValue {
			return nil, fmt.Errorf("value 超过 %d 字节上限", pluginDataMaxValue)
		}
		if err := a.DB.SetPluginData(pluginID, p.Key, p.Value); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil
	})

	a.PluginMgr.InjectHostMethod("db.delete", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		key, err := pluginDataKey(params)
		if err != nil {
			return nil, err
		}
		if err := a.DB.DeletePluginData(pluginID, key); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil
	})

	a.PluginMgr.InjectHostMethod("db.list", func(pluginID string, params json.RawMessage) (interface{}, error) {
		if a.DB == nil {
			return nil, fmt.Errorf("数据库未初始化")
		}
		all, err := a.DB.ListPluginData(pluginID)
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

	logger.I("插件 Host API 已注入（clipboard / notify / dialog / http / db）；插件日志写入 plugin-YYYYMMDD.log")
}

// pluginDataKey 解析并校验只含 key 的参数体
func pluginDataKey(params json.RawMessage) (string, error) {
	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	if err := validatePluginDataKey(p.Key); err != nil {
		return "", err
	}
	return p.Key, nil
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
func doPluginHTTP(pluginID, method, rawURL string, headers map[string]string, body, contentType string) (interface{}, error) {
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

	resp, err := pluginHTTPClient.Do(req)
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
