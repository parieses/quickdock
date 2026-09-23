package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThemeIconPath 图标按主题解析：浅色档缺失必须回退到清单图标，老插件才不会两个主题都空图。
func TestThemeIconPath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "icon.svg")
	if err := os.WriteFile(base, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("深色主题始终用清单图标", func(t *testing.T) {
		got, ok := themeIconPath(base, "dark")
		if ok || got != base {
			t.Fatalf("got (%q, %v), want (%q, false)", got, ok, base)
		}
	})

	t.Run("浅色档缺失时回退", func(t *testing.T) {
		got, ok := themeIconPath(base, "light")
		if ok || got != base {
			t.Fatalf("got (%q, %v), want (%q, false)", got, ok, base)
		}
	})

	t.Run("浅色档存在时取用", func(t *testing.T) {
		alt := filepath.Join(dir, "icon.light.svg")
		if err := os.WriteFile(alt, []byte("<svg/>"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := themeIconPath(base, "light")
		if !ok || got != alt {
			t.Fatalf("got (%q, %v), want (%q, true)", got, ok, alt)
		}
		// 深色主题不应受影响
		if got, ok := themeIconPath(base, "dark"); ok || got != base {
			t.Fatalf("深色受浅色档影响: (%q, %v)", got, ok)
		}
	})

	t.Run("非 svg 扩展名同样成立", func(t *testing.T) {
		png := filepath.Join(dir, "icon.png")
		got, ok := themeIconPath(png, "light")
		if ok || got != png {
			t.Fatalf("无浅色档应回退: (%q, %v)", got, ok)
		}
		alt := filepath.Join(dir, "icon.light.png")
		if err := os.WriteFile(alt, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, ok := themeIconPath(png, "light"); !ok || got != alt {
			t.Fatalf("got (%q, %v), want (%q, true)", got, ok, alt)
		}
	})

	t.Run("同名目录不算数", func(t *testing.T) {
		d2 := t.TempDir()
		b2 := filepath.Join(d2, "icon.svg")
		if err := os.Mkdir(filepath.Join(d2, "icon.light.svg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, ok := themeIconPath(b2, "light"); ok || got != b2 {
			t.Fatalf("目录不该当图标用: (%q, %v)", got, ok)
		}
	})
}

// TestIconCacheKey 缓存键必须区分主题与版本：否则两档互相覆盖、插件升级后读到旧图。
func TestIconCacheKey(t *testing.T) {
	dark := iconCacheKey("dark", "1.0.0", "com.quickdock.hash-calc")
	light := iconCacheKey("light", "1.0.0", "com.quickdock.hash-calc")
	bumped := iconCacheKey("dark", "1.1.0", "com.quickdock.hash-calc")

	if dark == light {
		t.Error("深浅两档的缓存键不能相同")
	}
	if dark == bumped {
		t.Error("版本变化后缓存键必须变化，否则图标更新不生效")
	}
	// 必须仍落在 app_state 允许的 key 前缀白名单内（否则 SetValue 静默失败、缓存永不生效）
	for _, k := range []string{dark, light, bumped} {
		if !strings.HasPrefix(k, "plugin_icon_") {
			t.Errorf("%q 不在 app_state 的 plugin_icon_ 白名单前缀内", k)
		}
	}
}
