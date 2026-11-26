package sshx

import (
	"strings"

	"quickdock/services"
)

// OpenSshHost 命令面板选中某台主机：打开（或唤回）SSH 窗口并直接连上它。
//
// 与 OpenSshWindow 的差别只有末尾一句事件：窗口是懒创建 + 复用的，Vue 组件只在
// 首次 mount 时读一次主机清单，光把它 Show 出来不会连任何东西。把 hostId 广播
// 过去，页面接到后自己走 selectHost（与点树上的主机完全同一条路径，不另开一套
// 连接逻辑）。
func (s *SshService) OpenSshHost(hostID string) *services.ApiResult {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return services.FailMsg("缺少主机 id")
	}
	if r := s.OpenSshWindow(); r != nil && r.Code != 0 {
		return r
	}
	if app := s.App.App(); app != nil {
		app.Event.Emit("quickdock:ssh:connect", map[string]string{"hostId": hostID})
	}
	return services.Ok(true)
}
