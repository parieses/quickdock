package aitools

// 辅助文件目录（WP18）：`<cwd>/.quickdock/tmp/`。
//
// 三个约束叠在一起，所以路径是现在这样：
//  1. **必须是点目录**：不能污染项目根的 `git status`（用户原话：「测试脚本的目录
//     应该生成 .quickdock，不是直接 quickdock 下」）；
//  2. **必须与项目里已有的 .quickdock 同名**：规则（`.quickdock/rules*`）、钩子
//     （`.quickdock/hooks.json`）、记忆（`.quickdock/memory`）都在这里。曾经记忆
//     用的是 `.quickDock`（大写 D）而规则用 `.quickdock` —— Windows 上大小写不敏感
//     所以是同一个目录、问题被掩盖，Linux 上则分裂成两个，表现为「记忆写在 A、
//     规则从 B 读」而谁都不报错。现在统一小写；
//  3. **必须是子目录**：清理动作只删 `tmp/`，绝不碰同级的 `memory/`、`rules/`、
//     `hooks.json`。如果脚本直接躺在 `.quickdock/` 根下，「清理」就只能靠白名单
//     猜哪些是草稿，而猜错一次就是把用户的记忆删了。

import (
	"os"
	"path/filepath"
	"strings"
)

// quickdockDirName 项目内的 QuickDock 隐藏目录。
//
// 必须与 services/ai 的 memoryDirName、ai_rules.go / hooks.go 的取值一致
// （两个包之间没有共享常量，靠这条注释与测试保证）。
const quickdockDirName = ".quickdock"

// scratchSubDir 辅助文件在 .quickdock 下的子目录名。
const scratchSubDir = "tmp"

// ScratchDir 返回某个项目的辅助文件目录（绝对路径）。
func ScratchDir(cwd string) string {
	return filepath.Join(filepath.Clean(cwd), quickdockDirName, scratchSubDir)
}

// ScratchGitignoreHint 写入落在辅助目录、且项目 .gitignore 没忽略 `.quickdock` 时返回一句提示。
//
// 返回空串表示无需提示：没写那个目录 / 项目没有 .gitignore / 已经忽略了。
// 只在「已经有 .gitignore」时提示：没有它的项目通常还没打算用 git 管这些，
// 提示一句「建议加一行」只是噪音。
func ScratchGitignoreHint(cwd, target string) string {
	cwd = strings.TrimSpace(cwd)
	target = strings.TrimSpace(target)
	if cwd == "" || target == "" {
		return ""
	}
	absTarget := target
	// Windows 上 filepath.IsAbs("/x") 是 false，但 POSIX 风格路径在别的平台
	// 就是绝对路径——两边都认，否则同一条路径在两种平台上会走不同分支。
	if !filepath.IsAbs(absTarget) && !strings.HasPrefix(absTarget, "/") && !strings.HasPrefix(absTarget, `\`) {
		absTarget = filepath.Join(cwd, target)
	}
	root := filepath.Join(filepath.Clean(cwd), quickdockDirName)
	absTarget = filepath.Clean(absTarget)
	if absTarget != root && !strings.HasPrefix(absTarget, root+string(filepath.Separator)) {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(filepath.Clean(cwd), ".gitignore"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		l = strings.TrimPrefix(l, "/")
		l = strings.TrimSuffix(l, "/")
		// 只认「整目录」的写法：`.quickdock/*` 之类也算忽略，但 `.quick` 不算。
		l = strings.TrimSuffix(l, "/*")
		if l == quickdockDirName {
			return ""
		}
	}
	return "提示：`" + quickdockDirName + "/` 还没被 .gitignore 忽略，它会出现在 git status 里。" +
		"把这一句转告用户让他自己加（不要直接改他的 .gitignore）。"
}
