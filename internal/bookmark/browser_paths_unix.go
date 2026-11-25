//go:build darwin || linux

package bookmark

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// browserRoots macOS / Linux 下的候选目录。
//
// 两个平台的差别只在 Application Support 与 .config 这一层，浏览器子路径完全相同，
// 故合在一个文件里按 GOOS 选基目录，不必维护两份几乎一样的清单。
func browserRoots() []browserRoot {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	support := ".config"
	if runtime.GOOS == "darwin" {
		support = filepath.Join("Library", "Application Support")
	}
	join := func(elem ...string) string {
		return filepath.Join(append([]string{home, support}, elem...)...)
	}
	add := func(out []browserRoot, browser, kind, dir string) []browserRoot {
		return append(out, browserRoot{browser: browser, kind: kind, dir: dir})
	}

	var out []browserRoot
	out = add(out, "Chrome", kindChromium, join("Google", "Chrome"))
	out = add(out, "Edge", kindChromium, join("Microsoft Edge"))
	out = add(out, "Brave", kindChromium, join("BraveSoftware", "Brave-Browser"))
	out = add(out, "Chromium", kindChromium, join("Chromium"))
	out = add(out, "Vivaldi", kindChromium, join("Vivaldi"))
	out = add(out, "Firefox", kindFirefox, join("Firefox"))
	return out
}
