//go:build !windows

package main

import (
	"net/http"
	"net/url"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// updaterProxyFunc 非 Windows 下使用环境变量代理（HTTP_PROXY/HTTPS_PROXY/NO_PROXY）。
// macOS 后续可接入 `scutil --proxy` 读取系统代理设置，P0 阶段先复用环境变量即可。
func updaterProxyFunc(req *http.Request) (*url.URL, error) {
	return http.ProxyFromEnvironment(req)
}

// applyPlatformAppOptions 非 Windows 下暂无需设置的平台应用选项。
// macOS 可在此设置 MacOptions（如 ActivationType），P0 阶段留空。
func applyPlatformAppOptions(o *application.Options) {}
