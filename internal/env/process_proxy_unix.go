//go:build darwin || linux

package env

import (
	"net/http"
	"time"
)

// findListenPIDWin 非 Windows 平台不提供（走 lsof 分支）。
func findListenPIDWin(int) int { return 0 }

// proxyTransport 非 Windows 平台：仅使用环境变量代理（HTTP_PROXY/HTTPS_PROXY）。
func proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second}
}
