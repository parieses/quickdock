//go:build windows

package platform

import "time"

// IdleFor 返回距最后一次用户输入的时长（GetLastInputInfo）。
//
// ok=false 表示系统不支持或查询失败；调用方应视为「未知」而不是「刚活动过」，
// 免得一次查询失败就把自动锁定无限期推迟。
func IdleFor() (time.Duration, bool) {
	return sessionIdle()
}
