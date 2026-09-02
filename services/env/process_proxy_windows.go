//go:build windows

package env

import (
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	winreg "golang.org/x/sys/windows/registry"
)

// hideWindowAttr Windows 下用 CREATE_NO_WINDOW(0x08000000) 隐藏子进程控制台窗口
func hideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000}
}

// proxyTransport 返回代理感知的 http transport：优先环境变量，回退 WinINET 系统代理（注册表）。
func proxyTransport() http.RoundTripper {
	return &http.Transport{Proxy: systemProxyURL, TLSHandshakeTimeout: 10 * time.Second}
}

func systemProxyURL(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	k, err := winreg.OpenKey(winreg.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, winreg.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, nil
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return nil, nil
	}
	host := server
	if i := strings.Index(server, "="); i >= 0 {
		rest := server[i+1:]
		if j := strings.Index(rest, ";"); j >= 0 {
			rest = rest[:j]
		}
		host = rest
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return url.Parse(host)
}
