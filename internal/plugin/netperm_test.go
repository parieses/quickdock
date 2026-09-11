package plugin

import (
	"encoding/json"
	"testing"
)

// 这些用例锁定域名白名单的边界：全放行、精确 origin、通配子域、userinfo 绕过防护。
func TestNetworkPermAllowsHost(t *testing.T) {
	cases := []struct {
		name string
		perm NetworkPerm
		url  string
		want bool
	}{
		{"全放行任意 host", NetworkPerm{All: true}, "https://anything.example", true},
		{"未授权拒绝一切", NetworkPerm{}, "https://api.github.com", false},
		{"空白名单拒绝", NetworkPerm{Hosts: nil}, "https://api.github.com", false},
		{"精确 origin 命中", NetworkPerm{Hosts: []string{"https://api.github.com"}}, "https://api.github.com", true},
		{"scheme 不符拒绝", NetworkPerm{Hosts: []string{"https://api.github.com"}}, "http://api.github.com", false},
		{"裸 host 任意 scheme", NetworkPerm{Hosts: []string{"api.github.com"}}, "http://api.github.com", true},
		{"子域命中通配", NetworkPerm{Hosts: []string{"*.example.com"}}, "https://api.example.com", true},
		{"多级子域命中通配", NetworkPerm{Hosts: []string{"*.example.com"}}, "https://a.b.example.com", true},
		{"apex 不匹配通配", NetworkPerm{Hosts: []string{"*.example.com"}}, "https://example.com", false},
		{"根域名需显式列", NetworkPerm{Hosts: []string{"example.com"}}, "https://example.com", true},
		{"其它域名拒绝", NetworkPerm{Hosts: []string{"https://api.github.com"}}, "https://evil.com", false},
		// userinfo 绕过防护：host 解析为 api.github.com，但请求实际发往 evil.com
		{"userinfo 绕过被拒", NetworkPerm{Hosts: []string{"https://api.github.com"}}, "https://evil.com@api.github.com", false},
		{"fragment 注入被拒", NetworkPerm{Hosts: []string{"https://api.github.com"}}, "http://evil.com#@api.github.com", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.perm.AllowsHost(c.url); got != c.want {
				t.Errorf("AllowsHost(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

func TestNetworkPermUnmarshal(t *testing.T) {
	var p Permissions
	// network: true 仍是全放行，老清单零改动
	if err := json.Unmarshal([]byte(`{"network":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Network.All {
		t.Error("network:true 应解析为 All=true")
	}

	// 数组白名单
	if err := json.Unmarshal([]byte(`{"network":["https://api.github.com","*.example.com"]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Network.All || len(p.Network.Hosts) != 2 {
		t.Errorf("数组未正确解析: %+v", p.Network)
	}

	// 非法类型应报错（不静默忽略）
	if err := json.Unmarshal([]byte(`{"network":123}`), &p); err == nil {
		t.Error("network:123 应报错")
	}
}
