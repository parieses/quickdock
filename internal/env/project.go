package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProjectDeps 一个生态（PHP / Composer 或 Node / npm 系）在项目目录里的依赖侧现状。
//
// 与 VersionHint 分工：VersionHint 只回答「项目要求哪个运行时版本」，本结构回答
// 「这个项目用谁管依赖、锁文件在不在、依赖装没装」。全部字段都是机器可读的 id / 数字 /
// 路径，不含用户可见文案——展示文案由前端按 i18n key 生成。
// 纯只读：不执行 composer / npm 命令，也不写任何文件。
type ProjectDeps struct {
	Kind          string `json:"kind"`        // php | node
	Manifest      string `json:"manifest"`    // composer.json | package.json
	ManifestDir   string `json:"manifestDir"` // 清单所在目录（可能在祖先层）
	Manager       string `json:"manager"`     // composer | npm | pnpm | yarn | bun
	ManagerSpec   string `json:"managerSpec"` // packageManager 钉的版本，如 "9.1.0"
	LockFile      string `json:"lockFile"`    // 命中的锁文件名；"" = 未找到
	LockPresent   bool   `json:"lockPresent"`
	LockDeps      int    `json:"lockDeps"`      // 锁文件里的包数；-1 = 该格式不解析
	ManifestDeps  int    `json:"manifestDeps"`  // 清单里声明的直接依赖数（去重）
	DepsDir       string `json:"depsDir"`       // vendor | node_modules
	DepsInstalled bool   `json:"depsInstalled"` // 依赖目录存在且非空
	LockStale     bool   `json:"lockStale"`     // 锁文件比清单旧，可能已过期
}

// maxLockFileBytes 超过这个大小的锁文件只记录「存在」，不解析包数。
// composer.lock / package-lock.json 在 monorepo 里可能好几 MB，全部读进内存换一个
// 展示用的数字不划算，也会让「检查项目」在慢盘上卡一下。
const maxLockFileBytes = 8 << 20

// nodeLockCandidates Node 侧锁文件候选，顺序即推断包管理器的优先级。
// pnpm / yarn / bun 的锁文件是 YAML 或二进制，不解析内容，只认存在。
var nodeLockCandidates = []struct {
	file    string
	manager string
}{
	{"pnpm-lock.yaml", "pnpm"},
	{"yarn.lock", "yarn"},
	{"bun.lockb", "bun"},
	{"bun.lock", "bun"},
	{"package-lock.json", "npm"},
	{"npm-shrinkwrap.json", "npm"},
}

// DetectProjectDeps 从 dir 及其祖先目录里找 composer.json / package.json，各取最近的一份，
// 返回两个生态的依赖快照。就近优先与 DetectVersionHints 一致；两个生态互不排斥
// （PHP 项目里常带一个前端 package.json）。
//
// 目录不存在、清单损坏时不会报错：损坏的清单仍算「识别到了项目」，只是字段取不到值，
// 这比直接返回空数组让前端说「没检测到任何项目文件」更贴近事实。
func DetectProjectDeps(dir string) []ProjectDeps {
	out := make([]ProjectDeps, 0, 2)
	if dir == "" {
		return out
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	var phpDir, nodeDir string
	d := dir
	for level := 0; level < maxAncestorLevels; level++ {
		if phpDir == "" {
			if st, err := os.Stat(filepath.Join(d, "composer.json")); err == nil && !st.IsDir() {
				phpDir = d
			}
		}
		if nodeDir == "" {
			if st, err := os.Stat(filepath.Join(d, "package.json")); err == nil && !st.IsDir() {
				nodeDir = d
			}
		}
		if phpDir != "" && nodeDir != "" {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if phpDir != "" {
		out = append(out, inspectPHPDeps(phpDir))
	}
	if nodeDir != "" {
		out = append(out, inspectNodeDeps(nodeDir))
	}
	return out
}

// inspectPHPDeps 读 composer.json / composer.lock / vendor。
func inspectPHPDeps(dir string) ProjectDeps {
	d := ProjectDeps{
		Kind: "php", Manifest: "composer.json", ManifestDir: dir,
		Manager: "composer", DepsDir: "vendor", LockDeps: -1,
	}
	manifestPath := filepath.Join(dir, "composer.json")
	manifestTime := fileModTime(manifestPath)

	if data, err := os.ReadFile(manifestPath); err == nil {
		var doc struct {
			Require    map[string]json.RawMessage `json:"require"`
			RequireDev map[string]json.RawMessage `json:"require-dev"`
		}
		if json.Unmarshal(data, &doc) == nil {
			seen := map[string]bool{}
			for _, src := range []map[string]json.RawMessage{doc.Require, doc.RequireDev} {
				for name := range src {
					if isComposerPackage(name) {
						seen[name] = true
					}
				}
			}
			d.ManifestDeps = len(seen)
		}
	}

	lockPath := filepath.Join(dir, "composer.lock")
	if st, err := os.Stat(lockPath); err == nil && !st.IsDir() {
		d.LockPresent, d.LockFile = true, "composer.lock"
		d.LockDeps = readComposerLockDeps(lockPath)
		d.LockStale = !manifestTime.IsZero() && st.ModTime().Before(manifestTime)
	}
	d.DepsInstalled = dirHasEntries(filepath.Join(dir, "vendor"))
	return d
}

// inspectNodeDeps 读 package.json、packageManager 与锁文件 / node_modules。
func inspectNodeDeps(dir string) ProjectDeps {
	d := ProjectDeps{
		Kind: "node", Manifest: "package.json", ManifestDir: dir,
		Manager: "npm", DepsDir: "node_modules", LockDeps: -1,
	}
	manifestPath := filepath.Join(dir, "package.json")
	manifestTime := fileModTime(manifestPath)

	if data, err := os.ReadFile(manifestPath); err == nil {
		var doc struct {
			PackageManager       string                     `json:"packageManager"`
			Dependencies         map[string]json.RawMessage `json:"dependencies"`
			DevDependencies      map[string]json.RawMessage `json:"devDependencies"`
			OptionalDependencies map[string]json.RawMessage `json:"optionalDependencies"`
			PeerDependencies     map[string]json.RawMessage `json:"peerDependencies"`
		}
		if json.Unmarshal(data, &doc) == nil {
			seen := map[string]bool{}
			for _, src := range []map[string]json.RawMessage{
				doc.Dependencies, doc.DevDependencies, doc.OptionalDependencies, doc.PeerDependencies,
			} {
				for name := range src {
					if name != "" {
						seen[name] = true
					}
				}
			}
			d.ManifestDeps = len(seen)
			// packageManager 是 corepack 的权威声明，比锁文件更可信：迁移中的项目可能
			// 同时残留 package-lock.json，此时仍应显示 pnpm。
			if name, ver := splitPackageManager(doc.PackageManager); name != "" {
				d.Manager, d.ManagerSpec = name, ver
			}
		}
	}

	for _, c := range nodeLockCandidates {
		st, err := os.Stat(filepath.Join(dir, c.file))
		if err != nil || st.IsDir() {
			continue
		}
		d.LockPresent, d.LockFile = true, c.file
		d.LockDeps = readNodeLockDeps(filepath.Join(dir, c.file), c.file)
		d.LockStale = !manifestTime.IsZero() && st.ModTime().Before(manifestTime)
		// 只有 packageManager 没钉包管理器时，才由锁文件反推（npm 为默认值）。
		if d.ManagerSpec == "" && d.Manager == "npm" {
			d.Manager = c.manager
		}
		break
	}
	d.DepsInstalled = dirHasEntries(filepath.Join(dir, "node_modules"))
	return d
}

// splitPackageManager 拆 "pnpm@9.1.0+sha512.abc" → ("pnpm", "9.1.0")。
// 用最后一个 @ 分隔以兼容带 scope 的名字；+ 后面是 corepack 的完整性哈希，不展示。
// 未知包管理器返回空名，让调用方回退到锁文件推断。
func splitPackageManager(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 {
		return "", ""
	}
	name := strings.ToLower(strings.TrimSpace(s[:at]))
	switch name {
	case "npm", "pnpm", "yarn", "bun":
	default:
		return "", ""
	}
	ver := strings.TrimSpace(s[at+1:])
	if i := strings.IndexByte(ver, '+'); i >= 0 {
		ver = ver[:i]
	}
	return name, ver
}

// isComposerPackage 排除 Composer 的 platform package（php / ext-* / lib-* / composer-*）。
// 注意不能一刀切排除 "php-" 前缀：php-http/* 之类是真实第三方包。
func isComposerPackage(name string) bool {
	if name == "" {
		return false
	}
	low := strings.ToLower(name)
	switch low {
	case "php", "hhvm", "php-64bit":
		return false
	}
	return !strings.HasPrefix(low, "ext-") &&
		!strings.HasPrefix(low, "lib-") &&
		!strings.HasPrefix(low, "composer-")
}

// readComposerLockDeps 只数 packages / packages-dev 的条数，不展开具体包信息。
func readComposerLockDeps(path string) int {
	if st, err := os.Stat(path); err != nil || st.Size() > maxLockFileBytes {
		return -1
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxLockFileBytes {
		return -1
	}
	var doc struct {
		Packages    []json.RawMessage `json:"packages"`
		PackagesDev []json.RawMessage `json:"packages-dev"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return -1
	}
	return len(doc.Packages) + len(doc.PackagesDev)
}

// readNodeLockDeps 只解析 npm 的 JSON 锁文件；pnpm/yarn/bun 的 YAML / 二进制返回 -1。
func readNodeLockDeps(path, file string) int {
	if file != "package-lock.json" && file != "npm-shrinkwrap.json" {
		return -1
	}
	if st, err := os.Stat(path); err != nil || st.Size() > maxLockFileBytes {
		return -1
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxLockFileBytes {
		return -1
	}
	var doc struct {
		Packages     map[string]json.RawMessage `json:"packages"`     // lockfileVersion 2/3
		Dependencies map[string]json.RawMessage `json:"dependencies"` // lockfileVersion 1
	}
	if json.Unmarshal(data, &doc) != nil {
		return -1
	}
	if doc.Packages != nil {
		n := len(doc.Packages)
		if _, ok := doc.Packages[""]; ok {
			n-- // 根包占位不算依赖
		}
		if n < 0 {
			n = 0
		}
		return n
	}
	if doc.Dependencies != nil {
		return len(doc.Dependencies)
	}
	return -1
}

// fileModTime 取文件修改时间；取不到返回零值，调用方据此跳过「锁文件过期」判断。
func fileModTime(path string) time.Time {
	if st, err := os.Stat(path); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// dirHasEntries 目录存在且非空才算「依赖已安装」：空的 node_modules / vendor
// 常见于一次失败的安装，判成已安装会误导。
func dirHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}
