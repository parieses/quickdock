//go:build !windows

package dsh

// detectWebView2 在非 Windows 平台无 WebView2 概念，直接返回满足。
// QuickDock 的 dsh 原生窗口仅在 Windows 上经由 WebView2 承载，其余平台仅作开发/CI 用途。
func detectWebView2() WebView2Check {
	return WebView2Check{OK: true, Version: "n/a", MinVersion: minWebView2Version}
}
