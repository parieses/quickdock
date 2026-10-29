package dsh

// WebView2Check 描述承载 dsh 原生窗口的 WebView2 运行时版本检测结果。
// dsh 0.2.1-rc.4 前端依赖 Chrome 119+ 的 Promise.withResolvers / AbortSignal.any，
// 低于此版本会在连接层抛 JS 异常 → WebSocket 无限 "connection lost, retry"，
// 页面虽能渲染却永远连不上后端。此检测用于打开 dsh 前给出明确升级提示，
// 避免用户对着一个死页面反复重试。
type WebView2Check struct {
	OK         bool   `json:"ok"`         // 版本是否满足 dsh 要求
	Version    string `json:"version"`    // 检测到的版本；未安装/无法读取时为 "unknown"
	MinVersion string `json:"minVersion"` // dsh 要求的最低版本（用于提示文案）
}

// minWebView2Major 是 dsh 前端能正常工作的最低 Chromium 主版本号。
// dsh 0.2.1-rc.4 用到 Promise.withResolvers（Chrome 119+）与 AbortSignal.any（Chrome 116+），
// 取较高者 119 作为门槛。
const minWebView2Major = 119

// minWebView2Version 仅用于提示文案中的最低版本号。
const minWebView2Version = "119.0.0"

// CheckWebView2 检测 WebView2 运行时版本是否满足 dsh 窗口要求。
// 平台相关实现见 webview2_windows.go / webview2_other.go。
func (m *DSHProcessManager) CheckWebView2() WebView2Check {
	return detectWebView2()
}
