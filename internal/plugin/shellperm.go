package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- 系统打开权限（host.shell.open 的目标白名单）----
//
// permissions.shell 支持两种写法：
//
//	"shell": true                  全放行（打开任意 URL / 文件 / 目录）
//	"shell": ["https://github.com", "file:///C:/Users/me"]
//	                            仅列表内的目标前缀可打开，其余拒绝
//
// 与 network 的区别：shell 打开的是本地文件/目录/链接，没有重定向二次校验，
// 所以白名单按「目标前缀」匹配（scheme://host 或本地路径前缀），比域名更宽但更可控。
// 文件型目标若用通配，建议写到足够具体的父目录（如 file:///D:/work），避免
// file:/// 直接放行全盘。

// ShellPerm 系统打开权限
type ShellPerm struct {
	All     bool     // 全放行（shell: true）
	Targets []string // 允许打开的目标前缀白名单
}

// UnmarshalJSON 兼容 bool 与数组两种写法。
func (s *ShellPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false":
		*s = ShellPerm{}
		return nil
	case "true":
		*s = ShellPerm{All: true}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf(`permissions.shell 必须是 true/false 或 ["https://github.com", "file:///C:/Users"]: %w`, err)
	}
	*s = ShellPerm{Targets: arr}
	return nil
}

// MarshalJSON 与输入对称：全放行写回 true。
func (s ShellPerm) MarshalJSON() ([]byte, error) {
	if s.All {
		return json.Marshal(true)
	}
	return json.Marshal(s.Targets)
}

// Granted 是否声明了任何 shell 能力
func (s ShellPerm) Granted() bool {
	return s.All || len(s.Targets) > 0
}

// Validate 清单加载时的形式校验：白名单条目必须非空。
func (s ShellPerm) Validate() error {
	if s.All {
		return nil
	}
	for _, t := range s.Targets {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("%w: permissions.shell 存在空条目", ErrInvalidManifest)
		}
	}
	return nil
}

// AllowsTarget 判断目标是否被白名单允许。
// 全放行直接 true；带白名单时大小写不敏感前缀匹配
// （file:///C:/Users 允许其下所有路径，https://github.com 允许其下所有页面）。
// 文件型目标若用通配，建议写到足够具体的父目录（如 file:///D:/work），避免
// file:/// 直接放行全盘。
func (s ShellPerm) AllowsTarget(target string) bool {
	if s.All {
		return true
	}
	if len(s.Targets) == 0 {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(target))
	for _, p := range s.Targets {
		if t == strings.ToLower(strings.TrimSpace(p)) ||
			strings.HasPrefix(t, strings.ToLower(strings.TrimSpace(p))) {
			return true
		}
	}
	return false
}
