//go:build windows

package main

import (
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// updaterProxyFunc 优先使用环境变量代理（HTTP_PROXY/HTTPS_PROXY/NO_PROXY），
// 未配置时回退读取 Windows 系统代理注册表（WinINET）——兼容 Clash/v2rayN 等
// 工具的"系统代理"模式（它们只写注册表，不设环境变量）。
func updaterProxyFunc(req *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(req)
	if u != nil || err != nil {
		return u, err
	}
	return windowsSystemProxy(req)
}

// windowsSystemProxy 读取 HKCU 系统代理注册表（ProxyEnable/ProxyServer）。
// 支持 "host:port" 与 "http=host:port;https=host:port" 两种 ProxyServer 格式。
// 未启用或无法解析时返回 nil（直连）。PAC（AutoConfigURL）不支持，返回直连。
func windowsSystemProxy(req *http.Request) (*url.URL, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(server) == "" {
		return nil, nil
	}
	server = strings.TrimSpace(server)

	// ProxyServer 可能按协议分号分隔（http=...;https=...），此时按请求 scheme 选择
	proxy := server
	if strings.Contains(server, "=") {
		proxy = ""
		for _, part := range strings.Split(server, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), req.URL.Scheme) {
				proxy = strings.TrimSpace(kv[1])
				break
			}
		}
		if proxy == "" {
			return nil, nil
		}
	}

	if !strings.Contains(proxy, "://") {
		proxy = "http://" + proxy
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return nil, nil
	}
	return u, nil
}

// applyPlatformAppOptions 设置 Windows 专属应用选项（WebView2 内存优化 + 用户数据路径）。
func applyPlatformAppOptions(o *application.Options) {
	o.Windows = application.WindowsOptions{
		WebviewUserDataPath:   EnsureConfigDir() + "\\WebView2",
		AdditionalBrowserArgs: memoryOptimizedArgs,
		DisabledFeatures:      disabledFeatures,
	}
}
