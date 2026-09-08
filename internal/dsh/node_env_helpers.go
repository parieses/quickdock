package dsh

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"quickdock/internal/sysutil"
)

func npmGlobalRoot() string {
	npmGlobalRootMu.Lock()
	defer npmGlobalRootMu.Unlock()
	if npmGlobalRootCached {
		return npmGlobalRootVal
	}
	cmd := exec.Command("npm", "root", "-g")
	sysutil.Hide(cmd)
	out, err := cmd.Output()
	if err == nil {
		npmGlobalRootVal = strings.TrimSpace(string(out))
		npmGlobalRootCached = true
	}
	return npmGlobalRootVal
}

// npmDefaultBinDir npm 默认全局 bin 目录（Windows 上为 %AppData%/npm，存放 dsh.cmd 等 shim）
func npmDefaultBinDir() string {
	if ad, err := os.UserConfigDir(); err == nil { // Windows: %AppData%
		return filepath.Join(ad, "npm")
	}
	return ""
}

// npxCacheDshBins 扫描 npx 临时缓存中的 dsh 入口（Windows 缓存可能落在 ~/.npm 或 %LocalAppData%/npm-cache）
func npxCacheDshBins() []string {
	var out []string
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	roots := []string{filepath.Join(home, ".npm")}
	if ld, err := os.UserCacheDir(); err == nil { // Windows: %LocalAppData%
		roots = append(roots, filepath.Join(ld, "npm-cache"))
	}
	for _, r := range roots {
		if st, err := os.Stat(filepath.Join(r, "_npx")); err != nil || !st.IsDir() {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(r, "_npx", "*", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js"))
		if len(matches) > 0 {
			out = append(out, matches...)
		}
	}
	return out
}

// Go 无标准库，这里只覆盖 dsh dist-tags 需要的范围（如 0.1.0-rc.7 vs 0.1.0-rc.8），
// 未处理 build metadata(+x)。
func compareSemver(a, b string) int {
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return -1
	}
	if b == "" {
		return 1
	}
	pa := strings.SplitN(a, "-", 2)
	pb := strings.SplitN(b, "-", 2)
	if c := compareCore(pa[0], pb[0]); c != 0 {
		return c
	}
	// 无 prerelease = 正式版，语义上大于同级 prerelease
	if len(pa) == 1 && len(pb) == 1 {
		return 0
	}
	if len(pa) == 1 {
		return 1
	}
	if len(pb) == 1 {
		return -1
	}
	return comparePre(pa[1], pb[1])
}

func compareCore(a, b string) int {
	av := strings.Split(a, ".")
	bv := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		an, bn := 0, 0
		if i < len(av) {
			an, _ = strconv.Atoi(av[i])
		}
		if i < len(bv) {
			bn, _ = strconv.Atoi(bv[i])
		}
		if an != bn {
			if an < bn {
				return -1
			}
			return 1
		}
	}
	return 0
}

func comparePre(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x == y {
			continue
		}
		if x == "" {
			return -1
		}
		if y == "" {
			return 1
		}
		// 数字标识符 < 字母标识符；纯数字按数值比较，否则字典序
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil && yerr != nil:
			return -1
		case xerr != nil && yerr == nil:
			return 1
		default:
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
