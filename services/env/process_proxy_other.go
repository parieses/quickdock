//go:build !windows

package env

import (
	"errors"
	"net/http"
	"time"
)

// processExePathWin 非 Windows 平台不提供（Windows 分支才需要按 pid 反查 exe 路径）。
func processExePathWin(int) (string, error) { return "", errors.New("windows only") }

// findListenPIDWin 非 Windows 平台不提供（走 lsof 分支）。
func findListenPIDWin(int) int { return 0 }

// proxyTransport 非 Windows 平台：仅使用环境变量代理（HTTP_PROXY/HTTPS_PROXY）。
func proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second}
}
