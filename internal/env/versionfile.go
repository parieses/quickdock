package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// VersionHint 从项目目录的版本声明文件里探测到的一条版本要求。
// Version 保留原始串（"18" / "^8.1" / ">=3.11"），匹配已装版本交给 MatchInstalledVersion。
type VersionHint struct {
	Runtime Runtime `json:"runtime"`
	Version string  `json:"version"`
	Source  string  `json:"source"` // 来源文件名，如 ".nvmrc"
}

// versionNumRE 从版本声明里抽取数字前缀："^8.1" → "8.1"、">=3.11" → "3.11"、"v18.20.0" → "18.20.0"。
var versionNumRE = regexp.MustCompile(`\d+(?:\.\d+)*`)

// asdfRuntimes .tool-versions 的插件名 → 本项目的运行时 id。
// asdf 用插件名（nodejs/golang），与运行时 id（node/go）不同，必须显式映射。
var asdfRuntimes = map[string]Runtime{
	"nodejs":   RuntimeNode,
	"node":     RuntimeNode,
	"php":      RuntimePHP,
	"python":   RuntimePython,
	"golang":   RuntimeGo,
	"go":       RuntimeGo,
	"bun":      RuntimeBun,
	"erlang":   RuntimeErlang,
	"composer": RuntimeComposer,
}

// maxAncestorLevels 向上查找版本文件的层数上限。
// 与 nvm/asdf 一致：版本文件允许放在项目上级目录，就近命中；上限只是防病态目录树。
const maxAncestorLevels = 20

// DetectVersionHints 探测 dir 及其祖先目录里的版本声明文件，返回按运行时去重的版本要求
// （就近的声明文件优先，与 nvm/asdf 的祖先查找语义一致）。
//
// 只做只读探测：文件不存在、无权限、内容不含版本信息，一律静默跳过，不报错——
// 调用方拿到空列表即"没有可识别的版本声明"，不应当因为探测失败而阻断打开流程。
func DetectVersionHints(dir string) []VersionHint {
	if dir == "" {
		return nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	d := dir
	seenRuntime := map[Runtime]bool{}
	out := make([]VersionHint, 0, 4)
	for level := 0; level < maxAncestorLevels; level++ {
		for _, spec := range versionFileSpecs {
			data, err := os.ReadFile(filepath.Join(d, spec.name))
			if err != nil {
				continue
			}
			for _, h := range spec.parse(data) {
				if h.Version == "" || seenRuntime[h.Runtime] {
					continue
				}
				seenRuntime[h.Runtime] = true
				if h.Source == "" {
					h.Source = spec.name
				}
				out = append(out, h)
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break // 已到根
		}
		d = parent
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Runtime < out[j].Runtime })
	return out
}

// versionFileSpec 一个版本声明文件的解析规则。
// 顺序即优先级：同一层目录里前者先命中，后者的同运行时结果被丢弃。
var versionFileSpecs = []struct {
	name  string
	parse func([]byte) []VersionHint
}{
	{".nvmrc", parsePlain(RuntimeNode)},
	{".node-version", parsePlain(RuntimeNode)},
	{".php-version", parsePlain(RuntimePHP)},
	{".python-version", parsePlain(RuntimePython)},
	{"package.json", parsePackageJSON},
	{"composer.json", parseComposerJSON},
	{"go.mod", parseGoMod},
	{".tool-versions", parseToolVersions},
}

// parsePlain 解析"整个文件就是一个版本串"的声明文件（.nvmrc 等）。
func parsePlain(rt Runtime) func([]byte) []VersionHint {
	return func(b []byte) []VersionHint {
		v := firstVersionLine(string(b))
		if v == "" {
			return nil
		}
		return []VersionHint{{Runtime: rt, Version: v}}
	}
}

// parsePackageJSON 从 package.json 的 engines.node 取 node 版本要求。
func parsePackageJSON(b []byte) []VersionHint {
	var doc struct {
		Engines map[string]string `json:"engines"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	v := strings.TrimSpace(doc.Engines["node"])
	if v == "" {
		return nil
	}
	return []VersionHint{{Runtime: RuntimeNode, Version: v}}
}

// parseComposerJSON 从 composer.json 的 require.php 取 php 版本要求。
func parseComposerJSON(b []byte) []VersionHint {
	var doc struct {
		Require map[string]string `json:"require"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	v := strings.TrimSpace(doc.Require["php"])
	if v == "" {
		return nil
	}
	return []VersionHint{{Runtime: RuntimePHP, Version: v}}
}

// parseGoMod 从 go.mod 取 go 版本：toolchain 指令优先（它是实际构建用的），否则用 go 指令。
// 只匹配行首的 "go " / "toolchain "，require 块里的模块路径（go.uber.org/... 等）不会误命中。
func parseGoMod(b []byte) []VersionHint {
	var goVer, toolchain string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "toolchain "):
			toolchain = strings.TrimSpace(strings.TrimPrefix(line, "toolchain "))
		case strings.HasPrefix(line, "go "):
			goVer = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		}
	}
	if m := versionNumRE.FindString(toolchain); m != "" {
		return []VersionHint{{Runtime: RuntimeGo, Version: m}}
	}
	if m := versionNumRE.FindString(goVer); m != "" {
		return []VersionHint{{Runtime: RuntimeGo, Version: m}}
	}
	return nil
}

// parseToolVersions 解析 asdf 风格的 .tool-versions：每行「插件名 版本[ 版本...]」，
// 只取第一个版本（asdf 语义即"首选版本"）。
func parseToolVersions(b []byte) []VersionHint {
	var out []VersionHint
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		rt, ok := asdfRuntimes[strings.ToLower(fields[0])]
		if !ok {
			continue // 有声明但我们不管理该运行时，跳过而不是报错
		}
		if v := stripV(fields[1]); v != "" {
			out = append(out, VersionHint{Runtime: rt, Version: v})
		}
	}
	return out
}

// firstVersionLine 取文本首个非空、非注释行，并剥掉前缀 v。
func firstVersionLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return stripV(line)
	}
	return ""
}

// stripV 去掉版本串的 "v" 前缀与尾部空白。
func stripV(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1 && (s[0] == 'v' || s[0] == 'V') && s[1] >= '0' && s[1] <= '9' {
		return s[1:]
	}
	return s
}

// MatchInstalledVersion 把版本声明（"18" / "^8.1" / ">=3.11" / "1.23.2"）匹配到已安装版本列表里
// 语义最贴近的一个，返回 "" 表示没有可用的已装版本。
//
// 规则：
//  1. 声明串去 v 后与某已装版本完全相等 → 直接命中；
//  2. 否则取声明里的数字前缀，按段比较，从最长前缀（"8.1.0"）逐段回退到最短（"8"），
//     取第一个能命中已装版本的粒度，层内再取版本号最高者。
//
// 回退是必要的："^8.1.0" 在只装了 8.1.10 时应当命中它，而不是判无匹配。
// 刻意不实现完整 semver 区间：那需要引一个 semver 库，而这里的终点只是"从已装的几个里挑一个"，
// 段前缀语义已覆盖 .nvmrc / .tool-versions / engines 的绝大多数实际写法。
func MatchInstalledVersion(spec string, installed []string) string {
	spec = stripV(spec)
	if spec == "" || len(installed) == 0 {
		return ""
	}
	for _, v := range installed {
		if v == spec {
			return v
		}
	}
	nums := versionNumRE.FindString(spec)
	if nums == "" {
		return ""
	}
	segs := strings.Split(nums, ".")
	for n := len(segs); n >= 1; n-- {
		if v := highestWithPrefix(strings.Join(segs[:n], "."), installed); v != "" {
			return v
		}
	}
	return ""
}

// highestWithPrefix 返回已装版本中「前 n 段与 prefix 逐段相等」的最高版本，无命中返回 ""。
// 逐段比较天然落在段边界上，"1.2" 不会误命中 "1.23.0"。
func highestWithPrefix(prefix string, installed []string) string {
	segs := strings.Split(prefix, ".")
	best := ""
	for _, v := range installed {
		vs := strings.Split(v, ".")
		if len(vs) < len(segs) {
			continue
		}
		ok := true
		for i, s := range segs {
			if vs[i] != s {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// compareVersions 按数字段比较版本号（"8.1.10" > "8.1.2"），非数字段退化为字符串比较。
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
			continue
		}
		if as[i] != bs[i] {
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
