//go:build !windows

package platform

import "time"

// IdleFor 在非 Windows 平台不可用：没有跨桌面的「最后输入时间」查询接口。
// 自动锁定因此只在这些平台上退化为「手动锁定」。
func IdleFor() (time.Duration, bool) {
	return 0, false
}
