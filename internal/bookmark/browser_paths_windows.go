//go:build windows

package bookmark

import (
	"os"
	"path/filepath"
	"strings"
)

// browserRoots Windows 下的候选目录。
//
// 只列「能在用户目录里直读」的浏览器：Windows 商店版 Edge 与常规版共用同一个
// User Data，无需另列。找不到目录时 DiscoverSources 会跳过一个空的候选，
// 因此这里不必逐个 TestPath —— 一次 ReadDir 失败就是不存在。
func browserRoots() []browserRoot {
	local := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	roaming := strings.TrimSpace(os.Getenv("APPDATA"))
	if local == "" && roaming == "" {
		return nil
	}
	join := func(base string, elem ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, elem...)...)
	}
	add := func(out []browserRoot, browser, kind, dir string) []browserRoot {
		if dir == "" {
			return out
		}
		return append(out, browserRoot{browser: browser, kind: kind, dir: dir})
	}

	var out []browserRoot
	out = add(out, "Chrome", kindChromium, join(local, "Google", "Chrome", "User Data"))
	out = add(out, "Edge", kindChromium, join(local, "Microsoft", "Edge", "User Data"))
	out = add(out, "Brave", kindChromium, join(local, "BraveSoftware", "Brave-Browser", "User Data"))
	out = add(out, "Chromium", kindChromium, join(local, "Chromium", "User Data"))
	out = add(out, "Vivaldi", kindChromium, join(local, "Vivaldi", "User Data"))
	out = add(out, "360 极速浏览器", kindChromium, join(local, "360Chrome", "Chrome", "User Data"))
	out = add(out, "QQ 浏览器", kindChromium, join(local, "Tencent", "QQBrowser", "User Data"))
	out = add(out, "Firefox", kindFirefox, join(roaming, "Mozilla", "Firefox"))
	return out
}
