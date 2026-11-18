package ai

// 辅助文件落点的两个尾巴（WP18）。
//
// 约定是「临时脚本、中间产物一律写 quickdock/」，但约定本身不解决两件事：
//  1. 那个目录会进 git status —— 用户得自己把它加进 .gitignore；
//  2. 攒久了要有人清 —— 否则一个项目里会留下几十个一次性脚本。
//
// 这里刻意**不自动改** .gitignore：静默修改用户仓库里的文件，比多一个未跟踪
// 目录糟糕得多（用户可能正在 review 那个文件）。只给一句提示，并且把「清理」
// 做成显式动作。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"quickdock/internal/fsguard"
	"quickdock/services"
)

// aiScratchDirName 辅助文件目录（相对项目根）。
//
// 点目录 + 与规则/钩子/记忆同名的 `.quickdock`，脚本一律在其 `tmp/` 子目录里。
// 清理只删 `tmp/`——同级的 memory/ 与 rules/ 是用户数据，绝不能连带删掉。
const aiScratchDirName = ".quickdock"

// aiScratchSubDir 辅助文件在 `.quickdock` 下的子目录。
const aiScratchSubDir = "tmp"

// AICleanScratch 清空某个项目的辅助文件目录（WP18 的清理入口）。
//
// 只删 `<cwd>/.quickdock/tmp/`：不做通配、不递归找同名的别处，
// 也不碰会话工作目录之外的东西。cwd 必须是已存在目录，否则拒绝——
// 空串或错的 cwd 会让 filepath.Join 拼到数据目录上去。
func (a *AIService) AICleanScratch(cwd string) *services.ApiResult {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return services.FailMsg("没有工作目录，无法清理")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return services.Fail(err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return services.FailMsg("工作目录不可用")
	}
	// 解引用到真实路径再判边界：`.quickdock` 或 `tmp` 任一段是指向项目外的软链时，
	// 字符串 Clean 看不出来，而 os.Stat 会跟着它指向外部目录——RemoveAll 就会
	// 删掉工作目录之外的东西（注释里一直承诺「不能被软链接带到别处」）。
	// 与写工具的 fsguard 共用同一份解引用实现，口径不再分叉。
	realCwd := fsguard.ResolveExisting(filepath.Clean(abs))
	dir := filepath.Join(realCwd, aiScratchDirName, aiScratchSubDir)
	realDir := fsguard.ResolveExisting(dir)
	// 目标必须正好是 `<cwd>/.quickdock/tmp`，既不能是 `.quickdock` 本身
	// （那会删掉记忆与规则），也不能解引用后跑到 cwd 之外。
	want := filepath.Join(realCwd, aiScratchDirName, aiScratchSubDir)
	if realDir != want || !fsguard.IsWithin(realDir, realCwd) {
		return services.FailMsg("清理目标不合法：辅助目录指向了工作目录之外")
	}
	// Lstat 而非 Stat：软链本身在判边界时已被拒，这里只认真实目录。
	st, serr := os.Lstat(realDir)
	if serr != nil {
		if errors.Is(serr, os.ErrNotExist) {
			return services.OkMsg(nil, "没有需要清理的辅助文件")
		}
		return services.Fail(serr)
	}
	if !st.IsDir() {
		return services.FailMsg("同名路径不是目录，已跳过")
	}
	if rerr := os.RemoveAll(realDir); rerr != nil {
		return services.Fail(rerr)
	}
	return services.OkMsg(nil, "已清理辅助文件目录")
}
