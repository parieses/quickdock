package plugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---- 网络权限（域名白名单）----
//
// permissions.network 支持两种写法（向后兼容旧清单）：
//
//	"network": true                             全放行（与老插件一致，零改动）
//	"network": ["https://api.github.com", "*.example.com"]
//	                                         仅列表内的域名可访问，其余拒绝
//
// 匹配规则（详见 AllowsHost）：
//   - 精确项 `https://api.github.com` 只匹配该 origin（scheme+host 都要对上）
//   - 通配 `*.example.com` 只匹配子域（api.example.com / a.b.example.com），
//     不匹配裸 `example.com`
//   - 仅写 host（`api.github.com`，不带 scheme）则任意 scheme 都允许
//   - 重定向后的最终 host 同样二次校验（见 doPluginHTTP 的 CheckRedirect）
//   - 带 userinfo 的 URL（`http://evil.com@api.github.com`）一律拒绝，
//     否则只看 Host 会被这种写法绕过白名单

// NetworkPerm 网络权限
type NetworkPerm struct {
	All   bool     // 全放行（network: true）
	Hosts []string // 域名白名单（network: [...]）
}

// UnmarshalJSON 兼容 bool 与数组两种写法。
func (n *NetworkPerm) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "", "null", "false":
		*n = NetworkPerm{}
		return nil
	case "true":
		*n = NetworkPerm{All: true}
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return fmt.Errorf(`permissions.network 必须是 true/false 或 ["https://host", "*.example.com"]: %w`, err)
	}
	*n = NetworkPerm{Hosts: arr}
	return nil
}

// MarshalJSON 与输入对称：全放行写回 true，保持老插件入库形状不变。
func (n NetworkPerm) MarshalJSON() ([]byte, error) {
	if n.All {
		return json.Marshal(true)
	}
	return json.Marshal(n.Hosts)
}

// Granted 是否声明了任何网络能力
func (n NetworkPerm) Granted() bool {
	return n.All || len(n.Hosts) > 0
}

// Validate 清单加载时的形式校验：白名单条目必须非空，避免插件带空串静默全拒。
func (n NetworkPerm) Validate() error {
	if n.All {
		return nil
	}
	for _, h := range n.Hosts {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("%w: permissions.network 存在空条目", ErrInvalidManifest)
		}
	}
	return nil
}

// AllowsHost 判断给定 URL 是否落在白名单内。
// 全放行直接返回 true；带白名单时解析 scheme+host 逐项匹配，
// 任何解析失败或含有 userinfo 都拒绝（fail-closed）。
func (n NetworkPerm) AllowsHost(rawURL string) bool {
	if n.All {
		return true
	}
	if len(n.Hosts) == 0 {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// userinfo（如 http://evil.com@api.github.com）本质是绕过：Host 解析为
	// api.github.com，但请求实际发往 evil.com。一律拒绝。
	if u.User != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	for _, rule := range n.Hosts {
		if matchHostRule(rule, scheme, host) {
			return true
		}
	}
	return false
}

// matchHostRule 单条白名单规则匹配。
// rule 可带 scheme（要求 scheme 一致）或仅 host（任意 scheme）。
// 通配 *.example.com 仅匹配子域，不含裸 example.com（需显式另列）。
func matchHostRule(rule, scheme, host string) bool {
	ru, err := url.Parse(rule)
	var ruleScheme, ruleHost string
	if err == nil {
		ruleScheme = strings.ToLower(ru.Scheme)
		ruleHost = strings.ToLower(ru.Hostname())
	}
	if ruleHost == "" {
		// 规则无 scheme（裸 host，如 "api.github.com" 或 "*.example.com"）
		ruleHost = strings.ToLower(rule)
	}
	// 规则带 scheme 时要求一致
	if ruleScheme != "" && ruleScheme != scheme {
		return false
	}
	// 通配：*.example.com 仅匹配子域，不含 apex
	if strings.HasPrefix(ruleHost, "*.") {
		base := ruleHost[len("*."):]
		return host != base && strings.HasSuffix(host, "."+base)
	}
	return host == ruleHost
}
