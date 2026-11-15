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

// HasProxy 报告当前是否真的配了代理。
//
// 供「直连失败 → 代理兜底」这类重试链使用：没有代理时那次兜底其实就是再来一次
// 直连，只是白等一个超时。ProxyFromEnvironment 读的是环境变量，与 proxyTransport 一致。
func HasProxy() bool {
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid", nil)
	if err != nil {
		return false
	}
	u, err := http.ProxyFromEnvironment(req)
	return err == nil && u != nil
}
