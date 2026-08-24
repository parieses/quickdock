package services

import "testing"

func TestCompareVersionsPrerelease(t *testing.T) {
	cases := []struct{ a, b string; want int }{
		{"0.1.0-beta", "0.1.0", -1},   // semver: prerelease < 正式版（修复点）
		{"1.0.0-rc.1", "1.0.0", -1},
		{"0.2.0", "0.1.0-beta", 1},
		{"0.10.0", "0.9.0", 1},        // 数字段比数值
		{"0.1.0", "0.1.0", 0},
		{"0.1.0-beta", "0.1.0-alpha", 1},
		{"0.1.0-beta.1", "0.1.0-beta.2", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.1.0-beta", "0.2.0", -1},   // 主版本优先于 prerelease
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
