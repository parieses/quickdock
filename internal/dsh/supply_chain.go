package dsh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// dsh 生态中需要执行 build script 的 git 插件「包内部 name」白名单。
// pnpm 供应链策略（allowBuilds）用包内部 name 匹配，而 git 依赖在 package.json / lockfile
// 里是 specifier（github:omdsh-dev/dsh-genui），内部 name（@changfenhuang/dsh-genui）只有
// pnpm 实际解析 git 仓库的 package.json 才可见——静态无法推断，故维护已知名单 + 报错自动学习。
// 键为包内部 name，值为说明（仅注释用途）。
var knownGitBuildDeps = map[string]string{
	"@changfenhuang/dsh-genui": "github:omdsh-dev/dsh-genui 的 git 插件，需执行 build script",
}

// mergeAllowBuilds 将 extra（包内部 name）以 allowBuilds:true 并入 profile 的 pnpm-workspace.yaml。
// 只改 allowBuilds 段，保留其它键（packages/nodeLinker/minimumReleaseAgeExclude…）；
// 文件由 dsh 自身生成，这里只做非破坏性 merge，避免整文件改写破坏 dsh 维护的内容。
// workspaceYaml 不存在（profile 未初始化）时静默返回 nil。
func mergeAllowBuilds(workspaceYaml string, extra []string) error {
	b, err := os.ReadFile(workspaceYaml)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(b, &root); err != nil {
		return fmt.Errorf("解析 %s 失败: %w", filepath.Base(workspaceYaml), err)
	}
	allow, _ := root["allowBuilds"].(map[string]any)
	if allow == nil {
		allow = map[string]any{}
		root["allowBuilds"] = allow
	}
	changed := false
	// 名单 + 动态学到的都并入
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			return
		}
		if _, ok := allow[name]; !ok {
			allow[name] = true
			changed = true
		}
	}
	for n := range knownGitBuildDeps {
		add(n)
	}
	for _, n := range extra {
		add(n)
	}
	if !changed {
		return nil
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("序列化 %s 失败: %w", filepath.Base(workspaceYaml), err)
	}
	return os.WriteFile(workspaceYaml, out, 0644)
}

// allowBuildsErrRe 匹配 pnpm 供应链报错里的包身份：如
//   The git-hosted package "@changfenhuang/dsh-genui@0.9.8" needs to execute build scripts
// 捕获 @scope/name 或 name（不含版本、URL）。allowBuilds 键应写包内部 name 不带版本。
var allowBuildsErrRe = regexp.MustCompile(`package\s+"?((?:@[^/@\s"']+/)?[^@/\s"']+)@`)

// parseBuildDepsFromPnpmErr 从 pnpm 供应链报错文本中提取需要放行 build 的包内部 name。
// 失败返回空切片（不阻断：交给 mergeAllowBuilds 名单 + 用户手动处理）。
func parseBuildDepsFromPnpmErr(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range allowBuildsErrRe.FindAllStringSubmatch(s, -1) {
		if len(m) < 2 {
			continue
		}
		n := strings.TrimSpace(m[1])
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
