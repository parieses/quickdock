//go:build windows

package dsh

import (
	"os"
	"sort"
	"strconv"
	"strings"

	winreg "golang.org/x/sys/windows/registry"
)

// webView2RegistryKeys 是 WebView2 Evergreen 运行时在注册表中的常见位置（值为 pv）。
// Wails3 默认使用 Evergreen 运行时，其版本以该 Client 的 pv 值记录。
var webView2RegistryKeys = []struct {
	root winreg.Key
	path string
}{
	{winreg.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A08C11}`},
	{winreg.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A08C11}`},
	{winreg.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A08C11}`},
}

// detectWebView2 读取系统 WebView2 运行时版本并对比最低要求。
func detectWebView2() WebView2Check {
	res := WebView2Check{MinVersion: minWebView2Version}
	ver := readWebView2Version()
	if ver == "" {
		// 未安装或注册表/目录均读不到：标记 unknown，由前端给出温和提示（不直接阻拦打开，
		// 以免检测偶发失败误伤正常环境；若真的没装，Wails 建窗会另有明确报错）。
		res.Version = "unknown"
		res.OK = true
		return res
	}
	res.Version = ver
	major, ok := versionMajor(ver)
	if !ok || major < minWebView2Major {
		res.OK = false
		return res
	}
	res.OK = true
	return res
}

// readWebView2Version 优先从注册表读取 Evergreen WebView2 运行时版本，
// 失败则扫描安装目录取最高版本号文件夹。
func readWebView2Version() string {
	for _, k := range webView2RegistryKeys {
		if v, err := readRegStringValue(k.root, k.path, "pv"); err == nil && v != "" {
			return v
		}
	}
	// 回退：扫描 C:\Program Files (x86)\Microsoft\EdgeWebView\Application\<version>
	root := `C:\Program Files (x86)\Microsoft\EdgeWebView\Application`
	if entries, err := os.ReadDir(root); err == nil {
		vers := make([]string, 0, len(entries))
		for _, e := range entries {
			if _, ok := versionMajor(e.Name()); ok && e.IsDir() {
				vers = append(vers, e.Name())
			}
		}
		if len(vers) > 0 {
			sort.Slice(vers, func(i, j int) bool { return compareVersion(vers[i], vers[j]) < 0 })
			return vers[len(vers)-1]
		}
	}
	return ""
}

func readRegStringValue(root winreg.Key, path, name string) (string, error) {
	k, err := winreg.OpenKey(root, path, winreg.READ)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err
}

// versionMajor 取版本号主版本（首个 "." 前的整数段）。非法时返回 0（0 在比较中必然 < 119，
// 但读不到主版本的场景调用方已通过 versionMajor(...) != 0 过滤）。
func versionMajor(v string) (int, bool) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, false
	}
	return n, true
}

// compareVersion 比较两个点分版本号（如 115.0.1901.183 vs 119.0.0），
// 逐段按整数比较，长度不等时短侧以 0 补齐。返回 -1/0/1。
func compareVersion(a, b string) int {
	as := splitVersionInts(a)
	bs := splitVersionInts(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(as) {
			ai = as[i]
		}
		if i < len(bs) {
			bi = bs[i]
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVersionInts(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}
