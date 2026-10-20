package sshx

import "quickdock/services"

// OpenSshWindow 打开（或唤回）SSH 终端独立窗口。
//
// 与 DSH 窗口（services/dsh）同样采用「已有就复用」：SSH 会话常驻 Go 侧
// （internal/sshx 的 Manager），复用同一个窗口才能保住终端滚动缓冲与已建立的连接；
// 每次点击都新建窗口会导致用户开第二台机器时丢掉第一台的画面。
//
// 窗口本身由 main 包懒创建（windows.go 的 sshWindowGetter），这里只负责显示。
func (s *SshService) OpenSshWindow() *services.ApiResult {
	if s.App == nil || s.App.GetSshWindow == nil {
		return services.FailMsg("SSH 窗口未就绪")
	}
	win := s.App.GetSshWindow()
	if win == nil {
		return services.FailMsg("SSH 窗口创建失败")
	}
	win.Show()
	win.Focus()

	// 窗口是懒创建 + 复用的：Vue 组件只在首次加载时 mount，Hide/Show 不会重新 mount。
	// 每次唤回都广播一次，供页面刷新主机列表 / 重放会话（回放见 #12）。
	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:ssh:shown")
	}
	return services.Ok(true)
}
