//go:build !windows

package env

import (
	"net/http"
	"syscall"
	"time"
)

// hideWindowAttr 非 Windows 平台无需隐藏窗口，返回 nil。
func hideWindowAttr() *syscall.SysProcAttr { return nil }

// proxyTransport 非 Windows 平台：仅使用环境变量代理（HTTP_PROXY/HTTPS_PROXY）。
func proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second}
}
